# Verification and integration status

The local application has been verified with documented API-shaped fixtures. It has **not** been tested against a live PingOne tenant or executed on macOS/RHEL in this workspace.

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
