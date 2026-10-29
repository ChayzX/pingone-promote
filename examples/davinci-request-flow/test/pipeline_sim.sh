#!/usr/bin/env bash
# Replays each intent the flow committed the way GitLab sees it: a branch from
# main whose only change adds intents/<id>/intent.json. Then the Go tool's own
# select-request.sh (merge request pipeline) picks the file and
# `pingone-runner validate --intent` checks it, as in validate_request. After
# the merge, plan_promotion runs `pingone-runner build-request` on it.
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
    mkdir intents && touch intents/.keep && git add . && git commit -qm base
    base="$(git rev-parse HEAD)"
    git checkout -qb "promotion/$id"
    mkdir -p "intents/$id" && cp "$f" "intents/$id/intent.json"
    git add . && git commit -qm "Add promotion intent $id"
    CI_PIPELINE_SOURCE=merge_request_event CI_MERGE_REQUEST_DIFF_BASE_SHA="$base" CI_COMMIT_SHA="$(git rev-parse HEAD)" \
      bash -c "source '$select' && '$runner' validate --intent \"\$PROMOTION_INTENT_FILE\"" > out.txt
    [[ "$(cat out.txt)" == "valid" && "$(cat selected-request.txt)" == "intents/$id/intent.json" ]] || { cat out.txt; exit 1; }
  )
  rm -rf "$repo"
  n=$((n + 1))
done
echo "pipeline: $n flow-committed intents selected and validated by the Go runner"
