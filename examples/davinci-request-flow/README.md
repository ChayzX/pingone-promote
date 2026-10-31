# PingOne Promote: DaVinci Request flow

> **Setting it up or updating it? Start with [IMPORT.md](IMPORT.md):** one checklist of every value to fill in, and what changed since earlier versions.

> Replaces the local app for requesters. The flow commits a promotion **intent** (`intents/<id>/intent.json`); the protected plan job verifies its target, takes fresh snapshots, and builds the redacted `request.json` with `pingone-runner build-request`. Pin `PROMOTION_TOOL_REF` to the reviewed full SHA of this DaVinci parity branch; pin `PROMOTION_VALIDATION_TOOL_REF` to a reviewed full SHA for the separate untrusted validation runner.

A DaVinci main flow and four subflows that replace the Go tool's local app for requesters. The flow does the choosing; the Go runner does the heavy lifting in GitLab. Planning, approval, deployment and rollback are unchanged.

**Why an intent.** A full `request.json` carries every selected resource's configuration, redacted and fingerprinted. Building it means holding whole snapshots, and large environments (dozens of DaVinci flows, hundreds of variables) are tens of megabytes: a live run in such an environment ended on DaVinci's generic error page with nothing logged. The flow now keeps only names, IDs, types and dependencies (tested at 24 MB of configuration: under 100 KB moves between flows). After the merge, `pingone-runner build-request` takes fresh snapshots and builds the request inside the protected plan job, with the same redaction and fingerprints as the local app.

