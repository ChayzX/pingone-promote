# PingOne Promote: DaVinci Request flow

> **Setting it up or updating it? Start with [IMPORT.md](IMPORT.md):** one checklist of every value to fill in, and what changed since earlier versions.

> Replaces the local app for requesters. It supersedes the [front-door preview](../davinci-front-door/README.md): this flow commits an ordinary `requests/<id>/request.json`, so the pipeline needs no intent support.

A DaVinci main flow and four subflows that replace the Go tool's local app. Everything after the merge request is unchanged: the same GitLab project, pipeline, `pingone-runner`, approvals and rollback.

| Flow | What it does | Ends with |
| --- | --- | --- |
| **PingOne Promote - Request** (main; the one the application launches) | Checks its settings, gets the worker token, invokes the subflows in order, builds both inventories, shows the result | **Merge request opened** |
| **1 Choose environments** | Source and target buttons from the four slots | Returns the chosen pair |
| **2 Snapshot environment** (invoked twice) | Live snapshot polled to COMPLETE, every page read | Returns the snapshot and pages |
| **3 Choose resources and review** | Resource picker, `request.json` build, promotion-variable check, review | Returns the request on **Submit** (Cancel ends here) |
| **4 Submit to GitLab** | Project and branch checks, branch, commit, merge request | Returns the MR link |

Each step is its own flow in **Analytics**, so a run shows which subflow it reached and the step inside it. All five log at Debug. Admin settings are set once, in the main flow; it passes each subflow what it needs as inputs.

```
DaVinci flow (this kit)                         GitLab (unchanged)
─────────────────────────────────────────       ───────────────────────────────────────────
[Sign-in: off for the proof of concept]         MR pipeline: validate_request
Live snapshots of source and target             Merge → plan_promotion (native draft + review)
Requester picks resources and mappings          Deployment approval → apply
Promotion-variable check                        Rollback: separate manual pipeline
Builds request.json (byte-identical to Go)
Branch promotion/<id> → commit → merge request ──▶
```

The flow never calls the PingOne promotions API, so it cannot deploy or roll back anything. The flow run test checks that.

