# Hosted server handoff

This branch is an incomplete implementation. Do not merge it on the strength of this document. The full work request is the hosted, source-only, full PingOne server described in ADR 0018. PR #105 began as a four-file scaffold.

## Binding requirements

Reuse `internal/request`, exporter, neutralizer, additive HCL merger, overlay, formatter, GitLab and pipeline-secret engines. Add `serve --config FILE`, server-rendered pages and in-memory session-owned drafts. Everything executes in the server, then commits HCL and secret declarations directly to a GitLab request branch. No tool CI job/variable/request file/protected request branch. Enterprise CI alone plans and deploys.

Only a source is selected; the configured rollout supplies later environments. Every environment's missing values block submission. All POSTs need CSRF, Host is allow-listed, CSP is strict. No sign-in yet: typed names are always `(unverified)`, behind an identity interface. Secrets only from an owner-readable server file, never config/pages/logs/Git. Exporters get a read-only worker only. Native promotion APIs are forbidden.

Full selection must cover every feasible provider configuration type, with include/exclude lists, optional transitive dependencies and update/keep/exclude choices. Never users, sessions, user data or audit logs. Keep existing DaVinci app and example CI checks passing without extending them.

## Current checkpoint (2026-11-10)

- Existing `feat/server` scaffold: `request.Env.NoRequestFile`, GitLab hosted helpers, server config.
- ADR 0018 records the authorized hosted design and supersedes ADR 0017 for this workflow.
- Security review found starting-slice issues. Status on `feat/server-safety` (fixture tests only; still needs independent review and a Linux/macOS CI run, because the local run was on Windows where POSIX mode/owner tests skip):
  - Fixed: secrets file must be a regular non-symlink file (Lstat + SameFile), mode exactly 0600/0400, owned by the server's user; errors never carry values or unknown names; Windows refuses (unsupported). Tests: `internal/server` `TestSecretsFileModes`, `TestSecretsFileRefusesSymlink`, `TestSecretsFileOwnerIsChecked`, `TestParseSecrets`, `TestSecretsWindowsRefusesUnverifiable`.
  - Fixed: config defaults/refusals and no secret-shaped config field. Tests: `TestConfigDefaults`, `TestConfigRefusals`, `TestConfigRejectTrailingDataAndMalformed`, `TestConfigHasNoSecretField`.
  - Fixed: `gitlab.Client.Snapshot(ctx, branch, dirs)` now creates and returns its own private 0700 temp dir (never deletes a caller's directory), resolves the head once and reads tree and files with `ref=<sha>`, bounds files/bytes, and skips dot/CI files and refuses directories outside the allow-list. Tests: `TestSnapshotReadsOneCommitDespiteConcurrentPush`, `TestSnapshotDirectoryIsPrivate`, `TestSnapshotLimitsAndCleanup`, `TestSnapshotRefusesPathsOutsideAllowList`.
  - Fixed: `HostedCommitter` creates the branch from the previewed SHA, refuses with "preview again" when the default branch moved, restricts branches to `BranchPrefix + requestID`, deletes only a branch it created when the failure is certain (4xx/preflight/conflict) and the head is still the start SHA, and keeps the branch with `PartialError{Stage: "commit_uncertain"}` on timeout/5xx. Tests: `TestHostedCommitSucceedsRootedAtReviewedSHA`, `TestHostedCommitConflictWhenDefaultBranchMoved`, `TestHostedCommitUncertainKeepsBranch`, `TestHostedCommitCertainFailureRemovesOnlyItsOwnBranch`, `TestHostedCommitNeverDeletesPreexistingBranch`, `TestHostedCommitBranchNames`. The server must set `HostedCommitter.BranchPrefix` from config.
  - Fixed: `request.Run` redacts diff, error, warnings, blocker messages, change reasons and queries for every status (private keys, JWTs, literals under secret-suffixed names). Tests: `TestRedactEveryStatus`, `TestRedactLeavesOrdinaryTextAlone`, `TestFailedRunRedactsEchoedInput`. Redaction is heuristic (secretscan); it is defense in depth, not a guarantee.
  - Fixed test: `TestSelectedWithoutUpstreamDoesNotAddDependencies` now supplies the excluded subflow's per-environment ID (the engine was right to block without it).
  - Open: the CI `GitLabCommitter` path still reports a 5xx/timeout on the commit call as a generic preflight-type failure in `request.commit`; a GitLab TOCTOU remains between the default-branch check and the commit, and between the head check and branch deletion (no conditional API).
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
5. Independent code/security/UX review. Fix findings with regression tests. Small PRs to main, merge only green CI. Keep this handoff current on frequent pushes.

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
