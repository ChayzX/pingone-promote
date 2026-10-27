# GitLab-controlled production promotion

## Intent and authorization
The user requires production deployment to be restricted to their enterprise GitLab and own runners. Continue authoring flows in PingOne's UI. Use native Configuration Management APIs, not a Terraform migration. User has delegated engineering decisions and asked for lower-cost parallel agents. Implement this phase without further preference gates.

## Policy and boundaries
- Local browser submission is the default execution route for every target. Local Deploy and Rollback requests are rejected server-side before any native execution/rollback request. Optional non-production local targets can be explicitly allowlisted at process startup; this allowlist is never editable through profile JSON or the browser. No target is implicitly non-production because of its friendly name.
- Actual organization enforcement requires production write credentials to exist only in protected GitLab variables and appropriate PingOne role assignments. Local flags/CI variables alone cannot prevent someone using independently held administrator credentials outside this tool. The app must not claim otherwise.
- A private, separately managed GitLab project stores promotion requests and selected redacted configuration; the public GitHub repository stores only tool code and example pipeline templates. Local app verifies project visibility before submission. GitLab server URL, project and token reference are configured locally; no company URL or token is hardcoded/published.
- Token may be provided for the current session or through a named environment variable. Do not persist token values, worker secrets, resolved native payloads or Terraform state.

## Request and pipeline contract
A versioned request pins source environment/region, immutable native source snapshot, selected original resource IDs, desired target mappings and fingerprints/redacted selected configuration exports. Target identity is explicit. No secret values or OAuth client configuration are included. Request IDs, paths and schema are validated; unknown fields/versions fail. Resource lists use canonical original IDs and URLs, never snapshot record IDs.

The local app submits a new branch, commit and merge request in the configured private project. It does not overwrite that project's pipeline, environment protections or settings. The operator installs the included pipeline template once and configures their runner tags, trusted target identity, protected credentials, approval rules and default-branch protections. Missing setup must produce a useful error, not an unapproved local fallback.

MR pipelines perform offline request validation. After merge into the protected default branch, a plan job uses runner credentials to read the pinned source snapshot and current target inventory, verify selected configuration fingerprints, create a native draft with explicit exclusions/mappings, and collect the full plan. The plan artifact contains redacted preview, native ID, pinned identities and review digest; raw sensitive native payloads stay in memory. Artifacts bind to GitLab project, pipeline and commit. Source changes are not silently re-exported as a newer version.

A protected production deployment job requires approval of the final plan and executes only after fresh-plan verification. The headless runner validates the GitLab job's identity with GitLab's API, confirms a protected ref/default branch and a deployment to the expected protected environment with required approvals, then checks trusted CI target ID/region against the request. Do not treat `CI=true` alone as authorization. Approval enforcement is fail-closed if the API cannot verify it. Never auto-retry native execute or rollback mutations after an uncertain result; use status reconciliation. Jobs targeting one environment use a GitLab resource group.

Rollback is also GitLab-only: generate a separate native rollback plan for the recorded deployment, review its complete scope including deletes/indirect resources, and execute through the same protected/approved gate. A Git revert is not presented as a native rollback. Native latest-promotion and intervening-edit limitations remain.

## Architecture
- `internal/gitlab`: TLS-verified REST client; private project/protected default-branch verification; branch/commit/MR submission, bounded JSON and origin checks, token-safe errors.
- `internal/gitops`: versioned request validation/building and source fingerprints; reusable native plan/review/execute/rollback operations with pinned inventory helpers; serializable metadata-only plan artifacts.
- `internal/cigate`: verify live GitLab job/deployment/approval state and trusted target policy. This is not user-editable manifest policy.
- `cmd/pingone-runner`: noninteractive validate, plan, apply, status, rollback-plan commands; approval checks immediately before every target-changing action, explicit approval digest, no interactive web dependency.
- Local app: process startup local-target policy, GitLab configuration/submission endpoints, preview/export/submission controls, links to merge request/pipeline, disabled direct production controls.
- `examples/gitlab`: private-project pipeline template plus setup instructions; no automatic corporate GitLab access in this session.

## Verification
Meaningful httptest contracts for private-only submission, hostile redirects/origins, token omission, empty/unknown selections, fingerprint/snapshot changes, API-paginated scopes, fresh digest/project/commit/pipeline binding, spoofed CI environment, unapproved/unprotected deployment rejection, and native rollback review. UI verifies GitLab submission and absent local production execute with fixture-only API calls. Existing non-production local tests explicitly opt into the startup allowlist. Run Go suite/race/vet, JS checks, browser regressions, Mac/Linux cross-builds, independent final review; publish verified source to existing GitHub repository. No live enterprise GitLab, own-runner approval policy or PingOne deployment is available here, so those require acceptance testing by the operator.
