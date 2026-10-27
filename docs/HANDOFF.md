# Production acceptance handoff

This workspace has no access to the enterprise GitLab instance, private project, own runner, PingOne tenant, test environment IDs or credentials. The GitLab template and instructions are prepared for operator review; they are not evidence of live GitLab or production compatibility. This work is on branch `feat/gitlab-production`. The published `main` at `v0.1.0` does not yet include the production policy and GitLab submission work on this branch.

## Evidence available in this workspace

API-shaped Go fixtures and local browser fixtures can verify application behavior and request/plan handling. They do not verify live PingOne API semantics, GitLab protected environment approval behavior, runner registration, organization policies or a production deployment. The fixture-based PingOne verification recorded in [verification.md](verification.md) covers explicit selection/exclusion, digest checks, duplicate-execution blocking and separate rollback review/execution.

The GitLab approval verifier and `pingone-runner` commands are now implemented on this branch. Go tests, local browser regression suites, and template/selector syntax tests have passed in this workspace. A complete independent production-readiness review is still needed. Live GitLab approval behavior and PingOne tenant behavior remain unverified here.

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

- Reproduce destination drift with an unchanged native promotion plan: the engine currently reconstructs the pinned target snapshot and rechecks native steps, but does not independently fingerprint current live target configuration immediately before apply. Determine whether PingOne invalidates such stale plans; otherwise add a fail-closed current-target comparison for affected resources. Separate GitLab resource-group jobs do not lock an entire plan/approval/apply interval. Do this before any production acceptance.
- Verify exact native `resourceMapping` behavior: the engine deliberately rejects extra/changed mappings rather than guessing whether a native automatic mapping is safe. It may need a narrowly tested compatibility adapter.
- Verify native rollback source snapshot/version/linkage fields against actual responses; the current checks are conservative and may reject a legitimate undocumented shape.
- Verify GitLab's actual `GET /job`, protected-environment and deployment `approval_summary.rules` payloads for the installed version. Unsupported or absent approval data must block; do not replace these checks with `CI=true`, a manual job alone, or a permissive callback.
- Verify least-privilege local inventory/snapshot access. If those APIs require production administrator credentials, move production discovery/planning into the runner rather than storing production write credentials locally.
- Re-run GitLab CI lint against the actual private project. YAML parsing here is not GitLab semantic validation. Keep the public repository free of tenant requests and credentials.

## Suggested local-agent instruction

Checkout `feat/gitlab-production` from `https://github.com/ChayzX/pingone-promote`. Read this file, `docs/gitlab.md`, and the GitLab production design/plan under `docs/superpowers/`. Continue using the user's signed-in **Microsoft Edge** sessions through the available local browser-control tool. Inspect the actual GitLab/PingOne setup, identify disposable non-production environments and a private test project, finish the review items above, configure the pipeline for runner tag `blue` (or `green` through the single tag variable), and execute the live acceptance checklist. Keep production deployment and rollback GitLab-only. Capture before/after template and dependency hashes, actual flow/version IDs, approvals, pipeline IDs, failure cases and rollback results. Use secure local/CI credential inputs; do not commit credentials, scrape unrelated browser sessions, or substitute fixture results for live evidence. If the available environments cannot be confirmed disposable, obtain their exact scope before changing them. Publish fixes with tests; do not label this production-ready until the live evidence supports it.
