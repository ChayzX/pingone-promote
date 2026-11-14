# Hosted server handoff (current state)

The hosted, source-only server of [ADR 0018](adr/0018-hosted-source-only-server.md) with the native core exporter of [ADR 0019](adr/0019-core-pingone-exporter.md). Operators: see the [README](../README.md). Developers: [development.md](development.md).

## Built and tested (fixtures only)

- `pingone-promote serve --config FILE [--check-config]`: net/http server, embedded pages, no JavaScript, strict CSP, Host allow-list (421), CSRF plus same-origin checks on every POST, `/healthz`. Drafts, previews and submissions are in memory and run as cancellable background jobs. Phase 1 has no sign-in: typed names are `NAME (unverified)` behind the `server.Identity` interface.
- Secrets file checks (regular file, not a symlink, mode 0600/0400, owned by the server user), config refusals, pinned tool digests.
- GitLab: private snapshot of one commit, `HostedCommitter` rooted at the previewed SHA, refuses when the default branch moved, keeps the branch on uncertain outcomes, Requests page with apply-job status.
- `request.Run` redacts every status. Additive merge, overlays, secret declarations and the `terraform fmt` gate are shared with the legacy path.
- DaVinci export through pinned pingcli-terraformer 0.6.0; core PingOne export through `internal/coreexport` (GET-only client). `internal/catalog` classifies all 101 provider resources (54 covered, 32 with limits, 15 not possible); see [coverage.md](coverage.md).
- Deploy files (`deploy/`) are drift-tested by `deploy/deploy_test.go`.

## Only fixture-tested

All core types are `Live: no`. The PingOne API paths and field names, worker role needs, the GitLab API assumptions, SELinux, systemd and Kubernetes behavior, and enterprise pipeline compatibility have not been verified on a real tenant, GitLab or host. Cases: [core-export.md](acceptance/core-export.md), [hosted-server.md](acceptance/hosted-server.md).

## Open items

- Done: built-in objects are adopted with per-environment import IDs entered on the preview page (`request.importIds`, `missing_import` blockers).
- Done: a non-loopback `listen` requires `exposure` (`tls` or `behind-proxy` with `allowedHosts`); see docs/security/threat-model.md.
- No real sign-in (OIDC reserved, unimplemented).
- GitLab TOCTOU remains between the default-branch check and the commit, and between the head check and branch deletion (no conditional API). The legacy CI `GitLabCommitter` still reports a 5xx/timeout on commit as a generic preflight failure.
- Redaction is heuristic (secretscan), defense in depth only.
- Race tests run in CI on Linux (`go test -race` for the server, request engine and exporters); the Windows dev box cannot run them.
- Terraform binary digest is not printed by `fetch-tools.sh`; operators compute it.
- Egress hostnames per region in operations.md are marked to verify.
- Upstream: pingcli-terraformer is DaVinci-only; Ping CLI 1.x has no export (issue #175).

## Security notes to keep

- Exporters get only the read-only source worker; no write credential, no rollout credential, no native promotion APIs.
- Secrets only from the owner-only file; never in config, pages, logs, Git or tfvars.
- Run one instance; do not expose without an authenticating, TLS-terminating proxy.
- Never run Terraform plan or apply in the server; roll forward, never revert ([recovery.md](recovery.md)).

## How to verify

With Go 1.27.3 and Terraform 1.16.7 on PATH (`scripts/fetch-tools.sh --with-go`): `gofmt -l .`, `go build ./...`, `go vet ./...`, `go test ./...`, `terraform fmt -check -recursive terraform/`, plus `python3 -m unittest discover -s scripts -p test_release.py`. Fixture success is not live proof.
