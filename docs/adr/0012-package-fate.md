# ADR 0012: Fate of existing packages

- Status: Accepted (revised 2026-11-03 for v2)
- Date: 2026-11-03
- Issues: #44, #60

## Decision

- **Delete:** `internal/gitops`, `internal/cigate`, `internal/promotion` (move
  its Redact heuristics to the translator), `cmd/pingone-runner`,
  `examples/gitlab`, the snapshot and promotion parts of `internal/pingone`,
  and `internal/app` `workflow.go` / `policy.go`.
- **Adapt:** the app server, the GitLab client and the store.
- **Keep:** `pingone.Client`, `logging`, `strictjson` and `buildinfo`.
- Archive the old docs to `docs/archive/`.
- Status of the move: the Redact name heuristics are now in
  `internal/translate/secrets.go`, where a match raises an acknowledged
  warning and produces a secret placeholder instead of a silent redaction.
  The original in `internal/promotion` stays until #44 deletes the native
  workflow.

## Alternatives

- Deleting in the foundation PR: rejected. The current app must keep working
  until the Terraform path replaces it.

## Consequences

- During the transition the repository contains both paths. The foundation
  PR only adds code: `internal/tfvars`, `internal/keys`, `terraform/` and the
  `check` subcommand.

## Revision for v2 (2026-11-03)

Plan for the v2 engine (ADR 0014, 0015); deletion happens in migration
step 10, so the v1 code still builds:

- **Delete (step 10):** `internal/davinci`, `internal/translate`,
  `internal/keys`, most of `internal/tfvars` (keep `fmt.go`,
  `secretsfile.go`, `overlay.go`), `internal/promotelegacy` (v1
  `internal/promote`, moved aside), `terraform/modules/pingone-davinci`.
- **Moved:** the secret heuristics are in `internal/secretscan`; `translate`
  and `tfvars` delegate to it.
- **New:** `internal/exporter` (pinned terraformer runner),
  `internal/neutralize`, `internal/hclblocks` (byte-range block edits),
  `internal/promote` (additive merge, overlays, `check --root`),
  `internal/request` (headless CI request job, ADR 0017).
- **Keep:** `internal/pipelinesecrets`, `internal/gitlab` (new v2 path
  allow-list and branch commits), `internal/store`.
- **Rule:** nothing in the v2 path imports `davinci`, `translate`, `keys`
  or `promotelegacy`.
