# GitLab CI integration: the DaVinci request flow

This is the contract between the DaVinci front end and the pingone-promote
job in the config repository (ADR 0017). One repository, one parent
pipeline, a `.pre` job triggered by a branch push. Deploys stay in the
enterprise template's manual, gated jobs; pingone-promote never plans or
applies.

## Flow

1. DaVinci creates branch `pingone-promote/<requestId>` from the default
   branch in ONE Commits API call (`start_branch`) that adds
   `.pingone-promote/request.json`. A later Preview or Submit updates that
   file on the same branch (`last_commit_id`).
2. The push pipeline runs `pingone-promote:request` in `.pre`:
   `$CI_PIPELINE_SOURCE == "push"`, `$CI_COMMIT_BRANCH =~ /^pingone-promote\//`,
   `exists: [.pingone-promote/request.json]`, `resource_group:
   pingone-promote-requests`, no `needs:`.
3. The job validates the request, exports the source environment with the
   pinned pingcli-terraformer, neutralizes, merges block by block into the
   checked-out branch, and runs `terraform fmt` on every file it writes.
4. `dryRun: true`: `result.json` (changes, blockers, required secrets, diff)
   is uploaded; nothing is committed and `request.json` stays.
5. `dryRun: false`: ONE commit on the same branch with every generated file
   (`definitions/**`, root `*.tf`, `<env>.tfvars`, `secrets.tf`,
   `customer_secrets.yml`) and the deletion of `.pingone-promote/request.json`.
   The commit carries `Requested-by:`, `Request-id:` and
   `PingOne-Promote-Request:` trailers. `mode: mr` opens an MR to the
   default branch, labelled `pingone-promote`, whose description ends with
   the `Requested-by:` and `Request-id:` lines; `mode: branch` leaves the
   branch. The next pipeline on the branch sees no request file and skips
   the job.
6. `result.json` is always uploaded (`artifacts: when: always`); the last
   job log line is `PINGONE_PROMOTE_RESULT {...}` (compact, no diff).

On any typed error except `partial`, nothing is committed and the request
file stays, so DaVinci can fix the request and push it again.

## Installing

Copy `examples/gitlab-ci/pingone-promote.yml` to
`.gitlab/pingone-promote.yml` and add one include after the existing one:

```yaml
include:
  - project: '<group>/<terraform-pipeline-templates>'
    ref: '<pinned-ref>'
    file: '/<terraform-module-application-template>.yml'
  - local: '/.gitlab/pingone-promote.yml'
```

`.pre` always exists whatever stages the enterprise template defines. Both
jobs run in the parent pipeline.

Both jobs build their tools in the job (so they do not depend on a compile
job that only runs on MRs or the default branch):

- the tool: `git clone` `PINGONE_PROMOTE_REPO_URL`, check out
  `PINGONE_PROMOTE_REF` (a 40-character commit), verify `git rev-parse HEAD`,
  `CGO_ENABLED=0 go build`;
- pingcli-terraformer: `git clone --branch $TERRAFORMER_VERSION`, verify the
  tag's commit (`TERRAFORMER_COMMIT`), build with
  `-ldflags "-X main.version=<v> -X main.commit=<sha>"`.
  `go install github.com/pingidentity/pingcli-plugin-terraformer@v0.6.0`
  also builds (it has no replace directives) but reports version `dev`,
  which the tool's version pin rejects;
- module downloads are verified by `go.sum` and the Go checksum database
  (`GOFLAGS=-mod=readonly`, `GOTOOLCHAIN=local`; leave `GONOSUMDB`,
  `GONOSUMCHECK` and `GOFLAGS=-insecure` unset);
- terraform for the fmt gate, from releases.hashicorp.com, checked with
  `sha256sum -c` against `TERRAFORM_SHA256`.

`PINGONE_PROMOTE_TERRAFORMER_SHA256` must be the SHA-256 of the
terraformer binary the job builds. The build is reproducible for a given Go
toolchain (`-trimpath`), so pin the image by digest
(`golang:1.27@sha256:<digest>`), build once, and record the digest the job
prints on its first, failing run (`terraformer binary checksum does not
match`) or build locally with the same image. Change it only together with
`TERRAFORMER_VERSION` or the image.

Replace the `tags:` list with the project's runner tags
(`PINGONE_PROMOTE_RUNNER_TAG` holds one).

