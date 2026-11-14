# ADR 0018: Hosted source-only promotion server

- Status: Accepted
- Date: 2026-11-10
- Supersedes: ADR 0017 for the hosted workflow
- Retains: ADRs 0014–0016 (source-only rollout, additive HCL merge, conditional imports)

## Decision

`pingone-promote serve --config FILE` runs one Go HTTP service. It searches and exports a source environment, neutralizes environment-specific values, merges into a GitLab Terraform configuration snapshot, formats HCL, and publishes a request branch and optionally a merge request. It never plans or applies. The enterprise pipeline owns deployment of the merged commit through its configured environments, with separate state backends.

The hosted workflow needs no configuration-repository CI job, CI variables, request JSON file, pipeline trigger, or protected request branch. The legacy DaVinci front end and CI command remain available for compatibility; new work belongs to the hosted service. PingOne native promotion APIs are not used.

Durable request history, HCL, environment overlays and secret declarations live in GitLab. Session-owned drafts and background work live in bounded memory and expire; a process restart loses unfinished drafts. Run one replica. Long operations run outside HTTP request contexts and report progress. Submission uses the reviewed source export and immutable Git snapshot. A default-branch move detected at submission requires another preview. The request commit is always rooted at the reviewed SHA; the GitLab API cannot atomically exclude a later default-branch move. It must not silently rebase or overwrite a different version. Uncertain commit responses preserve the branch for reconciliation.

Only a source is selected. The configured rollout supplies every subsequent environment. Shared definitions, root HCL, root `<env>.tfvars` and `customer_secrets.yml` are committed together. A missing environment value blocks submission. Secrets become named Vault references, never values in Git or tfvars. The exporter must omit per-environment generated client secrets so Terraform reads them in each environment rather than copying them from the source; this requires provider-specific verification.

## Identity and trust boundary

Phase 1 has no sign-in. A typed name is always recorded as `NAME (unverified)`. An identity-provider interface separates future authentication from handlers; OIDC is reserved but unimplemented. A typed name confers no authorization or draft ownership.

Bind to `127.0.0.1:8080` by default. Validate Host against an exact allow-list; do not trust forwarded host headers. Protect every POST with a random session-bound CSRF token and same-origin checks. Use server-rendered escaped HTML, a strict Content-Security-Policy, HttpOnly/SameSite cookies, and private no-store responses. Deliberate network exposure requires a trusted access boundary and TLS; it does not turn the typed name into verified identity.

Worker credentials and the GitLab token come only from an owner-readable sensitive `NAME=value` file (0400 or 0600). Configuration contains paths, metadata and tool checksums. Child exporters receive only the source read-only worker and an explicit environment allow-list. The server has no PingOne write credential and no rollout environment credential. GitLab writes are restricted in code to the configured request namespace and Terraform/secret-declaration path allow-list. GitLab token scopes alone do not generally enforce an arbitrary branch prefix; protect the default branch and apply server-side project policy.

## Additive review and recovery

A resource absent from an export is never deleted. Existing differing dependencies keep their Git version until explicitly updated. Renames use `moved {}` blocks. Conditional imports use static addresses with per-environment IDs. Built-in objects must be adopted; absent required adoption IDs block readiness rather than recreating defaults.

A preview must warn that downstream provider behavior may still propose replacement or destruction: additive file merging cannot prove a future plan is non-destructive. Recommend appropriate `lifecycle { prevent_destroy = true }` guards and inspect enterprise plans. Recover a bad deployment by rolling forward with a reviewed correcting commit. A Git revert can remove Terraform resources and cause destruction.

## Alternatives

Continuing to run the exporter in the configuration repository's CI would expose credentials to branch-controlled CI and require enterprise pipeline changes. Reusing DaVinci as the primary host would retain its per-node run limits. Both are retained only as legacy compatibility paths. A database would duplicate GitLab durability and is unnecessary for disposable drafts.

## Evidence limits

Fixture tests verify local HTTP, exporter, merge and GitLab contracts. They do not establish actual tenant API compatibility, worker permissions, enterprise pipeline policy, or runtime behavior on a macOS/RHEL host. Live cases and deployment checks must state these boundaries explicitly.
