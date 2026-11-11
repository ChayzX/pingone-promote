# ADR 0017: DaVinci front end, GitLab CI job backend

- Status: Accepted
- Date: 2026-11-03
- Issues: #58

## Context

The DaVinci-hosted front end comes first. DaVinci cannot run
pingcli-terraformer or terraform, and must not hold PingOne worker
credentials for the source, Vault secrets or a write token beyond what it
needs to file a request. The engine (ADR 0015) therefore runs as a GitLab CI
job in the config repository, triggered by DaVinci. The config repository's
`.gitlab-ci.yml` only includes an enterprise Terraform template; its deploy
jobs are manual.

## Decision

### Transport: a request file on a request branch

DaVinci creates `pingone-promote/<requestId>` from the default branch in ONE
Commits API call (`start_branch`) that adds `.pingone-promote/request.json`
(a later Preview or Submit updates the file with `last_commit_id`). The push
pipeline runs the job.

A file on a branch was chosen over a base64 pipeline variable because it is
reviewable and versioned, survives job retries, needs no trigger token, has
no variable size limit, and the branch is where the result lands anyway.
`--request-env` (base64) remains for manual runs.

### The job (`pingone-promote request`)

- Stage `.pre`, in the PARENT pipeline, rules:
  `$CI_PIPELINE_SOURCE == "push" && $CI_COMMIT_BRANCH =~ /^pingone-promote\//`
  and `exists: [.pingone-promote/request.json]`; `resource_group:
  pingone-promote-requests`; no `needs:`.
- Validates the request strictly (`requestVersion: 1`, unknown fields fail,
  size limit 256 KiB, secret-looking values refused), requires the branch to
  be exactly `pingone-promote/<requestId>`, and refuses the default branch.
  `scope` is `selected` (default; one to 20 of each kind) or `all` (the
  whole source environment; the lists may be empty and uncapped, and the
  request must carry `confirmAll: true` or validation fails). `exclude`
  entries (flow, application, flowPolicy, connection, variable, or a full
  address) apply in both scopes; one that is also included, or a dependency
  choice `exclude` on the selected item, is rejected with both sides named.
- Exports the source with the pinned terraformer (the selection plus
  upstream, or everything for `all`, minus the exclusions), neutralizes,
  merges additively into the checked-out workspace, runs `terraform fmt`.
  The semantics of whole-environment export and of excluded resources
  (references become per-environment ID variables unless Git already
  manages them) are in ADR 0015.
- `dryRun`: writes `result.json` (changes, blockers, required secrets, diff)
  and commits NOTHING.
- Otherwise: ONE commit to the SAME branch through the Commits API, with
  every generated file and the deletion of `.pingone-promote/request.json`,
  so the next pipeline on the branch skips the job (no loop). The commit
  carries `Requested-by:`, `Request-id:` and `PingOne-Promote-Request:`
  trailers; the MR description repeats the first two. `mode: mr` opens an MR
  to the default branch labelled `pingone-promote` (configurable); `mode:
  branch` leaves the branch. A whole-environment MR title is prefixed
  `[whole environment]` unless the title already contains it.
- The job never plans or applies.

### Concurrency, idempotency and failure

- The branch head must equal `CI_COMMIT_SHA`; a concurrent push is a
  `conflict`. Each file update carries `last_commit_id`.
- A head that already carries `PingOne-Promote-Request: <id>` (a retried
  job), or an earlier commit with that trailer in the branch history (a
  repeated submit), reports `already_submitted` with the existing commit and
  MR; nothing new is written and no second MR is opened.
- On any typed error except `partial`, nothing is committed and the request
  file stays. `result.json` is always uploaded (`artifacts: when: always`)
  and the last log line is `PINGONE_PROMOTE_RESULT {...}`.

### Trust boundary

- **Who can trigger:** anyone who can push a `pingone-promote/*` branch.
  Recommend protecting `pingone-promote/*` as a wildcard with Developer push
  and merge allowed, so protected CI variables are available on those
  branches and only project members can create them.
- **DaVinci's token:** a project access token on the config repository,
  Developer role, `api` scope, used only to create the request branch, write
  `request.json` and read pipeline, job artifact and MR status. It never
  sees PingOne or Vault credentials.
- **The job's token** `PINGONE_PROMOTE_GITLAB_TOKEN`: a separate project
  access token, Developer role, `api` scope, masked and protected
  (`CI_JOB_TOKEN` cannot push). The job commits only to its own request
  branch and only allow-listed paths (`definitions/*.tf`, the named root
  files, `<env>.tfvars`, `customer_secrets.yml`, and the deletion of the
  request file); never CI files, dot paths or `*.auto.tfvars`; never the
  default branch. Protected `pingone-promote/*` branches are accepted
  because that is the recommended setup; the prefix rule keeps every other
  protected branch out.
- **PingOne credentials:** a READ-ONLY worker for the source environment,
  masked and protected CI variables, passed to terraformer through an
  allowlisted child environment only. Downstream environments' credentials
  are not used by this job.
- **Request validation:** strict schema; the source must be in
  `PINGONE_PROMOTE_SOURCES` and not in `PINGONE_PROMOTE_ROLLOUT`;
  environment values may only name the source or rollout environments and
  are never secrets.
- **Merge requests:** the default branch stays protected; a person merges.
  Enterprise deploy jobs remain manual and gated.

### Size limits for a whole environment

`result.json` always lists every change and adds `summary.byType` (per-type
counts), so the app can lead with the summary. The diff is cut at a line
boundary at 1 MiB, or 512 KiB for `scope: all`, which keeps `result.json` a
few MiB at the most for a large environment; the complete diff is behind
`diffUrl`. The request limit stays 256 KiB (an exclude list is capped at 200
entries). The merge request description of a whole-environment request is
the per-type table plus at most 300 changed rows, far below GitLab's limit.
`resultVersion` stays 1: the new fields are additive.

## Alternatives

- Pipeline trigger API with a base64 variable: workable, but the trigger
  token is a second secret in DaVinci, the request is not versioned, and
  variables have size limits. Kept as `--request-env` for manual use.
- A separate request-runner project, or a child pipeline: more moving
  parts; the user chose one repository and the parent pipeline.

## Consequences

- The request and result schemas are a published contract
  (`docs/gitlab-ci-integration.md`); changes bump `requestVersion` or
  `resultVersion`.
- A request branch exists per request; MRs delete it on merge.

## Addendum (2026-11-11): configurable branch prefix

`pingone-promote/` is the default request branch prefix. A project whose push
rules require another one sets it in `PINGONE_PROMOTE_BRANCH_PREFIX`, in the request
job's rule, and in DaVinci's `ppBranchPrefix`, for example `CIAM-`. Wherever this
ADR says `pingone-promote/<requestId>`, read `<prefix><requestId>`. The prefix must
end with `/` or `-`. The app acts only on `<prefix>` plus its own request ID format
(`YYYYMMDD-HHMMSS-xxxxxx`), so other branches that share the prefix are never
listed, read or deleted.