## Variables

| Variable | Where | Protected | Masked | Purpose |
| --- | --- | --- | --- | --- |
| `PINGONE_PROMOTE_WORKER_CLIENT_ID` | project | yes | yes | read-only PingOne worker for the SOURCE environment |
| `PINGONE_PROMOTE_WORKER_CLIENT_SECRET` | project | yes | yes | its secret |
| `PINGONE_PROMOTE_WORKER_ENVIRONMENT_ID` | project | yes | no | environment holding the worker app |
| `PINGONE_PROMOTE_REGION_CODE` | project or file | no | no | `AP`, `AU`, `CA`, `EU`, `NA` or `SG` |
| `PINGONE_PROMOTE_GITLAB_TOKEN` | project | yes | yes | project access token, Developer role, `api` scope; the job's commits and MRs (`CI_JOB_TOKEN` cannot push) |
| `PINGONE_PROMOTE_TERRAFORMER_SHA256` | project | yes | no | pinned digest of the built terraformer |
| `PINGONE_PROMOTE_GO_IMAGE` | file | - | - | `golang:1.27` (pin by digest) |
| `PINGONE_PROMOTE_REPO_URL`, `PINGONE_PROMOTE_REF` | file | - | - | the tool's source and pinned commit |
| `TERRAFORMER_VERSION`, `TERRAFORMER_COMMIT` | file | - | - | `v0.6.0`, `9aa51edd1930b1b2724183f7c56911aad0c47556` |
| `TERRAFORM_VERSION`, `TERRAFORM_SHA256` | file | - | - | terraform for the fmt gate |
| `PINGONE_PROMOTE_RUNNER_TAG` | file | - | - | runner tag |
| `PINGONE_PROMOTE_SOURCES` | file | - | - | allowed source environments, e.g. `development` |
| `PINGONE_PROMOTE_ROLLOUT` | file | - | - | the rollout chain in order, e.g. `testing,production` |
| `PINGONE_PROMOTE_OVERLAY_PATTERN` | file | - | - | default `{env}.tfvars` (root overlays) |
| `PINGONE_PROMOTE_MR_LABELS` | file | - | - | default `pingone-promote` |
| `PINGONE_PROMOTE_SOURCE_ENVIRONMENT_ID` | optional | - | - | overrides `pingone_environment_id` from `<source>.tfvars` |
| `PINGONE_PROMOTE_LAYOUT` | optional | - | - | `default` or `terraformer` file names |

Every name is configurable with `pingone-promote request --<name>-env VAR`.
Values are never printed.

**Protected variables on request branches.** Protected variables are only
available on protected branches. Two options:

- *Recommended:* protect the wildcard `pingone-promote/*` with "Allowed to
  push and merge: Developers + Maintainers". Protected variables (worker
  secret, GitLab token) are then available to the job, and only project
  members can create request branches. The job's own refusal of protected
  branches does not apply to `pingone-promote/*`, by design.
