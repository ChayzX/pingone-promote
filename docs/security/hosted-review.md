# Hosted server security review

Status: reviewed implementation under fixture verification; external DAST unavailable. This document does not claim a production security certification or completed live acceptance.

## Trust model

Phase 1 intentionally has no sign-in. Everyone who can reach this service can read configuration and create configuration proposals. Names are unverified attribution. Loopback is the default; deliberate exposure requires a controlled access boundary and HTTPS. A session owns drafts through an unpredictable HttpOnly/SameSite cookie, not its typed name. Future authenticated identity has a separate interface.

The only durable writes are Terraform HCL and customer secret declarations on GitLab request branches. The server holds only a read-only source worker and a GitLab token. GitLab scopes alone cannot enforce an arbitrary branch prefix: protect the default branch and configure project policy or a constrained API gateway. No request branch protection, tool CI job, tool CI variable, pipeline trigger, or rollout credential is required. Provisioning a broad administrator worker to work around DaVinci permissions is not an accepted workaround; permission failures must block export and be investigated through live acceptance.

## Controls and independent findings

| Area | Implemented control / corrected finding | Regression evidence |
|---|---|---|
| Browser boundary | Exact Host validation, explicit proxy HTTPS origin, no forwarded-header trust, CSP, no-store, CSRF and Origin on every POST | `internal/server/server_test.go` HTTP boundary, proxy, POST, sensitive rendering cases |
| Draft ownership / concurrency | Bounded sessions/drafts/jobs, expiry, per-session ownership, stale revision rejection, cancellation and shutdown | Hosted HTTP and race tests; stale-form regression |
| Preview / commit | Immutable Git snapshot and frozen formatted publication; default-branch movement conflicts; uncertain outcomes preserve branch for reconciliation | Hosted workflow MR/branch, head-move and retry tests; `hosted_test.go` in GitLab package |
| Existing Git | Hosted existing differences kept until explicit update; additive resources, moved blocks; warning about downstream destruction and roll-forward recovery | Hosted selected-update regression, merge goldens and preview text |
| Secrets | Owner-only 0400/0600 file, input-size limits, no reflected secret values, schema/state sensitivity, typed required Vault variables, blocked unsafe diffs/metadata | Config, state-renderer, request secret-output and HTML tests |
| Sensitive references | Graph inputs carry environment-local metadata; schema-sensitive variables cannot retain source values or be downgraded by explicit lift rules | Core lift tests and `core_review_regression_test.go` |
| External tools | Pinned private executable copies, exact provider archive mirror, sealed child environment, bounded diagnostics, deadline/process-group cleanup | Provider reader protocol, pin, overflow and descendant tests |
| Temporary paths | Private directories, checked regular files, no symlink permission following; cleanup on success and failure | Reader private-tree symlink regression (failed before fix) |
| Inventory | Config-only fixed GET recipes, no users/sessions/audit/member enumeration, refused redirects, collection-bound pagination, response budgets | Inventory fixtures across all 83 recipes and hostile response tests |
| Dependency graph | Typed ID references, variable-valued DaVinci fields, cross-service cycles, unknown/excluded references become per-environment inputs; ambiguous IDs fail closed | Composite/graph tests and independently authored regression cases |
| Completeness | Malformed BOM/collections fail closed, enabled-service failures remain errors, scoped role labels include semantic scope | Independent malformed-response and role-scope regressions |
| Deployment | Non-root runtime, read-only mounts/root filesystem, owner-only staged secret, explicit pod tool ownership, hardened systemd | Artifact review and deployment instructions; runtime acceptance still required |

The server never runs a Terraform plan or apply. Read/import CLI behavior was exercised with the actual verified Terraform 1.16.7 and provider 1.24.0 against a local TLS fake API, with dummy UUID credentials. The test observed no management writes. This proves that fixture's population import/read/render path, not every provider resource's live behavior.

## Open verification limits

HawkScan v6.6.0 was installed and credentials were configured. On 2026-11-11 its app-list API returned `402 Payment Required`; no DAST scan started. This is an external verification gap, not an accepted vulnerability. The installed skill reported version 2.7.4 vs 2.7.5 and recommended `codex plugin marketplace upgrade`.

There is no PingOne sandbox or GitLab instance. Real source role permissions, every service entitlement/API shape, enterprise pipeline controls, actual pod/macOS/RHEL runtime and rollout behavior remain subject to [live acceptance](../acceptance/hosted-live-cases.md). The [coverage matrix](../coverage.md) records resource-specific limits. No unresolved known finding is silently accepted by this report.
