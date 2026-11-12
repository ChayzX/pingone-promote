# Hosted server handoff

This branch is an incomplete implementation. Do not merge it on the strength of this document. The full work request is the hosted, source-only, full PingOne server described in ADR 0018. PR #105 began as a four-file scaffold.

## Binding requirements

Reuse `internal/request`, exporter, neutralizer, additive HCL merger, overlay, formatter, GitLab and pipeline-secret engines. Add `serve --config FILE`, server-rendered pages and in-memory session-owned drafts. Everything executes in the server, then commits HCL and secret declarations directly to a GitLab request branch. No tool CI job/variable/request file/protected request branch. Enterprise CI alone plans and deploys.

Only a source is selected; the configured rollout supplies later environments. Every environment's missing values block submission. All POSTs need CSRF, Host is allow-listed, CSP is strict. No sign-in yet: typed names are always `(unverified)`, behind an identity interface. Secrets only from an owner-readable server file, never config/pages/logs/Git. Exporters get a read-only worker only. Native promotion APIs are forbidden.

Full selection must cover every feasible provider configuration type, with include/exclude lists, optional transitive dependencies and update/keep/exclude choices. Never users, sessions, user data or audit logs. Keep existing DaVinci app and example CI checks passing without extending them.

## Current checkpoint (2026-11-10)

- Existing `feat/server` scaffold: `request.Env.NoRequestFile`, GitLab hosted helpers, server config.
- ADR 0018 records the authorized hosted design and supersedes ADR 0017 for this workflow.
- Security review found starting-slice issues: group-readable secrets accepted; snapshot recursively deletes supplied directory; mutable-ref snapshot reads; hosted cleanup may delete uncertain/concurrent writes; raw blocked diffs/errors can expose secrets. Fixes and regression tests are in progress. None are claimed fixed here.
- Go 1.27.3 and Terraform 1.16.7 were downloaded with verified SHA-256. Local first compilation encountered a full root disk; build caches moved to a separate work volume. No passing integrated suite yet.
- Independent work is in progress on hosted GitLab safety, HTTP server/UI, and exporter research; checkpoint commits may contain tests before their implementation.

## Exporter findings that change the implementation

Verified against upstream sources and actual binaries:

