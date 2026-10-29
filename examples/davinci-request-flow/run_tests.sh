#!/usr/bin/env bash
# Offline tests for the DaVinci Request flow. The Go cross-checks need the Go
# tool at commit 537dbf4: inside this repository it is found automatically;
# elsewhere set GO_TOOL to a checkout. It is cloned into a temporary
# directory and never modified.
set -euo pipefail
cd "$(dirname "$0")"
if [[ -z "${GO_TOOL:-}" ]] && root="$(git rev-parse --show-toplevel 2>/dev/null)" && [[ -f "$root/go.mod" ]]; then
  GO_TOOL="$root"
fi
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

node test/unit.js
python3 -I gen_flows.py "$work/out" values.example.json >/dev/null
python3 -I check_flows.py "$work/out"/*.json
node test/shipped_code.js "$work/out"/*.json
# The same checks with sign-in on (off is the proof-of-concept default).
python3 -c 'import json,sys; v=json.load(open("values.example.json")); v["requireSignIn"]=True; json.dump(v,open(sys.argv[1],"w"))' "$work/signin.json"
python3 -I gen_flows.py "$work/signin" "$work/signin.json" >/dev/null
python3 -I check_flows.py "$work/signin"/*.json
node test/shipped_code.js "$work/signin"/*.json
node test/flow_run.js "$work/signin/pingone-promote-request.flow.json"
# The committed Terraform flow must match the generator.
mkdir "$work/tf" && python3 -I gen_flows.py --terraform "$work/tf" >/dev/null
cmp -s "$work/tf/flow.tf.json" terraform/flow.tf.json || { echo "terraform/flow.tf.json is stale: run python3 gen_flows.py --terraform terraform"; exit 1; }
echo "terraform/flow.tf.json matches the generator"
if command -v terraform >/dev/null; then
  (cd terraform && TF_DATA_DIR="$work/tfdata" terraform init -backend=false -input=false >/dev/null && TF_DATA_DIR="$work/tfdata" terraform validate -no-color)
fi
mkdir "$work/saved"
node test/flow_run.js "$work/out/pingone-promote-request.flow.json" "$work/saved"

if [[ -z "${GO_TOOL:-}" ]]; then
  echo "GO_TOOL not set: skipping the Go cross-checks"
  exit 0
fi
go="$work/go"
git clone -q "$GO_TOOL" "$go" && git -C "$go" checkout -q 537dbf4
cp test/testdata/go/xcheck_drift.go "$go/internal/gitops/"
cp test/testdata/go/xinv_export.go "$go/internal/pingone/"
cp test/testdata/go/xgitlab_export.go "$go/internal/gitlab/"
mkdir -p "$go/cmd/xcheck" "$go/cmd/xinv" "$go/cmd/xreq"
cp test/testdata/go/xcheck_main.go "$go/cmd/xcheck/main.go"
cp test/testdata/go/xinv_main.go "$go/cmd/xinv/main.go"
cp test/testdata/go/xreq_main.go "$go/cmd/xreq/main.go"
(cd "$go" && for c in xcheck xinv xreq pingone-runner; do go build -o "$work/$c" "./cmd/$c"; done)

# BuildRequest rules and Redact
python3 -I test/fixtures.py > "$work/f.json" && "$work/xcheck" < "$work/f.json" > "$work/g.json"
node test/xcheck.js "$work/f.json" "$work/g.json"
# Inventory, InventoryVersion and pagination against the real Go client
python3 -I test/inv_fixtures.py > "$work/i.json" && "$work/xinv" < "$work/i.json" > "$work/ig.json"
node test/xinv.js "$work/i.json" "$work/ig.json"
# request.json bytes: NewRequest + Encode, then Decode of the JS output
python3 -I test/req_fixtures.py > "$work/r.json" && node test/xreq.js "$work/r.json" "$work/rj.json"
"$work/xreq" "$work/r.json" "$work/rj.json"
# GitLab settings validation and returned-URL checks
python3 -I test/gitlab_fixtures.py > "$work/gl.json" && node test/xgitlab.js "$work/gl.json" "$work/glj.json"
"$work/xreq" "$work/gl.json" "$work/glj.json" gitlab
# What the flow committed, through the pipeline's selector and validator
bash test/pipeline_sim.sh "$work/pingone-runner" "$go/examples/gitlab/select-request.sh" "$work/saved"
