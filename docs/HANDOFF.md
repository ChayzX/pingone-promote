# Production acceptance handoff

This work integrates the GitLab-controlled workflow and subsequent main-branch fixes. Disposable acceptance used a private GitLab project and GitLab-hosted Linux runners at the operator's request; this does not verify enterprise or self-managed runner policy. Keep credentials, tenant configuration and raw evidence outside this public repository. An existing public main-branch commit message contains sandbox-identifying details and should be reviewed separately.

### Current status — 2026-10-31

The latest live checks verified snapshot inventory loading, request validation, and a completed one-variable promotion. A subsequent selected sandbox flow promotion created a destination flow and version before PingOne returned HTTP 500. Reconcile the native tenant state before any retry; complete resource effects and recovery remain unverified. The service returned diagnostics in `promotionErrors`, which the tool previously ignored. The app and runner now recognize that field alongside legacy `errors`, expose sanitized summaries, and block error-bearing plans while retaining execution markers. This diagnostic correction does not resolve the service failure. Full live acceptance and rollback remain unverified. macOS Keychain and Linux Secret Service runtime behavior are also unverified. Production readiness is not established.

## Earlier acceptance status — 2026-10-30

Production readiness is **not established**. A subsequently provided sandbox pair supports native inventory snapshots and draft creation. Live browser testing verified complete inventory loading, private GitLab request submission and merge request validation, and rejection of zero-request and two-request changes before planning. GitLab compatibility fixes cover commits to an already-created request branch and job identity responses that report the pipeline source under `pipeline.source`.

One sandbox promotion reached PingOne and was independently reported COMPLETED by a read-only GitLab status job. The original apply job failed after sending execution because the status check compared resolved mappings to draft mappings. A regression-tested runner fix permits resolved mappings only for recognized post-execution statuses; pre-execution checks remain strict. This is evidence of one completed sandbox promotion, not proof of all resource effects or production readiness. A separately initiated rollback plan returned HTTP 400 without an actionable service detail. No rollback was applied; reconcile the native state before another attempt.

Before/after inventory hashes and detailed test evidence are retained privately. Some unrelated form hashes changed across captures; their cause is not established. Do not attribute every observed change to the promotion or claim that every excluded resource remained unchanged.

Disposable acceptance uses GitLab-hosted runners at the operator's request. For the completed sandbox run, the user approved a temporary project-only exception allowing the pipeline triggerer to approve its deployment; the exception was restored afterward. This is not independent human approval. Rollback requires a separately reviewed and approved web pipeline; do not weaken production approval checks to accommodate a single available account.

### Earlier trial capability result — 2026-10-27

At that time, live authenticated probes could read ordinary environment, flow and application APIs, but native snapshots returned `403 ACCESS_FAILED` with detail code `LICENSE_EXCEEDED`. Both disposable trial sandboxes were already opted in to Configuration Management while their effective capability remained false. Later testing used a different sandbox pair; the trial capability issue remains unresolved.