- `pingcli-plugin-terraformer` v0.6.0 (`9aa51edd1930b1b2724183f7c56911aad0c47556`) remains DaVinci-only: seven enabled resource definitions and seven hardcoded resource handlers. The environment definition is disabled. There is no runtime custom-definitions flag.
- Ping CLI v1.11.0 removed `platform export`; old documentation is stale (upstream issue #175).
- Ping CLI v0.8.4 (`8b93d3c06e7c2674d83203f100b09111b600e784`) exports import blocks only. Its usual config-generation path requires `terraform plan -generate-config-out`, which this server must not run.
- Adding YAML alone cannot expand the existing binary: new handlers/API dispatch require an upstream change or fork. A second exporter needs an explicit ADR and fixtures. One candidate is read-only import/state/schema extraction in isolated temporary state; evaluate thoroughly before choosing it. Do not claim full coverage from import-ID enumeration.
- Provider stable v1.24.0 is pinned by tag `d5019dec50baddf344540249219023f839874f2b`; its full resource inventory must populate the matrix. Provider #1360 was fixed in v1.22.0; current/published DaVinci versions, injected defaults and unreadable secrets still need explicit fixtures/live cases.

## Next work

1. Finish and independently review safety fixes and server flow. Preserve a frozen source export and immutable Git snapshot between preview and submit. Generic request selectors should coexist with legacy flow/application/policy fields.
2. Complete exporter decision and implementation family by family, with cross DaVinci/core graph tests, one notification template, no-dependencies selection and whole-environment exclusions. Publish an honest full-provider coverage matrix.
3. Add pinned tool installation, Containerfile/pod/systemd/RHEL notes, short operator README, developer details and live acceptance cases. macOS/RHEL/live service behavior cannot be inferred from fixture success.
4. Rewrite issues #82–85 for this hosted design and open concrete new issues. Read all requested issue context (#58/#61/#62/#69/#73/#82–85/#92) and archived HANDOFF; do not carry forward their retired transport assumptions.
5. Independent code/security/UX review. Fix findings with regression tests. The user now requires this competition implementation to stay on `feat/server`, out of `main`; do not touch Claude’s `release/hosted-v1` branch. Keep this handoff current on frequent pushes. Prior instructions to merge are superseded.

## Required verification before push/merge

With Go 1.27.3 and Terraform **1.16.7** on PATH:

```sh
gofmt -w cmd internal
go build ./...
go vet ./...
go test ./...
terraform fmt -check -recursive terraform/
python3 -I davinci-app/check_app.py
node davinci-app/test_app.js
python3 -m unittest discover -s scripts -p test_release.py
```

Also run race tests for draft/background concurrency and meaningful security/HTTP end-to-end tests. Record failures and skips. A WIP checkpoint is not a completion claim. Review every code piece using an agent other than its author before merge. Only a real tenant/GitLab acceptance run can verify service assumptions; document those steps under `docs/acceptance/`.

### First development check

The first full development snapshot (including uncommitted work in progress) ran all required Go commands and Terraform formatting on the authorized dev box with Go1.27.3/Terraform1.16.7. Build/vet/test failed because server templates were being implemented and the GitLab positive snapshot fixture was not mode0700. Terraform formatting passed. Existing exporter, neutralizer, merger, request, formatter, pipeline-secret and CI-example package tests passed in that run. This is a development observation, not green CI or a release gate. The secret-file focused regression subsequently passed; the optional-dependency test still needs its missing-value fixture completed. Rerun everything against the final reviewed commit.

### Hosted implementation checkpoint (2026-11-11)

The worktree now includes server-rendered asynchronous search/preview/fill/submit, frozen publication, MR and branch modes, GitLab Requests/rollout views, identity seam, CSRF/Host/CSP, private pinned executable startup, and deployment/installer files. Independent review corrected proxy HTTPS handling, stale form revisions, merge-versus-squash rollout SHA selection, and sensitive Git metadata rendering. The server suite and race suite passed on the authorized development host; final whole-tree verification remains required.

Core coverage is still being implemented: the corrected provider state renderer has passing focused/exporter checks; safe REST inventory, the isolated provider import/read lifecycle, and cross-core/DaVinci closure are separate modules in progress. The provider inventory is now exact: the actual pinned stable provider schema registers101 types, including SDKv2 resources the initial framework-only research missed. The central inventory excludes5 user-data types. Do not assume the current default server exporter already wires the new core modules. Built-in adoption readiness and per-family neutralizer rules are also unfinished. No full-coverage or production-ready claim is made.

The pinned installer passed offline fixture tests and a real Linux amd64 download/verification run. Container/pod/macOS/RHEL runtime and tenant/GitLab acceptance remain unverified. See `deploy/README.md` and `docs/acceptance/hosted-live-cases.md` for the intended steps.

### Core integration checkpoint (2026-11-11)

Latest published foundation: `b13ebf0` (hosted checkpoint plus separately published CI tooling). The default server factory now wires the shared composite exporter: safe REST inventory, pinned provider private import/read/render, DaVinci runner, and transitive graph. `pingone.services` can explicitly select licensed configuration families; empty uses bill of materials detection and fails closed on malformed responses.

All 101 registered provider resources are accounted for in [coverage](coverage.md). This is an honest fixture/static-source matrix, not proof of live tenant round trips. Three action-only resources have no importer; unreferenced images have no list endpoint. Users and four user-assignment resources are excluded.

Independent core review found concrete gaps in variable-valued DaVinci references, missing/ambiguous cross-type ID references, unknown ID map keys, excluded source-ID literals, malformed collections and BOM products, and scoped-role labels. Reproductions are retained in `core_review_regression_test.go`; fixes are under verification. The reader's symlink permission escape was reproduced, fixed and covered. Provider process-group cleanup handles timeout and early parent exit. Real Terraform/provider loopback fixture imported/read/rendered a population without management writes.

Built-in objects carry adoption metadata through the existing neutralizer and merge. Missing rollout import IDs block readiness and can be supplied in preview; environment-only singleton imports use each environment's ID. Hosted mode now keeps every differing Git resource until Update is explicitly chosen, including selected resources. Legacy CI request behavior is retained for compatibility. Typed sensitive map/object values retain their types at root and module boundaries.

Core environment lifts now cover explicit deployment paths (application/IdP URLs, domains, certificate inputs, gateways, MFA relying-party IDs, notification endpoints and other declared fields). Sensitive literals become required Vault variables with no source values. A complete test run against the current frozen checkpoint is still required. Do not treat earlier checkpoints' passing tests as evidence for these newer edits.

HawkScan preflight is blocked: app-list API returned `402 Payment Required` on 2026-11-11. No DAST scan ran. Independent source/security review and regression tests continue; this external limitation must remain explicit. The installed skill also reports 2.7.4 vs 2.7.5 and recommends `codex plugin marketplace upgrade`.

Remaining: finish independent review and full checks, fix exact-SHA CI including optional live-fixture skip policy, validate packaging/builds and rendered UI, add stronger hosted core end-to-end coverage, update issue/PR evidence. Keep all work on this branch and do not merge to main.

### Verified core checkpoint

The exact index snapshot passed Go build, vet and full tests on MinecraftMachine with verified Terraform 1.16.7, actual pinned terraformer, and the real provider ZIP enabled. The actual-provider TLS fake-API fixture passed (31.67 seconds, three local API/auth requests, no management writes); only the intentionally opt-in fixture-regeneration test skipped. Terraform fmt, locked-installer tests and the retained DaVinci app (11,273 checks) passed. Release-script tests passed seven cases. Gofmt reported no files in the frozen snapshot. Independent reviews approved the corrected core graph, adoption, credential lifts and process cleanup; no known finding remains in those reviewed scopes.

This checkpoint deliberately excludes the separately progressing hosted notification workflow test, notification sender-address rules, runtime serve-command smoke test and UX refinements. Their newer work must receive its own verification. Packaging runtime validation and exact-SHA GitHub CI investigation are still active; GitHub job-log endpoints have repeatedly returned HTTP 502. Do not infer green remote CI from local checks.

### Image, required-input and Podman checkpoint

Work after `68a7c45` adds exact image UUID search and retained include/exclude selectors, regional public-original image recovery with strict URL/content/size controls, typed missing required inputs and descendant sensitivity, exact-schema notification workflow coverage, real serve-command startup smoke, and plain-language preview summaries. Single-item selection/whole-exclusion tests cover all22 existing core family fixtures. Independent reviewers inspected all new code and corrected two test gaps. Focused exporter, server, renderer, neutralizer and CLI checks passed.

The macOS operator path now includes Podman machine setup, native Linux tool installation inside its VM, owner-only mounted secrets, non-root read-only runtime, loopback publishing, Host/CSP checks and cleanup. Native Apple Silicon container builds no longer default to amd64. Container builds bound Go concurrency for smaller development VMs.

Current full-check evidence must be interpreted carefully: frozen checkpoint4 passed build/vet and every Go package except the actual-provider fixture on MinecraftMachine; that fixture failed before API calls at version verification. Its isolated local run passed (61.66 seconds, three fake requests, no writes). The remote host was heavily loaded with full swap; diagnostics and a serialized local full run are in progress. Do not call this checkpoint fully verified until that run finishes. Linux image build/non-root/read-only homepage smoke passed earlier with optional PingCLI omitted, exposing its clean-home version-banner bug; the named-version fix has a passing regression test and awaits the final full-tools container rerun. Podman on macOS and RHEL systemd/SELinux are not runtime-verified. GitHub CI for `68a7c45` remains queued; no green remote checks are claimed.

All implementation stays on `feat/server`; do not merge to `main` or touch `release/hosted-v1`. UI render evidence, final full-tools container checks, latest-SHA CI, and issue/PR evidence remain active follow-up work. HawkScan still has the documented 402 preflight blocker.

### Latest verified source checks

All tracked Go source in the next checkpoint matches the frozen local snapshot that passed Go build, vet and full tests with Terraform1.16.7, the actual pinned terraformer and the real provider ZIP enabled. The actual-provider fixture passed in43.31 seconds with three fake API/auth requests and no management writes. Only the opt-in TestRegenerateRepoFixture skipped. Gofmt, Terraform fmt, four installer tests and the retained DaVinci app's11,273 checks passed. The release-script unit suite is still building its cross-platform binaries on the small local host; do not infer that result yet.

The final Linux container rerun passed with all pinned tools, including PingCLI, under UID10001, read-only root/config/tools,2GiB executable tmpfs,2GiB memory and2CPUs. Exact Containerfile/tools.go hashes are recorded in the bounded review. Mac Podman setup/testing instructions are in deploy/README.md; actual Mac/RHEL execution remains unverified.

Old GitHub run logs were recovered through the run archive endpoint. CI's old skip guard incorrectly treated the test after an allowed regeneration skip as unexpected; Homebrew's source/assets job lacked Terraform. Both are corrected. The new skip parser has eight passing regression tests, including real indented skipped subtests, and accepts the full Go log. Workflows parsed as YAML. Root independently reviewed these fixes. PR105 is now draft to reflect the user's instruction to keep this implementation out of main.

Four fixture HTML pages and assets passed HTTP capture. Remote browser rendering failed under actual inotify/I/O constraints; a bounded local browser alternative is being tried. No visual pass is claimed. HawkScan app-list preflight again returned402; no DAST scan ran. The exact-SHA GitHub checks, release fixture result, browser evidence and updated remote issue/PR descriptions remain follow-up work.
