#!/usr/bin/env bash
# Replays each committed request from the flow run the way GitLab sees it: a
# branch from main whose only change adds requests/<id>/request.json. Then the
# Go tool's own select-request.sh (merge request pipeline) picks the file and
# `pingone-runner validate --request` checks it, as in validate_request.
# usage: test/pipeline_sim.sh RUNNER SELECT_SCRIPT DIR_OF_SAVED_REQUESTS
set -euo pipefail
runner="$1"; select="$2"; saved="$3"
n=0
for f in "$saved"/flow-*.json; do
  repo="$(mktemp -d)"
  id="$(node -e 'console.log(JSON.parse(require("fs").readFileSync(process.argv[1],"utf8")).id)' "$f")"
  (
    cd "$repo"
    git init -q -b main && git config user.email t@example.com && git config user.name t
    mkdir requests && touch requests/.keep && git add . && git commit -qm base
    base="$(git rev-parse HEAD)"
    git checkout -qb "promotion/$id"
    mkdir -p "requests/$id" && cp "$f" "requests/$id/request.json"
    git add . && git commit -qm "Add promotion request $id"
    CI_PIPELINE_SOURCE=merge_request_event CI_MERGE_REQUEST_DIFF_BASE_SHA="$base" CI_COMMIT_SHA="$(git rev-parse HEAD)" \
      bash -c "source '$select' && '$runner' validate --request \"\$PROMOTION_REQUEST_FILE\"" > out.txt
    [[ "$(cat out.txt)" == "valid" && "$(cat selected-request.txt)" == "requests/$id/request.json" ]] || { cat out.txt; exit 1; }
  )
  rm -rf "$repo"
  n=$((n + 1))
done
echo "pipeline: $n flow-committed requests selected and validated by the Go runner"
