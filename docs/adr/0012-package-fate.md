# ADR 0012: Fate of existing packages

- Status: Accepted (executed in #44, a separate PR)
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
