#!/usr/bin/env bash
# One promotion project, several GitLab environments: replays each request the
# flow committed as a post-merge push to main. For every environment, the Go
# tool's select-request.sh picks the file and for-this-environment.sh decides
# whether that environment's jobs act on it. Exactly the environment whose
# trusted target is the request's target must act.
# usage: test/one_project_sim.sh SELECT_SCRIPT ROUTE_SCRIPT DIR_OF_SAVED_REQUESTS
set -euo pipefail
select="$(realpath "$1")"; route="$(realpath "$2")"; saved="$(realpath "$3")"
n=0
for f in "$saved"/flow-*.json; do
  repo="$(mktemp -d)"
  read -r id target source < <(node -e 'const r=JSON.parse(require("fs").readFileSync(process.argv[1],"utf8"));console.log(r.id,r.target.id,r.source.id)' "$f")
  (
    cd "$repo"
    git init -q -b main && git config user.email t@example.com && git config user.name t
    mkdir requests && touch requests/.keep && git add . && git commit -qm base
    before="$(git rev-parse HEAD)"
    mkdir -p "requests/$id" && cp "$f" "requests/$id/request.json" && git add . && git commit -qm "Add promotion request $id"
    sha="$(git rev-parse HEAD)"
    job() { # env trusted-target [PROMOTION_ENVIRONMENT]
      env -i PATH="$PATH" CI_PIPELINE_SOURCE=push CI_COMMIT_BEFORE_SHA="$before" CI_COMMIT_SHA="$sha" CI_ENVIRONMENT_NAME="$1" \
        PINGONE_TARGET_ENVIRONMENT_ID="$2" ${3:+PROMOTION_ENVIRONMENT="$3"} \
        bash -c "source '$select' && source '$route' && echo \"decision=\$PP_FOR_THIS_ENVIRONMENT env=\$PROMOTION_ENVIRONMENT\"" 2>&1
    }
    acted=0
    for pair in "dev:$source" "test:$target" "production:99999999-9999-4999-8999-999999999999"; do
      out="$(job "${pair%%:*}" "${pair#*:}")"
      if [[ "$out" == *"decision=true env=${pair%%:*}"* ]]; then acted=$((acted + 1)); [[ "${pair%%:*}" == test ]] || { echo "wrong environment acted: $out"; exit 1; }
      elif [[ "$out" != *"decision=false"* || "$out" != *"Not for ${pair%%:*}"* ]]; then echo "unexpected: $out"; exit 1; fi
    done
    [[ $acted -eq 1 ]] || { echo "expected exactly one environment to act, got $acted"; exit 1; }
    # A PROMOTION_ENVIRONMENT scoped to another environment, or a missing target, stops the job.
    ! job test "$target" production > out.txt && grep -q "differs from this job's GitLab environment" out.txt || { cat out.txt; exit 1; }
    ! job test "" > out.txt && grep -q "PINGONE_TARGET_ENVIRONMENT_ID is not set" out.txt || { cat out.txt; exit 1; }
  )
  rm -rf "$repo"
  n=$((n + 1))
done
echo "one project: $n flow-committed requests routed to exactly the environment that deploys to their target"