| Flow | What it does | Ends with |
| --- | --- | --- |
| **PingOne Promote - Request** (main; the one the application launches) | Checks its settings, gets the worker token, invokes the subflows in order, reads PingOne's saved promotion mappings, lists both environments, shows the result | **Merge request opened** |
| **1 Choose environments** | Source and target buttons from the four slots | Returns the chosen pair |
| **2 Snapshot environment** (invoked twice) | Live snapshot polled to COMPLETE, every page read, kept as names, IDs, types and dependencies only | Returns that list |
| **3 Choose resources and review** | Resource picker, intent build (checked with the runner's rules), review | Returns the intent on **Submit** (Cancel ends here) |
| **4 Submit to GitLab** | Project and branch checks, branch, commit, merge request | Returns the MR link |

Each step is its own flow in **Analytics**, so a run shows which subflow it reached and the step inside it. Admin settings are set once, in the main flow; it passes each subflow what it needs as inputs.

```
DaVinci flows (this kit)                        GitLab (the tool's pipeline)
─────────────────────────────────────────       ───────────────────────────────────────────
[Sign-in: off for the proof of concept]         MR pipeline: validate_request (--intent)
Live snapshots: names, IDs, types only          Merge → plan_promotion:
Requester picks resources and mappings            pingone-runner build-request (fresh snapshots,
Builds intents/<id>/intent.json                    redaction, fingerprints) → plan + review
Branch promotion/<id> by default → commit → merge request ──▶ Deployment approval → apply
                                                Rollback: separate manual pipeline
```

The flow never calls the PingOne promotions API, so it cannot deploy or roll back anything. The flow run test checks that.

**Proof of concept: sign-in is off.** The flow starts straight at the environment picker. The admin SSO and the DaVinci Admin check are built and tested but switched off. An intent must name who asked, so with sign-in off it says `Sign-in off (proof of concept, requester not verified)` (user ID `sign-in-off`), visibly, in the intent, the MR and the request the runner builds. Turn sign-in on with `"requireSignIn": true` in `values.json`, or `enable_signin = true` plus `gen_flows.py --terraform . --signin`; see the [runbook](RUNBOOK.md#turning-sign-in-back-on). Until then, anyone who can launch the flow can open a merge request. GitLab's MR and deployment approvals still gate every change.

**Status: proof of concept, imported and run live up to the snapshot step.** Live runs fixed the REST body fields, styling (CSS/Script fields), timeouts and large-environment size. The rest is tested offline, and the Terraform passes `terraform validate` against the real pingone provider. See [What still needs a live check](#what-still-needs-a-live-check).

## What the admin sees

0. **Settings check.** Before any API call, the flow checks its flow variables (host environment, origins, environment slots, GitLab URL and project IDs) and, if any is a placeholder or malformed, shows **Settings need attention** naming each one. Unused slots (blank, `null` or filler) are fine.
1. **Sign in** (only when sign-in is on). PingOne SSO, which sends the admin to Entra. The flow then checks for the DaVinci Admin role on the organization or on one of the configured environments. No GitLab account is needed.
2. **Choose environments.** Two pages with one button per configured environment (up to four): first the source, then the target.
3. **Choose resources.** The local app's Step 2: a table of every source resource (name, ID, type, dependencies) with search across name, type and ID, a type filter and a selection count. Each row has a destination-mapping dropdown of same-type target resources, pre-filled as the local app does it: PingOne's saved promotion mappings first, then a unique same-type name match (cross-checked against Go's `suggestMappings`). **Show dependencies** and **Select listed dependencies** work as in the app, and a description becomes the MR title. If the screen's script can't run, plain lists and text boxes for IDs and `sourceId=targetId` mappings take its place.
4. **Review request.** The page shows `intents/<id>/intent.json` exactly as it will be committed and offers optional branch name and commit message fields, with the same safety checks as the local app. The selection and mappings were already checked with the runner's request rules (unknown resources, type mismatches, two sources on one target), so a merged intent builds. **Submit to GitLab** or **Cancel**.
5. **Merge request opened.** The page links to the MR. Nothing has changed in PingOne at this point. After the merge, the plan job builds the full request and PingOne's plan, with the `plan-summary.md` change summary for approvers; PingOne's plan reports any missing promotion variables.

> **One GitLab project for every environment.** All requests go to `gitlabProjectId`. In that project, each environment you deploy to is a GitLab environment (`dev`, `test`, `production` …) with its own scoped variables and approvals, and the pipeline from `gitlab/make_pipeline.py` runs each environment's jobs only for requests that target it. Name a target's GitLab environment in `envNGitlabEnvironment` (Terraform: `gitlab_environment` in `environments`); empty uses `gitlabEnvironment`. See the runbook's [One project, several environments](RUNBOOK.md#one-project-several-environments).

## How it matches the Go tool

| Go runner (selected current branch commit) | In the flow | Checked how |
| --- | --- | --- |
| `gitops.DecodeIntent` / `ValidateIntent` | `newIntent`, `validateIntent` | 59 intents (as built, and breaking each rule) through Go's `DecodeIntent`: same accept/reject and the same message every time |
| The pipeline's `select-request.sh` + `pingone-runner validate --intent` | — | Each intent the flow commits is replayed as a GitLab MR and passes the Go tool's own selector and validator |
| Local app `suggestMappings` | `suggestMappings` | 52 fixtures (incl. Unicode names) against Go: 0 differences |
| `pingone.Inventory`, `apiURL`, `Pages` | `inventoryFromSnapshots`, `apiURL`, `pagesComplete` | The real Go client against a fake API: 33 fixtures, 0 differences |
| `promotion.BuildRequest` selection and mapping rules | `buildRequest` (as a check on the slim inventories) | 57 fixtures through the Go functions: 0 differences |
| `gitlab.Config.Validate`, `ValidateSubmissionOptions`, `sameGitLabLocation`, `VerifyProject` | `gitlabConfig`, `validateSubmissionOptions`, `sameGitLabLocation`, `checkProject`, `checkDefaultBranch` | 336 URL/config/branch/commit fixtures against Go: 0 differences |

The submit sequence:

1. Read the project. It must be the configured ID, private, not archived, have a default branch, and its `web_url` must be on the configured server path.
2. Read the default branch. It must be protected.
3. Create the chosen branch (default `promotion/<id>`) from the default branch.
4. Commit `intents/<id>/intent.json` with the chosen message (default `Add promotion intent <id>`).
5. Open the MR with `remove_source_branch: true` and `squash: false`. The title is the description's first line, or "Promote selected PingOne resources", cut to 180 bytes. The description names both environments, the GitLab environment, the requester and every selected resource with its mapping.
6. The MR's `web_url` must be on the configured server path.

If any step fails, the flow stops before the next GitLab write.

**What moved to GitLab:** the full `request.json` (redacted configurations and fingerprints) and the promotion-variable check now come from the runner in the plan job, where PingOne's plan reports a missing sensitive promotion variable. The flow no longer reads promotion-variable declarations or definitions.

## Credentials

| Credential | Lives in | Needs |
| --- | --- | --- |
| Admin's own sign-in | Admins' PingOne environment, through your Entra SSO | DaVinci Admin on the organization or the source environment |
| Snapshot worker | DaVinci company secret `promoteWorkerBasicAuth`, as `base64(clientId:clientSecret)` | Read environments, create Configuration Management snapshots and read saved promotion mappings on source and target (the role that fixed the acceptance-test 403s), plus Identity Data Read Only on the admins' environment for the role check (sign-in on) |
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
6. **Size.** The flow keeps only names, IDs, types and dependencies, but each snapshot page still passes through one REST step. If a single page is too large for DaVinci, the snapshot subflow's error screen will name the page step.
7. **Runner version.** `PROMOTION_TOOL_REF` must point to the reviewed full SHA of this DaVinci parity branch, which provides `validate --intent` and `build-request`. `PROMOTION_VALIDATION_TOOL_REF` is a separate full SHA used on the untrusted MR validation runner.
8. **Randomness.** Check whether `globalThis.crypto` exists in DaVinci functions. The fallback works either way.

## Tests (no tenant needed)

```sh
bash examples/davinci-request-flow/run_tests.sh   # from a clone of this repository
```

The Go cross-checks build the selected current branch commit from a temporary clone; set `GO_TOOL_REF` to another reviewed full SHA when needed. Outside the repository, set `GO_TOOL` to the checkout to clone. Neither checkout is modified. Needs Node 18+, Python 3, Go 1.27.3+ and, for the screen test, Playwright with Chromium.

- `test/unit.js` (79 checks): SHA-256 against Node crypto, `apiURL`, GitLab project, branch and commit validation, status and MR checks, ID and time formats, and byte truncation.
- `test/flow_run.js` (about 175 checks with sign-in off and on; both run): **runs the generated export itself**, main flow and subflows, against a fake PingOne and a fake GitLab. It executes each node, evaluator branch, template, flow variable and subflow call; function inputs are fed both as objects and as JSON strings. It covers the happy path with the exact GitLab payloads, custom branch and commit metadata, the intent's content, the settings check, environment slots, a failed call at each step (named error screens), cancel, invalid input, project and branch refusals, a failure at each write, and a large environment (80 resources of 300 KB configuration: under 100 KB moves between flows, and no configuration value reaches any screen, flow variable or GitLab).
- `test/picker_ui.js` (19 checks): the exported resource picker, CSS and script in Chromium: search, type filter, dependencies, mapping suggestions, the submitted fields, names shown as text, hidden unused environment slots, and the working page's delayed fallback button.
- `check_flows.py`: structure checks on all five flows: one start node, every node reachable, references only to nodes that can run earlier, flow variables written before they're read, every REST call with a failure branch, no `<style>`/`<script>` in Custom HTML, function inputs matching their code, and subflow wiring (inputs match the schema, parameters declared, returned values declared, a screen on every subflow path).
- `test/shipped_code.js`: every function node in the export is byte-identical to the tested code, and every screen script parses.
- With `GO_TOOL`: the Go cross-checks in the table above, `test/pipeline_sim.sh` and `test/one_project_sim.sh`.

## First live run

| Case | Expected |
| --- | --- |
| Non-admin signs in | "Access denied: You need the DaVinci Admin role…"; no snapshots, no GitLab call |
| Admin selects one mapped flow and submits | An MR in the promotion project with one new `intents/<id>/intent.json`; `validate_request` passes |
| Merge the MR | `plan_promotion` runs `build-request`, then plans; `plan-summary.md` summarizes the plan for approvers |
| Select a DaVinci connector with a secret and no promotion variable | The plan job's PingOne plan reports the missing sensitive promotion variable |
| Cancel on the review page | "Cancelled. Nothing was sent to GitLab." |
| Temporarily unprotect the default branch, then submit | "GitLab default branch must be protected"; no branch created |
| Point `gitlabProjectId` at a public project | "GitLab project must be private…"; no branch created |
