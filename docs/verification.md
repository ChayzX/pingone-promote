# Verification and integration status

The local application has been verified with documented API-shaped fixtures. Later live sandbox testing verified native inventory loading and draft creation, but the native plan review remains blocked. No live native execution or rollback is verified. The application has not been executed on macOS/RHEL in this workspace.

## Live sandbox follow-up — 2026-10-29

Visible browser testing verified private GitLab submission, successful one-request merge request validation, and zero-request and two-request rejection before native planning. Fixes address native snapshot identities and supported read expansions, GitLab's existing-branch commit behavior, shell checkout line endings, and the nested pipeline-source field in GitLab job responses. Tenant identities, credentials, request files, pipeline identifiers and raw evidence remain outside this public repository.

Native drafts expose an ordered four-operation flow/version lifecycle. The strict compatibility adapter and destination payload-reference drift checks have passing regression tests, the full Linux Go suite, race detector, vet and the GitLab Playwright fixture suite. A subsequent live review still rejects the sequence. Reconstructed redacted-artifact replay passes but lacks some original native inputs; live compatibility is therefore unresolved. Apply has not been run. See [HANDOFF.md](HANDOFF.md) for the remaining acceptance criteria and observed pre-deployment inventory differences.

## Acceptance follow-up — 2026-10-27

The revised template passed the private project's GitLab CI lint without errors or warnings. An isolated GitLab-hosted Linux job passed the Go suite, race detector, vet, JavaScript syntax check, ten selector regressions and Linux/macOS cross-builds for its captured source manifest. Later source changes require another manifest-bound run before completion. Windows full-suite failures concern Unix file-lock and permission semantics; they are not a successful Windows compatibility test.

Additional regressions cover changed live destination configuration and reused dependencies, create collisions, unavailable/ambiguous inventory, exact native mapping changes, original rollback job provenance, and missing or changed native rollback linkage. Red/green outputs and live job traces are retained privately with the source state they tested.

Native snapshot probes returned `403 ACCESS_FAILED` with detail code `LICENSE_EXCEEDED`, despite Configuration Management already being opted in. An independent HTTP reproduction confirmed the denial. Successful probe-job status means diagnostics completed; it does not mean deployment succeeded. Live GitLab approval blocking, separate approval, retry rejection and merge request isolation were tested independently without native writes. Native resource/version before-and-after evidence and separately approved native rollback remain outstanding. See [HANDOFF.md](HANDOFF.md) for the acceptance gate and vendor sandbox-only support limitation.

## Offline QE follow-up

Additional regression tests cover non-object JSON request bodies, missing GitLab job metadata, encoded GitLab path traversal, incomplete merge request responses, mismatched native resource and step identities, credential reflection in API error codes, and browser review invalidation while a request is pending. These are fixture tests; they do not establish live service compatibility. Private evidence retains the failing reproductions and subsequent verification results, bound to the tested source manifest.

## Automated checks

- `go test ./...`: client, inventory, planner, profile store, authenticated local HTTP workflow, launcher, and restart reconciliation.
- `go test -race ./...`: concurrent profile/history writes and HTTP workflow with Go's race detector.
- `go vet ./...` and `node --check internal/app/web/app.js`.
- Cross-compilation with CGO disabled: macOS arm64, macOS amd64, Linux amd64.

Regression coverage includes empty selection (never request whole-environment promotion), explicit exclusions, optional template selection, same-tenant/type-compatible mapping, mapped flow boundaries, invalid/missing/array mutation payloads, unsafe URLs, incomplete pagination and forbidden resources on a later page. HTTP fixtures verify native execution media types, digest rechecking, no duplicate execute, metadata recovery after app restart, and creation/review/execution of a separate rollback promotion.

## Browser checks

Chromium with Python Playwright exercised the actual local app against a Go `httptest` PingOne fixture. At 1440, 900, and 390 CSS pixels, the app rendered without page-level horizontal overflow. Desktop history uses a table and mobile history uses cards.

The browser checks exercised profile save/delete and secret omission, friendly-name search, individual resource selection, destination mapping, an unselected template on a later plan page blocking deployment, invalidation after changes, confirmation before execution, completed history, separate rollback review and execution, and absence of JavaScript page errors in those flows. Additional held-response tests verified that old inventory/plan responses cannot overwrite new choices, and selecting automatic mapping does not silently reapply the UI suggestion.

Fixture state and screenshots are development artifacts, not live deployment evidence. Native Keychain access was inspected against Apple's Security tool source and cross-compiled; it was not executed on macOS. No universal accessibility, Safari, load/performance, or live service compatibility claim is made.

## Live acceptance required

Use a non-production environment pair first:

1. Confirm environment-root snapshot creation and complete resource discovery work with your tenant's early-access API.
2. Select one deployed DaVinci flow and map it to a differently named existing destination flow. Leave changed, unrelated notification templates unselected. Confirm the full plan contains no template writes.
3. Deploy and confirm the intended existing flow receives a new version/deployment, and unrelated destination template content is unchanged.
4. Map an unselected dependency to an existing destination dependency and verify it is reused without updating it. If native exclusion semantics reject that plan, the application must stay blocked.
5. Select a changed dependency or one notification template explicitly in a separate plan and confirm only approved resources are updated.
6. Create, review and execute rollback of the latest promotion. Confirm restoration and that unrelated templates remain unchanged. Later manual edits can be overwritten by native rollback.
7. Exercise the desired credential mode on macOS; use environment-variable credentials for the initial RHEL run.

The native API does not document atomic conditional execution of a reviewed plan and acknowledges indirect resource effects. Full-plan inspection is a guard, not proof of zero unlisted service side effects. Avoid concurrent console/API edits during planning and execution. Forward deletion promotion is intentionally blocked. Rollback deletion operations require separate review.

Before/after comparisons use the loaded destination inventory snapshot; after restarting, only metadata is retained locally, so previous configurations must be reloaded for a complete before-value view. Complete native operation payloads remain visible, redacted, before execution. Secrets and certificates retain PingOne's native promotion-variable requirements; resolve native validation errors in PingOne and rebuild the draft.