**Proof of concept: sign-in is off.** The flow starts straight at the resource picker. The admin SSO, the DaVinci Admin check and the requester line in the MR are built and tested but switched off. Turn them on with `"requireSignIn": true` in `values.json`, or `enable_signin = true` plus `gen_flows.py --terraform . --signin`; see the [runbook](RUNBOOK.md#turning-sign-in-back-on). Until then, anyone who can launch the flow can open a merge request. GitLab's MR and deployment approvals still gate every change.

**Status: first live import done; the REST node body fields were corrected from it.** The rest is tested offline, and the Terraform passes `terraform validate` against the real pingone provider. See [What still needs a live check](#what-still-needs-a-live-check).

## What the admin sees

0. **Settings check.** Before any API call, the flow checks its flow variables (host environment, origins, environment slots, GitLab URL and project IDs) and, if any is a placeholder or malformed, shows **Settings need attention** naming each one.
1. **Sign in** (only when sign-in is on). PingOne SSO, which sends the admin to Entra. The flow then checks for the DaVinci Admin role on the organization or on one of the configured environments. No GitLab account is needed.
2. **Choose environments.** Two pages with one button per configured environment (up to four): first the source, then the target. The same environment twice, or an empty slot, is refused.
3. **Choose resources.** The local app's Step 2: a table of every source resource (name, ID, type, dependencies) with search across name, type and ID, a type filter and a selection count. Each row has a destination-mapping dropdown of same-type target resources, pre-filled as the local app does it: PingOne's saved promotion mappings first, then a unique same-type name match (cross-checked against Go's `suggestMappings`). **Show dependencies** and **Select listed dependencies** work as in the app, and a description becomes the MR title. The page shows names, IDs and types, never configuration. If the screen's script can't run, plain lists and text boxes for IDs and `sourceId=targetId` mappings take its place.
4. **Review request.** The page shows the promotion-variable check (below) and `requests/<id>/request.json` exactly as it will be committed, with secrets redacted. The admin chooses **Submit to GitLab** or **Cancel**.
5. **Merge request opened.** The page links to the MR. Nothing has changed in PingOne at this point.

> **One GitLab project for every environment.** All requests go to `gitlabProjectId`. In that project, each environment you deploy to is a GitLab environment (`dev`, `test`, `production` …) with its own scoped variables and approvals, and the pipeline from `gitlab/make_pipeline.py` runs each environment's jobs only for requests that target it. Name a target's GitLab environment in `envNGitlabEnvironment` (Terraform: `gitlab_environment` in `environments`); empty uses `gitlabEnvironment`. See the runbook's [One project, several environments](RUNBOOK.md#one-project-several-environments).

## Promotion-variable check

PingOne requires a **sensitive promotion variable** for every secret attribute of a promoted resource, such as a DaVinci connector's client secret. Without one, the native draft fails and the GitLab plan stops. The flow checks this before submitting, so the admin can fix it in PingOne first rather than finding out at plan time.

1. **Find the secrets.** Every value the request redacts (`[REDACTED]`) is a sensitive attribute. The flow lists each one by resource ID and path, for example `clientSecret` or `properties.clientSecret.value`.
2. **Read the declarations.** `GET /v1/environments/{source}/promotionVariableDeclarations/{resourceId}` for each selected resource. This shows which property maps to which variable.
3. **Read the definitions.** `GET /v1/environments/{source|target}/promotionVariableDefinitions`, every page. Sensitive values come back masked; the flow only needs each variable's name and whether it is sensitive.
4. **Report** each sensitive attribute as one of:
   - **ready:** a sensitive variable is declared and defined;
   - **needs a variable:** nothing is declared for it;
   - **declared but undefined:** no definition was found in source or target;
   - **not sensitive:** a definition exists but isn't marked sensitive, which PingOne rejects for secrets;
   - **could not be checked:** PingOne didn't answer.

The report appears on the review page and as a **Promotion variables** section at the end of the merge request description, so approvers see it too. It is **advisory**: it never blocks the submission, and if the API can't be read (feature off, missing role) the flow says so and carries on. The GitLab plan still enforces PingOne's own rules. `request.json` is unchanged, and no secret value is read or shown.

## How it matches the Go tool

| Go (commit `537dbf4`) | In the flow | Checked how |
| --- | --- | --- |
| `pingone.Inventory`, `apiURL`, `Pages` | `inventoryFromSnapshots`, `apiURL`, `pagesComplete` | The real Go client against a fake API: 33 fixtures, 0 differences |
| `promotion.BuildRequest`, `Redact` | `buildRequest`, `redact` | 57 fixtures run through the Go functions: 0 differences |
| `gitops.NewRequest` + `Encode` | `newRequest`, `encodeRequest`, `goJSON` | **Byte-for-byte** against Go on 19 fixtures: HTML characters, U+2028, control characters, astral-plane keys, `1e21`, `1e-7`, `-0` and secrets. Go's `Decode` accepts every JS-built file. |
| `gitlab.Config.Validate`, `sameGitLabLocation` | `gitlabConfig`, `sameGitLabLocation` | 324 URL/config fixtures against Go: 0 differences |
| `Client.VerifyProject`, `Submit`, MR title and description | `checkProject`, `checkDefaultBranch`, `submitBodies`, `checkMergeRequest` | Flow run test against a fake GitLab |
| The pipeline's `select-request.sh` + `pingone-runner validate` | — | Each request the flow commits is replayed as a GitLab MR and passes the Go tool's own selector and validator |

The Go Submit sequence, reproduced step for step:

1. Read the project. It must be the configured ID, private, not archived, have a default branch, and its `web_url` must be on the configured server path.
2. Read the default branch. It must be protected.
3. Create the branch `promotion/<id>` from the default branch.
4. Commit `requests/<id>/request.json` (the file plus a trailing newline) with the message `Add promotion request <id>`.
5. Open the MR with `remove_source_branch: true` and `squash: false`. The title is the description, or "Promote selected PingOne resources", cut to 180 bytes. The description is the same template the Go app uses.
6. The MR's `web_url` must be on the configured server path.

If any step fails, the flow stops before the next GitLab write.

**Deliberate differences from the Go app:**
- The promotion-variable check above is new. The Go app has no equivalent; it shows PingOne's errors at plan time.
- The MR description has one extra line: `Requested in DaVinci by: <username> (PingOne user <id>)`. The GitLab token belongs to a bot, so this line records who asked. `request.json` itself is unchanged.
- A GitLab base URL containing `\` is refused. Go accepts it, but URL parsers in HTTP clients can read `\` as `/`.
- The request ID uses the Web Crypto random generator when DaVinci provides it. Otherwise it is SHA-256 over the time, `Math.random` and the request. It needs to be unique, not secret.

## Credentials

| Credential | Lives in | Needs |
| --- | --- | --- |
| Admin's own sign-in | Admins' PingOne environment, through your Entra SSO | DaVinci Admin on the organization or the source environment |
| Snapshot worker | DaVinci company secret `promoteWorkerBasicAuth`, as `base64(clientId:clientSecret)` | Read environments and create Configuration Management snapshots on source and target (the role that fixed the acceptance-test 403s), read promotion-variable declarations and definitions there, plus Identity Data Read Only on the admins' environment for the role check |
| GitLab token | DaVinci company secret `promoteGitlabToken` | A project or group access token with `api` scope and the Developer role on the promotion project only. It can open MRs. The protected default branch and the pipeline's deployment approval stop it from deploying. |

Production PingOne write credentials stay in protected GitLab CI variables, as before. Neither secret in DaVinci can execute a promotion. Anyone who can edit this flow can still use both secrets, so give DaVinci Admin on the host environment to as few people as possible.

## Setup

**Just want to import the flow? Use [IMPORT.md](IMPORT.md)** and the file in [`export/`](export/pingone-promote-request.flow.json).

For the full setup, follow **[RUNBOOK.md](RUNBOOK.md)**. It covers prerequisites, the GitLab token, setup with Terraform ([`terraform/`](terraform/), pingone provider 1.23+) or by hand in the console, the one console step both paths share, first-run checks, troubleshooting and rotation.

## What still needs a live check

These were written from documentation and real export samples, not captured from a tenant. Check them on first import:

1. **REST node fields: confirmed on a live import.** DaVinci saves `url`, `httpMethod`, `headers`, and the body as `httpBody` = `raw` plus a plain-text `raw` value; `check_flows.py` enforces that shape.
2. **Redirects.** The Go client refuses GitLab redirects. Check whether the DaVinci HTTP connector follows them. If it does, GitLab must not redirect API calls.
3. **Sign-on output (sign-in on only).** The sign-on capability is exported as `loginFirstFactor`. Its `accessToken` output is confirmed by Ping's connector docs.
4. **Loops.** Snapshot polling and pagination route back to an earlier REST node. DaVinci must allow this cycle.
5. **Function outputs.** Confirm each function's Output Schema lists the fields downstream nodes read, such as `state`, `requestJson` and `branchBody`.
6. **Size.** Large selections make `request.json` large. It is held in function output and sent as one commit body, and DaVinci may cap either.
7. **Promotion variables.** These endpoints are early access, so check:
   - that declarations live in the **source** environment, keyed by the source resource ID (the flow assumes so);
   - how a declaration names a nested DaVinci connector property (the flow matches a declared property to the attribute's path or any parent of it);
   - that definitions report `isSensitive` or a masked value.

   Create one sensitive variable in the console and compare what the review page reports.
8. **Randomness.** Check whether `globalThis.crypto` exists in DaVinci functions. The fallback works either way.

## Tests (no tenant needed)

```sh
bash examples/davinci-request-flow/run_tests.sh   # from a clone of this repository
```

The Go cross-checks build the Go tool at commit `537dbf4` from this repository's history, in a temporary clone; nothing in the working tree changes. Outside the repository, set `GO_TOOL` to a checkout. Needs Node 18+, Python 3 and Go 1.24+.

- `test/unit.js` (61 checks): SHA-256 against Node crypto, `apiURL`, GitLab project, branch, status and MR checks, ID and time formats, and byte truncation.
- `test/flow_run.js` (120 checks with sign-in off, 123 with it on; both run): **runs the generated export itself**. It executes each node, evaluator branch, template and flow variable against a fake PingOne and a fake GitLab. Function inputs are fed both as objects and as JSON strings. It covers:
  - the happy path, including the exact GitLab payloads;
  - title truncation;
  - the promotion-variable check: ready, missing, declared but undefined, not sensitive, API refused, no secrets, and paging of definitions;
  - non-admin, cancel and invalid input, each ending with no GitLab call;
  - a public, archived, wrong-ID or wrong-host project, and an unprotected branch, each ending with no GitLab write;
  - a failure at each write step;
  - a merge request URL on another host.

  Ten deliberate wiring faults were injected into the export, and the run caught each one.
- `check_flows.py`: structure checks. It confirms one start node, every node reachable, references only to nodes that can run earlier, flow variables written before they're read, evaluator branches wired, and function inputs matching their code.
- `test/shipped_code.js`: every function node in the export is byte-identical to the tested code.
- With `GO_TOOL`: the Go cross-checks in the table above, plus `test/pipeline_sim.sh`.

## First live run

| Case | Expected |
| --- | --- |
| Non-admin signs in | "Access denied: You need the DaVinci Admin role…"; no snapshots, no GitLab call |
| Admin selects one mapped flow and submits | An MR in the promotion project with one new `requests/<id>/request.json`; `validate_request` passes |
| Merge the MR | `plan_promotion` runs, as it does for requests from the Go app |
| Select a DaVinci connector with a secret and no promotion variable | Review page and MR say it needs a sensitive promotion variable; after you create one in PingOne, a new request shows it as ready |
| Cancel on the review page | "Cancelled. Nothing was sent to GitLab." |
| Temporarily unprotect the default branch, then submit | "GitLab default branch must be protected"; no branch created |
| Point `gitlabProjectId` at a public project | "GitLab project must be private…"; no branch created |
