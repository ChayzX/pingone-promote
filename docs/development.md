# Development

For operators, start at the [README](../README.md). Design background: [ADR 0018](adr/0018-hosted-source-only-server.md) (hosted server), [ADR 0019](adr/0019-core-pingone-exporter.md) (native core exporter), ADRs 0014-0016. Current state: [HANDOFF.md](HANDOFF.md).

Platforms: macOS and Linux only. Windows is not supported; a few tests carry small Windows allowances (POSIX mode and owner checks skip) so development there is possible, but it proves nothing about the server.

## Architecture

```
browser
  |  HTML forms (no JavaScript), CSRF + Host checks
  v
serve (cmd/pingone-promote/serve.go) -> internal/server   drafts, background jobs, pages
  |
  v
request engine (internal/request)      preflight, redaction, result
  |
  v
multiexport (internal/multiexport)
  |-- pingcli-terraformer v0.6.0 (internal/exporter)   DaVinci
  '-- coreexport (internal/coreexport)                 core PingOne
        '-- internal/pingone   worker token + GET only
  |
  v
neutralize (internal/neutralize)  -> shared definitions + per-environment values
  v
promote merge (internal/promote, hclblocks, overlay, pipelinesecrets)   additive
  v
tffmt (internal/tffmt)            terraform fmt gate
  v
gitlab HostedCommitter (internal/gitlab)   branch + one commit + merge request
```

The server never runs `terraform plan` or `apply` and holds no PingOne write credential.

## Packages

| Package | Role |
| --- | --- |
| `cmd/pingone-promote` | `serve`, `check`, `export`, `request` commands. |
| `internal/server` | HTTP server, config and secrets-file loading, drafts, jobs, pages, Requests page. |
| `internal/request` | The request engine shared by the hosted server and the legacy CI job. |
| `internal/multiexport` | Combines the DaVinci and core exporters behind one interface. |
| `internal/exporter` | Pinned pingcli-terraformer runner; `exportertest` has fixtures and a fake binary. |
| `internal/coreexport` | Reads every catalog type and renders terraformer-shaped HCL. |
| `internal/pingone` | Read-only PingOne client; `pingonetest` is the fake API. |
| `internal/catalog` | Registry of all 101 provider resource types; generates `docs/coverage.md`. |
| `internal/neutralize` | Export to shared definitions plus source values. |
| `internal/promote`, `internal/hclblocks` | Additive merge, blockers, `check --root`, byte-range block edits. |
| `internal/overlay`, `internal/pipelinesecrets`, `internal/secretscan` | Overlays, secret declarations, secret detection. |
| `internal/tffmt` | The `terraform fmt` gate. |
| `internal/gitlab` | GitLab client with path allow-list, snapshots, `HostedCommitter`, status; `gitlabtest` is the fake. |
| `terraform/root`, `davinci-app/`, `examples/gitlab-ci/` | Reference root; legacy DaVinci app and CI example. |

## Running the checks

```sh
bash scripts/fetch-tools.sh --dest ./tools --with-go
export PATH="$PWD/tools:$PWD/tools/go/bin:$PATH"     # Terraform 1.16.7, Go 1.27.3
export PINGONE_PROMOTE_TERRAFORM="$PWD/tools/terraform"

gofmt -l .                       # must print nothing
go build ./... && go vet ./...
go test ./...
go test -race ./internal/server/... ./internal/gitlab/...   # Linux/macOS (needs cgo)
terraform fmt -check -recursive terraform/

# Legacy DaVinci app and release script checks
python3 -I davinci-app/check_app.py && node davinci-app/test_app.js
python3 -m unittest discover -s scripts -p test_release.py
```

Without Terraform 1.16.7 on PATH some engine tests skip their formatter gate; that is not full verification. Set `PINGONE_PROMOTE_REAL_TERRAFORMER` to the real pingcli-terraformer to run its version contract test.

## Fixtures and fakes

- `internal/exporter/exportertest/fixtures/dev` and `cross`: DaVinci export fixtures (`cross` also references core objects); `faketerraformer` is a stand-in binary. Regenerate with `PINGONE_PROMOTE_REGEN_FIXTURE`.
- `internal/pingone/pingonetest` and `internal/coreexport/testdata/tenant.json`: a fake PingOne API serving a synthetic tenant. Shapes are written from documentation, not recorded; every core row in `docs/coverage.md` stays `Live: no` until [core-export.md](acceptance/core-export.md) passes.
- `internal/gitlab/gitlabtest`: a fake GitLab server (branches, commits, merge requests, pipelines).

Store no real credentials or tenant data in fixtures, output or commits.

## Adding or fixing a resource type

1. Edit `internal/catalog/catalog.go`: add or adjust the definition (path, parent, name attributes, renames, hoists, built-in rule, import kind, limits).
2. Add the object to `internal/coreexport/testdata/tenant.json` and a case in `internal/coreexport/coreexport_test.go`.
3. Regenerate the matrix: `PINGONE_PROMOTE_WRITE_COVERAGE=1 go test ./internal/catalog`. The plain test fails when `docs/coverage.md` is stale.
4. Changing the provider pin: regenerate `internal/catalog/provider_schema.json` with `internal/catalog/schemagen` (the command is at the top of its source file), then repeat step 3.
5. Add or update the live case in [core-export.md](acceptance/core-export.md).

## CI annotations

`.github/workflows/ci.yml` runs on Linux and macOS, installs the pinned tools, and fails on unexpected test skips (only Windows and REGEN_FIXTURE skips are allowed). Failures are also written as `::error` annotations (go test failures, failing packages, panics, skip reasons) so they can be read without signing in to GitHub.

## Rules

Keep HTTP handlers free of resource-specific logic. Serialize draft mutations, invalidate stale previews, and bind published content to the previewed export and Git SHA. Tests must cover concurrent and stale operations, uncertain GitLab outcomes, every POST security check, and secrets in blocked previews. Use small, independently reviewed changes; merge only when CI is green for the reviewed commit.
