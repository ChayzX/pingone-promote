# PingOne Promote

PingOne Promote moves a DaVinci flow (and what it depends on) from a source PingOne environment into a Terraform config repository, as a reviewable merge request. It has three parts:

- **DaVinci app** ([`davinci-app/`](davinci-app/README.md)): the front end people sign in to. They pick a source environment and a main flow, preview the change, submit it and watch the rollout. It writes one request file on a `pingone-promote/<requestId>` branch and holds no PingOne write credentials.
- **CLI** (`cmd/pingone-promote`): one binary with three subcommands (below). It makes no local server and has no UI.
- **CI job** (`pingone-promote request`): runs in the config repository's GitLab pipeline, triggered by the request branch. It exports the source with a pinned [pingcli-terraformer](docs/adr/0015-pingcli-terraformer-exporter.md), neutralizes it into shared definitions plus one overlay per environment, merges additively, and commits the result to the same branch (and opens a merge request). It never plans or applies; deployment stays in the enterprise Terraform pipeline.

Details are in the documents below.

## CLI

```sh
go build -o pingone-promote ./cmd/pingone-promote
```

Go 1.27.3 or newer is required to build. Running the binary with no subcommand prints usage and exits 2. `--version` prints the version.

| Command | Purpose |
| --- | --- |
| `pingone-promote check --root DIR` | Validates a config repository root offline: every overlay sets the required variables and no secrets, imports are conditional, no `*.auto.tfvars`, declared pipeline secrets, no masked values or keys, and optionally `terraform fmt` / `terraform validate`. Exit 0 valid, 1 problems, 2 usage. |
| `pingone-promote export --source ENV --flow "NAME or ID"` | Runs the export, neutralize and merge locally against a checkout, with `--dry-run` to preview. |
| `pingone-promote request` | The CI request job: reads `.pingone-promote/request.json` from the checked-out branch and writes `result.json`. See the [CI contract](docs/gitlab-ci-integration.md). |

Run a subcommand with `--help` for its flags and environment variables.

## Layout

- `internal/exporter`: the pinned pingcli-terraformer runner and search.
- `internal/neutralize`: export to shared definitions plus source values.
- `internal/promote`, `internal/hclblocks`: the additive merge and `check --root`.
- `internal/overlay`, `internal/tffmt`: per-environment overlays and the `terraform fmt` gate.
- `internal/request`: the CI request job.
- `internal/gitlab`: GitLab API client with a path allow-list and branch commits.
- `terraform/root`, `terraform/spikes`: the reference root and the conditional-import spike.
- `examples/gitlab-ci/pingone-promote.yml`: the job to include in the config repository.

## Documentation

- [DaVinci app guide](davinci-app/README.md): import, configure and operate the front end.
- [GitLab CI integration](docs/gitlab-ci-integration.md): the request file, the job and its trust boundary.
- [Architecture decisions](docs/adr/README.md): why it is built this way (start with 0014 to 0017).
- [Live acceptance](docs/acceptance/live-acceptance.md): what has been verified against live tenants.
- [`docs/archive/`](docs/archive/): superseded v1 documents, kept for history.

## Build release bundles

`scripts/build.sh` cross-builds CGO-free `pingone-promote` binaries for macOS arm64/amd64 and Linux amd64 and writes `.tar.gz` bundles and `SHA256SUMS` under `dist/`; it honors `GO` and `VERSION`. `scripts/release.py` exports the source archive from Git, generates and verifies the Homebrew formula, and `scripts/test_homebrew.py` exercises a real install, upgrade and uninstall in a temporary tap. The release workflow produces a draft prerelease only; review it before publishing the formula to the tap.

## Checks

```sh
go build ./... && go vet ./... && go test ./...
terraform fmt -check -recursive terraform/
python3 -I davinci-app/check_app.py && node davinci-app/test_app.js
python3 -m unittest discover -s scripts -p test_release.py
```
