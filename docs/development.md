# Developer guide

How to build, test and release pingone-promote. Operators setting the tool up need the [README](../README.md) instead.

## CLI

```sh
go build -o pingone-promote ./cmd/pingone-promote
```

Go 1.27.3 or newer is required to build. Running the binary with no subcommand prints usage and exits 2. `--version` prints the version.

| Command | Purpose |
| --- | --- |
| `pingone-promote check --root DIR` | Validates a config repository root offline: every overlay sets the required variables and no secrets, imports are conditional, no `*.auto.tfvars`, declared pipeline secrets, no masked values or keys, and optionally `terraform fmt` / `terraform validate`. Exit 0 valid, 1 problems, 2 usage. |
| `pingone-promote export --source ENV --flow "NAME or ID"` | Runs the export, neutralize and merge locally against a checkout, with `--dry-run` to preview. |
| `pingone-promote request` | The CI request job: reads `.pingone-promote/request.json` from the checked-out branch and writes `result.json`. See the [CI contract](gitlab-ci-integration.md). |

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

- [DaVinci app guide](../davinci-app/README.md): import, configure and operate the front end.
- [GitLab CI integration](gitlab-ci-integration.md): the request file, the job and its trust boundary.
- [Architecture decisions](adr/README.md): why it is built this way (start with 0014 to 0017).
- [Live acceptance](acceptance/live-acceptance.md): what has been verified against live tenants.
- [`docs/archive/`](archive/): superseded v1 documents, kept for history.

## Build release bundles

`scripts/build.sh` cross-builds CGO-free `pingone-promote` binaries for macOS arm64/amd64 and Linux amd64 and writes `.tar.gz` bundles and `SHA256SUMS` under `dist/`; it honors `GO` and `VERSION`. `scripts/release.py` exports the source archive from Git, generates and verifies the Homebrew formula, and `scripts/test_homebrew.py` exercises a real install, upgrade and uninstall in a temporary tap. The release workflow produces a draft prerelease only; review it before publishing the formula to the tap.

## Checks

```sh
go build ./... && go vet ./... && go test ./...
terraform fmt -check -recursive terraform/
python3 -I davinci-app/check_app.py && node davinci-app/test_app.js
(cd davinci-app && python3 gen_app.py --sign-in subflow --out out/sso.flow.json && python3 check_app.py out/sso.flow.json && node test_app.js out/sso.flow.json)
python3 -m unittest discover -s scripts -p test_release.py
```