- Unprotected but masked variables: simpler, but any branch pipeline (any
  developer's branch) can read them. Not recommended.

## Tokens

- **DaVinci's token:** a project access token on the config repository,
  Developer role, `api` scope. It creates request branches, writes
  `request.json`, and reads pipelines, the `result.json` artifact and MRs.
  It never holds PingOne or Vault credentials, and it does not need a
  pipeline trigger token (the branch push triggers the pipeline).
- **The job's token** (`PINGONE_PROMOTE_GITLAB_TOKEN`): a different project
  access token, Developer role, `api` scope, masked and protected.

## What the jobs may and may never do

`pingone-promote:request` may:

- read the checked-out branch, run the pinned terraformer against the
  SOURCE environment with a read-only worker;
- commit once to its own `pingone-promote/<requestId>` branch: allow-listed
  paths only (`definitions/*.tf`, `main.tf`, `variables.tf`, `secrets.tf`,
  `imports.tf`, `versions.tf`, `<env>.tfvars`, `customer_secrets.yml`) and
  the deletion of `.pingone-promote/request.json`;
- open or find one merge request for that branch.

It may never: plan or apply; commit to the default branch, a protected
branch outside `pingone-promote/`, another request's branch, CI files, dot
paths or `*.auto.tfvars`; overwrite a concurrent push (that is a
`conflict`); commit twice for one `requestId`; write secret values anywhere.

`pingone-promote:check` (merge request pipelines only) may read the
repository and run `terraform fmt`; it never writes.

## request.json, requestVersion 1

Unknown fields fail. Maximum 256 KiB.

```json
{
  "requestVersion": 1,
  "requestId": "20261103-120000-abc123",
  "requestedBy": "jdoe",
  "source": "development",
  "flows": [{ "id": "f0000000000000000000000000000002", "name": "Flow B" }],
  "applications": [{ "name": "Web" }],
  "flowPolicies": [{ "name": "Web Policy" }],
  "dependencyChoices": { "pingone_davinci_connector_instance.pingcli__PingOne": "update" },
  "envValues": {
    "testing": {
      "pingone_environment_id": "22222222-2222-4222-8222-222222222222",
      "davinci_variable_companyName_value": "Example Corp",
      "davinci_application_Web_oauth_redirect_uris": ["https://auth.pingone.com/22222222-2222-4222-8222-222222222222/rp/callback/openid_connect"]
    }
  },
  "secretMappings": { "davinci_connection_PingOne_clientSecret": "PingOne_DaVinci_connection_PingOne_clientSecret" },
  "connectorEnvLocal": { "pingcli__PingOne": ["clientId"] },
  "inlineValues": ["davinci_variable_companyName_value"],
  "baseCommit": "0123456789abcdef0123456789abcdef01234567",
  "mode": "mr",
  "branchName": "pingone-promote/20261103-120000-abc123",
  "commitMessage": "Add Flow B from development",
  "title": "PingOne: Flow B from development",
  "description": "Adds the account-recovery subflow.",
  "dryRun": true
}
```

| Field | Required | Rules |
| --- | --- | --- |
| `requestVersion` | yes | `1`; anything else is `unsupported_version` |
| `requestId` | yes | `^[A-Za-z0-9][A-Za-z0-9_-]{2,63}$` (DaVinci uses `YYYYMMDD-HHMMSS-xxxxxx`); the branch must be `pingone-promote/<requestId>` |
| `requestedBy` | no | a username, at most 128 characters, one line; written as `Requested-by:` |
| `source` | yes | environment name; must be in `PINGONE_PROMOTE_SOURCES` (when set) and not in `PINGONE_PROMOTE_ROLLOUT` |
| `flows` | one of flows, applications, flowPolicies | up to 20 `{id, name}`; `id` (32 hex or UUID) selects when present, `name` is then only checked; a name alone matches an exact ID, an exact name, or a unique case-insensitive substring; several matches fail with `candidates` |
| `applications`, `flowPolicies` | no | up to 20 `{name}` each; included only when listed |
| `dependencyChoices` | no | address -> `update` or `keep`; default: selected flows update, new dependencies are added, differing dependencies are kept |
| `envValues` | no | environment -> root variable -> JSON value; environments must be the source or in the rollout; `import_ids` and secret-looking values are refused |
| `secretMappings` | no | terraformer secret variable (`requiredSecrets[].key`) -> declared `customer_secrets.yml` variable name |
| `connectorEnvLocal` | no | connector label -> property names kept as environment values |
| `inlineValues` | no | DaVinci variable value variables that are the same in every environment |
| `baseCommit` | no | commit SHA for three-way status (the commit last rolled out to the source) |
| `mode` | no | `mr` (default) or `branch` |
| `branchName` | no | if set, must be `pingone-promote/<requestId>` |
| `commitMessage` | no | at most 2000 characters; default `PingOne: <flow names> from <source>`; trailers are appended |
| `title` | no | MR title, one line, at most 255 characters |
| `description` | no | MR description prefix, at most 64 KiB; the change table and `Requested-by:`/`Request-id:` lines follow |
| `dryRun` | no | `true` previews only |

## result.json, resultVersion 1

```json
{
  "resultVersion": 1,
  "requestId": "20261103-120000-abc123",
  "requestedBy": "jdoe",
  "status": "submitted",
  "dryRun": false,
  "mode": "mr",
  "source": "development",
  "branch": "pingone-promote/20261103-120000-abc123",
  "commitId": "<sha>",
  "mrUrl": "https://gitlab.example/<group>/<project>/-/merge_requests/7",
  "mrIid": 7,
  "branchUrl": "https://gitlab.example/<group>/<project>/-/tree/pingone-promote%2F20261103-120000-abc123",
  "diffUrl": "https://gitlab.example/<group>/<project>/-/commit/<sha>",
  "selected": [{ "query": "f0000000000000000000000000000002", "address": "pingone_davinci_flow.pingcli__Flow-0020-B", "name": "Flow B", "id": "f0000000000000000000000000000002" }],
  "changes": [
    { "address": "pingone_davinci_flow.pingcli__Flow-0020-B", "role": "selected", "action": "updated", "status": "changed-in-source", "file": "definitions/pingone_davinci_flow.tf" },
    { "address": "pingone_davinci_connector_instance.pingcli__PingOne", "role": "dependency", "action": "kept_differs", "status": "changed-in-source", "file": "definitions/pingone_davinci_connector_instance.tf", "reason": "differs from Git; kept because it is a shared dependency (choose update to use the source version)" }
  ],
  "summary": { "added": 3, "updated": 1, "kept_differs": 1, "unchanged": 6, "moved": 0 },
  "files": [{ "path": "definitions/pingone_davinci_flow.tf", "action": "update" }, { "path": ".pingone-promote/request.json", "action": "delete" }],
  "requiredSecrets": [{ "key": "davinci_variable_apiKey_value", "varName": "davinci_variable_apiKey_value", "field": "davinci_variable_apiKey_value", "declared": false, "vault": "apps/pingone-davinci/${VAULT_PATH_ENV}/pingone/davinci_variable_apiKey_value@secret" }],
  "blockers": { "testing": [{ "code": "missing_value", "var": "davinci_variable_companyName_value", "message": "testing.tfvars must set davinci_variable_companyName_value" }] },
  "warnings": [],
  "diff": "--- a/definitions/pingone_davinci_flow.tf\n+++ b/...",
  "diffTruncated": false,
  "error": null
}
```

| Field | Meaning |
| --- | --- |
| `status` | `previewed` (dryRun), `submitted`, `no_changes` (only the request file was deleted, no MR), `already_submitted` (this `requestId` was committed before; the existing commit and MR are reported), `blocked`, `failed` |
| `mrUrl`, `mrIid`, `branchUrl`, `diffUrl` | links; `diffUrl` is the commit when one was made, otherwise the compare view of the branch |
| `changes[]` | one per address in the export. `role`: `selected` or `dependency`. `action`: `added`, `updated`, `kept_differs`, `unchanged`, `moved` (relabelled; `from` is the old address). `status`: `new`, `changed-in-source`, `matches`, `git-newer`, `changed-in-both` |
| `summary` | counts per action |
| `files[]` | `create`, `update` or `delete` per path |
| `requiredSecrets[]` | secrets the written blocks need. `key`: terraformer's name, the key of `secretMappings`. `varName`: the Terraform variable after mapping (`TF_VAR_<varName>`). `field`: the Vault field. `declared: false` means this request adds the `customer_secrets.yml` entry and the Vault value must exist for every environment |
| `blockers` | environment -> list of `{code, var, address, message}`; `_request` holds blockers for no single environment. Codes: `missing_value`, `missing_overlay`, `undeclared_key`, `secret_in_overlay`, `invalid_import_key`, `changed_in_both`, `customer_secrets`, `secret_finding`, `missing_dependency`, `auto_tfvars` |
| `diff` | unified diff of the change, at most 1 MiB (`diffTruncated`) |
| `error` | `null`, or `{code, type, message, retryable, field?, candidates?}`. `code`: `conflict`, `preflight`, `partial`, `blocked`. `type`: `validation`, `export`, `merge`, `gitlab`, `internal`. `candidates` lists matches for an ambiguous name |

The final log line is `PINGONE_PROMOTE_RESULT` followed by
`{resultVersion, requestId, status, mrUrl, branchUrl, commitId, summary,
blockedEnvs, error}`.

Exit status: 0 for `previewed`, `submitted`, `no_changes`,
`already_submitted`; 1 for `blocked` and `failed`.

## Overlays

Environment values live in `<env>.tfvars` at the repository root by
default, matching the existing repository (`development.tfvars`,
`testing.tfvars`, `production.tfvars`). The pattern is configurable
(`PINGONE_PROMOTE_OVERLAY_PATTERN`, `check --overlay-pattern`), for
example `environments/{env}.tfvars`. Never use `*.auto.tfvars`.