[Ping's current Configuration Management documentation](https://docs.pingidentity.com/pingone/early-access-features/ea-p1_promote.html) describes early access for sandbox environments only. Production use also requires vendor-confirmed support, independently of passing sandbox tests.

Review fixes now compare current destination resources immediately before native execution, including reused unselected dependencies, and reject rollback reviews missing native target-snapshot linkage. Rollback planning verifies that the original artifact came from the matching successful `plan_promotion` job in the trusted GitLab project. Merge request validation has a separate non-secret SHA pin and runner tag. API-fixture regressions support these checks. Actual native response compatibility still requires live evidence.

GitLab's actual CI linter accepted the revised example template. Keep local automated checks, hosted Linux fixture checks, and live service acceptance distinct in the evidence log. Hosted fixture success does not complete any deployment or rollback criterion.

## Evidence available in this workspace

API-shaped Go fixtures and local browser fixtures can verify application behavior and request/plan handling. They do not verify live PingOne API semantics, GitLab protected environment approval behavior, runner registration, organization policies or a production deployment. The fixture-based PingOne verification recorded in [verification.md](verification.md) covers explicit selection/exclusion, digest checks, duplicate-execution blocking and separate rollback review/execution.

The GitLab approval verifier and `pingone-runner` commands are implemented on this branch. Earlier private disposable GitLab tests verified deployment blocking before approval, retry rejection, merge request credential isolation, and zero/one/two request selection without native execution. Later sandbox evidence is described above. A complete independent production-readiness review and successful live rollback remain outstanding.

## Local verification commands

Run these from the repository root after integration:

```sh
go test ./...
go test -race ./...
go vet ./...
node --check internal/app/web/app.js
```

For the browser fixture checks, start this in one terminal:

```sh
PINGONE_BROWSER_FIXTURE_FILE=/tmp/pingone-browser.json go test ./internal/app -run TestBrowserFixture -v -timeout=20m
```

Then run these in another terminal with the installed Chromium path:

```sh
python3 scripts/test_browser.py --fixture-file /tmp/pingone-browser.json --chromium /path/to/chromium
python3 scripts/test_browser_races.py --fixture-file /tmp/pingone-browser.json --chromium /path/to/chromium
python3 scripts/test_browser_profiles.py --fixture-file /tmp/pingone-browser.json --chromium /path/to/chromium
python3 scripts/test_browser_gitlab.py --fixture-file /tmp/pingone-browser.json --chromium /path/to/chromium
python3 -m unittest discover -s examples/gitlab -p "test_*.py"
```

These commands use loopback-only API fixtures. They do not use company credentials or contact PingOne/GitLab. Cross-compilation checks, if repeated, should use `CGO_ENABLED=0` for `darwin/arm64`, `darwin/amd64` and `linux/amd64`; successful compilation does not establish runtime behavior on macOS or RHEL.

## Live acceptance required before production

Use a non-production PingOne environment first and the actual private GitLab project and runners. Record the exact GitLab version/tier and approval API responses. Confirm that protected branch and production environment rules are active, that deployment approvals are distinct from merge request approvals, and that the job verifier rejects missing or unreadable approval data.

1. Install the template and helper in the private project. Pin `PROMOTION_TOOL_REF` to the reviewed full SHA. Confirm the `blue` runner works, then test `green` by setting the single tag variable to `green`.
2. Verify merge request validation has no production variables or target access. Verify environment-scoped credentials and restrictions on pipeline-variable overrides; ordinary requestors must not be able to substitute trusted policy, tool ref, image or environment. Confirm a request change adding zero or two request files fails, an older request is never selected, and path traversal and symlink selections fail.
3. Configure a non-production target and run plan/apply with the protected ref and project settings. Confirm the plan digest, GitLab project/pipeline/commit binding, target ID and region checks, and pre-execution revalidation match the actual GitLab APIs.
4. Exercise negative cases: unprotected branch, wrong project or GitLab URL, wrong target ID/region, wrong or retried job, absent deployment approval, unavailable approval endpoint, stale plan and native timeout/uncertain result. Each must block or require status reconciliation without replaying a native mutation.
5. Verify exact snapshot resource selection, explicit exclusions and reuse of unselected dependencies against the live tenant. Use a source flow whose latest saved version differs from its deployed version and confirm the native deployed-version rule. Inspect the complete native plan and confirm no unselected configuration is updated.
6. From a successful test deployment, start the separate rollback web pipeline, provide the original plan job ID, inspect the rollback draft and run its separately approved apply job. Verify the native latest-promotion and intervening-manual-edit behavior in the tenant.
7. Only after the above pass, establish production roles and protected credentials in GitLab. Measure target state before and after one approved production deployment and rollback using the tenant's native version IDs and configuration snapshots. Record the IDs, selected resources, exclusions, approvals, job identities and outcomes. Do not iterate against production to debug failures.

Use a local agent configured for the enterprise GitLab and test PingOne environment for this acceptance run. That is the better place to verify live API responses and runner policy. This handoff prepares the template and exact acceptance criteria so the local agent can validate them without guessing; it does not claim that acceptance has already happened.

## First review items for the local agent

- Verify the current-target drift gate against the live service with an unchanged native promotion plan, including secret-only changes, reused dependencies, deletes and create collisions. The engine now compares affected live target state immediately before execute; it cannot provide an atomic lock against a later console edit. Separate GitLab resource-group jobs do not lock an entire plan/approval/apply interval. Avoid concurrent target editing and establish native stale-plan behavior before production acceptance.
- Verify exact native `resourceMapping` behavior: the engine deliberately rejects extra/changed mappings rather than guessing whether a native automatic mapping is safe. It may need a narrowly tested compatibility adapter.
- Verify native rollback source snapshot/version/linkage fields against actual responses; the current checks are conservative and may reject a legitimate undocumented shape.
- Verify GitLab's actual `GET /job`, protected-environment and deployment `approval_summary.rules` payloads for the installed version. Unsupported or absent approval data must block; do not replace these checks with `CI=true`, a manual job alone, or a permissive callback.
- Verify least-privilege local inventory/snapshot access. If those APIs require production administrator credentials, move production discovery/planning into the runner rather than storing production write credentials locally.
- Re-run GitLab CI lint against the actual private project. YAML parsing here is not GitLab semantic validation. Keep the public repository free of tenant requests and credentials.

## Suggested local-agent instruction

Checkout `main` from `https://github.com/ChayzX/pingone-promote`. `feat/davinci-request-flow` is an alternate maintained design; use it only when that workflow is the subject of review. Read this file, `docs/gitlab.md`, and the GitLab production design/plan under `docs/superpowers/`. Continue using the user's signed-in **Microsoft Edge** sessions through the available local browser-control tool. Inspect the actual GitLab/PingOne setup, identify disposable non-production environments and a private test project, finish the review items above, configure the pipeline for runner tag `blue` (or `green` through the single tag variable), and execute the live acceptance checklist. Keep production deployment and rollback GitLab-only. Capture before/after template and dependency hashes, actual flow/version IDs, approvals, pipeline IDs, failure cases and rollback results. Use secure local/CI credential inputs; do not commit credentials, scrape unrelated browser sessions, or substitute fixture results for live evidence. If the available environments cannot be confirmed disposable, obtain their exact scope before changing them. Publish fixes with tests; do not label this production-ready until the live evidence supports it.
