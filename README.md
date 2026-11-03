# PingOne Promote: DaVinci feasibility test

One DaVinci flow you import into a **sandbox** PingOne environment. It runs the pingone-promote workload inside DaVinci and times every stage, so we can decide on evidence whether DaVinci can carry it.

> **The hosted design is the primary path** ([epic #58](https://github.com/ChayzX/pingone-promote/issues/58)). This test exists because an earlier DaVinci-based iteration was rejected as "the API was too slow". The test tells us whether that was DaVinci or that design.

- Import file: [`davinci-feasibility/flow/pingone-promote-feasibility.flow.json`](davinci-feasibility/flow/pingone-promote-feasibility.flow.json)
- Time needed: about 30 minutes to set up, then a few minutes per run.
- What we need back: the **Copy results** JSON from the result screen of each run (see [What to send back](#what-to-send-back)).

## What the test measures

Every stage records `Date.now()` before and after, plus call counts, payload sizes and errors.

| # | Stage | What happens | What it tells us |
| --- | --- | --- | --- |
| 0 | Calibrate | 10 loop iterations with no HTTP call (function, save, evaluator) | DaVinci's own per-node overhead |
| 1 | Auth | PingOne worker token by client credentials, for a **read-only** worker | Token latency |
| 2 | Inventory | Lists flows (`?attributes=id,name`), connector instances and variables in the source environment, following `_links.next` | Paging and list latency |
| 3 | Bulk fetch | Gets N full flows by ID, one REST call each. You choose N: **1, 5, 20 or all** | DaVinci looping, per-call latency, payload size, flow timeout |
| 4 | Translate | Custom JavaScript turns each flow into a tfvars fragment as it arrives, then assembles `environments/<target>.tfvars` | Whether custom functions can do the translation in time |
| 5 | GitLab write | Commits that file to a **new branch you name** in a disposable test project. Opening a merge request is optional and off by default | GitLab write latency and the guard rails |
| 6 | Result | Timing table, PASS/WARN/FAIL checks, the secrets a real submission would need, and the copyable results JSON | |

The translation follows the hosted design's tfvars conventions (ADR 0003, 0005 and 0009 on `terraform/phase1-foundation`):

- **Logical keys.** Keys come from names and are lossless: `PingOne SSO` becomes `PingOne-20SSO`. Raw IDs are replaced by `@@flow:<key>@@`, `@@connection:<key>@@`, `@@variable:<key>@@` and `@@env:id@@`, and node connections by `connection = "<key>"`. An ID the test cannot resolve is kept and counted as `unresolvedIds`.
- **Variable keys always carry the context.** They are `<name>_company`, `<name>_flowInstance`, `<name>_user` or `<name>_flow_<flowKey>`. A company variable and a flow variable with the same name therefore get distinct keys. pingcli-terraformer has a bug here; this design avoids it, and the tests cover the case.
- **Subflow tiers.** Top-level flows go in t3 and the flows they call go below them, in t2 to t0.
- **Secrets.** A secret is any value masked as `"******"`, any property marked `secure`, any secret-named literal (`clientSecret`, `apiKey`, `*token`, `*password`) and any variable of type `secret`. Each one becomes `@@secret:<key>@@`, or `secret = "<key>"` for a variable, and the value is never emitted. Keys are Terraform variable names (`^[A-Za-z][A-Za-z0-9_]*$`):
  - `davinci_variable_<variableKey>_value` for DaVinci secret variables;
  - `davinci_connection_<connectionKey>_<property>` for connector properties;
  - `davinci_flow_<flowKey>_<nodeId>_<property>` for flow nodes.

  Characters that are not allowed in Terraform variable names become `_`.
- **Rendering.** The HCL is deterministic: sorted keys, quoted map keys, aligned `=` and canonical JSON heredocs, with `${` escaped as `$${` and `%{` as `%%{`.

> **Known gap: real `terraform fmt` cannot run inside DaVinci.** The flow imitates its layout, but formatting and validation must happen in the pipeline (Terraform 1.16). Run `terraform fmt -check` on the committed file there. A difference is a cosmetic finding, not a test failure.

## Prerequisites

1. **A PingOne sandbox environment** with DaVinci, containing some real flows to read. Never use production.
2. **A read-only worker app.** Add a **Worker** application in the environment that will hold it, and give it only **DaVinci Admin Read Only** on the source environment. Note its client ID and secret.
3. **A disposable, private GitLab test project.** The flow refuses a project that is not private.
4. **A project access token** on that project, with role **Developer** and scope **`api`**, and a short expiry date.
5. A DaVinci admin who can import flows and create company variables in the sandbox.

## Import (DaVinci console)

1. **DaVinci → Flows → Add Flow → Import from JSON**, and choose `pingone-promote-feasibility.flow.json`. The flow is called **PingOne Promote - DaVinci feasibility test**.
2. The nodes point at placeholder connections. Open the flow and point each node at your own connection of that type:

   | Placeholder connection ID | Connector |
   | --- | --- |
   | `a1f0d00a000000000000000000000002` | HTTP (REST calls and screens) |
   | `a1f0d00a000000000000000000000003` | Functions (custom functions and comparisons) |
   | `a1f0d00a000000000000000000000005` | Variables (Save Value) |

   To skip this step, generate a private copy with your connection IDs filled in (see [Rebuild and check](#rebuild-and-check)).
3. **Flow → Settings.** DaVinci keeps its own defaults on import, so set these by hand:
   - **Flow Timeout** and **Flow HTTP Response Timeout**: leave the tenant defaults (normally 300 s and 15 s) for the first run. Whatever they are, copy them into `fsFlowTimeoutSeconds` and `fsHttpTimeoutSeconds` (below) so the checks use the real limits.
   - **Logging:** Info. Debug logs node inputs and outputs, tokens among them; use it only to investigate a failure, then set it back.
4. **Deploy** the flow, then add a DaVinci **Application** with a flow policy that runs it at 100%, as the prior iteration did. You can also use the console's preview, if your tenant has one.

## Configure after import

**Company variables** (**DaVinci → Variables**). The import creates both as empty **secret** variables; DaVinci masks them and the flow never shows them.

| Variable | Value |
| --- | --- |
| `fsWorkerBasicAuth` | `base64(clientId:clientSecret)` of the read-only worker, e.g. `printf '%s:%s' "$ID" "$SECRET" \| base64` |
| `fsGitlabToken` | The GitLab project token. Only needed for stage 5. |

**Flow variables** (the flow's **Variables** panel). Re-importing resets them.

| Variable | Default | Set to |
| --- | --- | --- |
| `fsWorkerEnvironmentId` | empty | Environment that holds the worker app (its token URL) |
| `fsSourceEnvironmentId` | empty | Sandbox environment whose flows are read |
| `fsApiOrigin` / `fsAuthOrigin` | `https://api.pingone.com` / `https://auth.pingone.com` | Your region's domains, e.g. `.eu`, `.ca`, `.asia` |
| `fsGitlabApi` | `https://gitlab.com/api/v4` | Your GitLab API base |
| `fsGitlabProjectId` | empty | Numeric ID (or `group/project` path) of the test project |
| `fsFlowTimeoutSeconds` / `fsHttpTimeoutSeconds` | `300` / `15` | The values in **Flow → Settings** |
| `fsPathFlows`, `fsFlowsListQuery`, `fsPathFlow`, `fsPathConnectorInstances`, `fsPathVariables` | see [Assumptions](#assumptions-to-confirm-in-a-live-tenant) | Change only if a path turns out to be wrong |
| `fsCalibrationLoops` | `10` | 0 to 50 |

The start screen lists every setting as *set*, *not set* or *malformed*, by name only and never with its value. Fix anything marked in red, deploy, and launch again.

## Run each load level

Launch the flow. On the start screen:

- **Target name:** names the file `environments/<target>.tfvars`. The default is `feasibility`.
- **New GitLab branch:** for example `davinci-feasibility/run-1`. Leave it empty to skip stage 5.
  - The flow refuses the default branch, obviously protected names (`main`, `master`, `develop`, `production`, `release/…`, `hotfix/…` and so on), names that match a protected-branch rule, and any branch that already exists.
  - It never overwrites a branch. Use a new name every run.
- **Open a merge request:** `yes` opens a draft MR from the new branch. The default is no.
- **Hand back to the browser every N API calls:** the default is 10. 0 never hands back (see [Known DaVinci limits](#known-davinci-limits-to-watch)).

Then click **1 flow**, **5 flows**, **20 flows** or **All flows**. Flows are taken in logical-key order, so the same N always picks the same flows. Run the levels in this order, with a new branch name each time:

| Run | Level | Branch | Hand-back | Purpose |
| --- | --- | --- | --- | --- |
| 1 | 1 | empty | 10 | Smoke test, no GitLab |
| 2 | 1 | `davinci-feasibility/l1` | 10 | End to end |
| 3 | 5 | `davinci-feasibility/l5` | 10 | |
| 4 | 20 | `davinci-feasibility/l20` | 10 | **The key data point** |
| 5 | all | `davinci-feasibility/all` | 10 | Stress test |
| 6 | 20 | empty | 0 | One browser request: finds the per-request limit |

"Working" pages may flash between steps. They continue by themselves; if one stays for 10 seconds, click **Continue**.

## What to send back

For each run, click **Copy results** on the result screen and paste the JSON to us, with the run number from the table. It holds counts, sizes, timings, check results and redacted error codes only: no names, IDs, URLs, tokens or values. Please also tell us:

- the PingOne region and the two timeout values in Flow → Settings;
- whether `terraform fmt -check` (Terraform 1.16) passes on a committed file, if you can run it;
- anything DaVinci showed that the result screen did not, for example its generic error page or a `TIMEOUT` in Analytics.

The result screen also lists the `customer_secrets.yml` entries that a real submission of that snippet would need, one per secret placeholder, for example:

```
TF_VAR_davinci_variable_apiToken_company_value: apps/pingone-davinci/${VAULT_PATH_ENV}/pingone/davinci_variable_apiToken_company_value@secret
```

These are key names only. Check them against the pipeline's conventions; there is no need to send them.

## How to read PASS / WARN / FAIL

The verdict is the worst result among the checks. SKIP does not count.

| Check | PASS | WARN | FAIL |
| --- | --- | --- | --- |
| `total_time`: start screen to result | ≤ 10 s + 2.5 s per flow (**60 s for 20 flows**) | up to twice that | more |
| `flow_timeout_margin`: run time / `fsFlowTimeoutSeconds` | < 50% | < 80% | ≥ 80% |
| `http_timeout_margin`: slowest call / `fsHttpTimeoutSeconds` | < 50% | < 80% | ≥ 80% |
| `browser_request`: longest time between two screens | ≤ 20 s | ≤ 40 s | more |
| `function_time`: slowest custom-function run | ≤ 1 s | ≤ 3 s | more |
| `response_size`: largest single API response | ≤ 1 MB | ≤ 5 MB | more |
| `carried_state`: largest flow-instance variable | ≤ 2 MB | ≤ 8 MB | more |
| `variable_integrity`: tfvars read back intact from its variable | intact | | changed |
| `coverage`: flows fetched and translated / requested | all | | any missing |
| `translation`: unresolved IDs, name collisions, tier problems | none | any | |
| `stage_*`: a stage failed, did not finish, or was refused | | refused, or errors it could skip | failed |
| `gitlab_write` | committed | | (failure shows as `stage_gitlab`) |

How to read the results:

- **PASS at 20 flows** is evidence that DaVinci can do the job.
- **FAIL on time or timeout margin at 20 flows** is evidence that it cannot.
- **WARN** means it works but with no headroom. Compare `calibration.avgIterationMs`, `stages[].maxCallMs` and `totals.handbackMs` to see where the time goes: DaVinci's own node overhead, the API, or browser round trips.

## Known DaVinci limits to watch

- **Flow timeout.** It covers the whole run (the tenant default is usually 300 s). The prior iteration ended with `TIMEOUT` in Analytics at the default. The import does not carry timeout settings.
- **One browser request.** Everything between two screens runs in one request. The prior iteration timed out in the browser after many REST calls in one request, with nothing in Analytics. The hand-back setting splits the work. Run 6 measures the limit without it.
- **Custom functions.** The limits on run time, code size (each function here is 11–37 KB, mostly the embedded shared library) and available APIs are not documented to us. The functions use only plain JavaScript: no `fetch`, `Buffer`, `TextEncoder` or `URL`.
- **Payload size.** The prior iteration failed with DaVinci's generic error page, and nothing logged, when whole snapshots (tens of MB) passed through the flow. This test keeps raw flow bodies out of the state: each flow is translated as it arrives and only the fragment is carried. Full flow lists are also avoided (`?attributes=id,name`): pingcli-terraformer notes that a full list can exceed a 10 MB gateway limit and return HTTP 500.
- **Loops.** Paging and the bulk fetch loop back through an evaluator to an earlier REST node. DaVinci has no `for` construct.
- **Variable size.** The flow-instance variables declare `max: 2000`, as Ping's own exports do. `variable_integrity` fails if DaVinci truncates the snippet.

## What we learned from the prior iteration

The previous branch content is in Git history before the cleanup commit. Its notes and commit messages show three causes of "too slow":

1. **Configuration Management snapshots take minutes.** The flow took a live snapshot of each environment and polled until it was COMPLETE. That alone exceeded DaVinci's default 300 s flow timeout ("TIMEOUT … in the snapshot subflow"); the fix was a 3600 s timeout set by hand.
2. **Too many REST calls in one browser request.** Snapshot polling plus page reads came to about 118 calls in one request, and the browser timed out with nothing in Analytics. The fix was self-submitting "working" pages every 5 pages.
3. **Too much data through the flow.** Whole snapshots of a large environment (80+ flows, 150 variables, tens of MB) ended on DaVinci's generic error page. The fix kept only names, IDs and types.

This test drops snapshots entirely. It uses the DaVinci admin API directly, as the hosted design does, which is the question we need answered.

## Assumptions to confirm in a live tenant

These were taken from the PingOne Go SDK (`pingone-go-client` v0.13.0), the pingcli-terraformer code and the prior iteration. None of them has been run against a live tenant from this flow. Every endpoint path is a flow variable, so you can correct it without regenerating.

1. **Endpoints.** These paths are under `https://api.pingone.<tld>` and taken from the SDK:
   - `GET /v1/environments/{envId}/flows?attributes=id,name`; the `attributes` parameter is used by pingcli-terraformer;
   - `GET /v1/environments/{envId}/flows/{flowId}`;
   - `GET /v1/environments/{envId}/connectorInstances`;
   - `GET /v1/environments/{envId}/variables`.

   Each response is expected to hold `_embedded.flows`, `_embedded.connectorInstances` or `_embedded.variables`. In the SDK only the variables list has a `_links.next`; the flow follows `next` on every list anyway.
2. **The DaVinci Admin Read Only role is enough** for all four reads, including connector instance properties. Secrets are expected to come back masked as `******`.
3. **The token.** The worker app uses client secret **basic** authentication. The token URL is `https://auth.pingone.<tld>/<workerEnvId>/as/token`.
4. **REST node behaviour** (as the prior iteration found):
   - the body is stored as `httpBody: raw` plus a plain-text `raw`;
   - a non-2xx response takes the evaluator's false branch with `rawResponse.statusCode` and `body` still set;
   - redirects are not followed silently.
5. **Earlier node outputs survive a screen.** Later REST nodes read the token from `{{local.<token node>.payload.output.rawResponse.body.access_token}}`, after "working" pages.
6. **Flow-instance variables.** The flow saves state with **Save Value** (`saveValue` / `saveVariables`) and reads it as `{{global.variables.<name>}}`, the shape Ping's own exports use. The prior iteration used flow variables instead. Object variables are expected to keep their structure, and large strings to stay intact (`variable_integrity` checks this).
7. **Screens.**
   - form fields are captured by `formFieldsList` and button values as `buttonValue`;
   - placeholders, not prefilled values, are used, so empty means default;
   - the CSS and Script fields apply; `<style>` and `<script>` inside the HTML do not.
8. **No flow `trigger` in the export.** The flow is launched by a DaVinci application policy, like the prior iteration, so the export has no `trigger`. `gen_flow.py --trigger AUTHENTICATION` adds one, as Ping's main-flow exports carry, if your launch method needs it.
9. **The clock.** `Date.now()` is consistent across custom-function runs within one flow execution. If functions run on different hosts, small timing skew is possible.
10. **GitLab.**
    - A Developer token can read project, branch and file metadata and create commits and MRs. If it cannot list protected branches (403), only the name checks guard the branch, and the result screen notes this.
    - The Commits API creates `branch` from `start_branch` when the branch is new. The flow checks that it does not exist first; nothing sets `force`.
11. **The `customer_secrets.yml` layout** shown on the result screen is the format described to us, and has not been checked against the pipeline.
12. **The translation is an approximation of the hosted translator, made to measure cost.** It handles graph nodes, edges, settings, trigger, input and output schema, connections and variables.
    - It drops unknown and object-valued settings and counts them as `droppedSettings`.
    - It does not write `import_ids`, so no IDs are emitted.
    - It writes `pingone_environment_id = ""` for the pipeline to fill.
    - A secret-named literal inside rich text is replaced as a whole value.

## Cleanup

1. In GitLab, delete every `davinci-feasibility/…` branch and close any draft MR. Then **revoke the project token**, or delete the test project.
2. In PingOne, delete the worker app's secret or the worker app itself.
3. In DaVinci:
   - delete the flow and its application or flow policy;
   - delete the company variables `fsWorkerBasicAuth` and `fsGitlabToken`;
   - set logging back to Info if you changed it.

## Rebuild and check

You need Python 3 and Node 18+. No tenant is needed.

```sh
cd davinci-feasibility
python3 gen_flow.py          # fn/*.js -> flow/pingone-promote-feasibility.flow.json
python3 check_flow.py        # structure, wiring, and an ID/secret scan of the whole repo
node test_functions.js       # unit tests plus the exported graph run against fake PingOne and GitLab
```

- `gen_flow.py --values values.json --out out/private.flow.json` writes a private copy with your DaVinci connection IDs and non-identifying presets. Start from `values.example.json`; `values.json` and `out/` are git-ignored. Check that copy with `check_flow.py out/private.flow.json --allow-connection-ids`.
- Environment IDs, the project ID and secrets cannot be preset in either copy; they are set only in DaVinci.

| File | Purpose |
| --- | --- |
| `davinci-feasibility/fn/_lib.js` | Shared helpers: state, timing, redaction, URL and branch checks |
| `davinci-feasibility/fn/_translate.js` | Logical keys, placeholders, secrets, tiers, HCL rendering |
| `davinci-feasibility/fn/<step>.js` | One custom function each (settings, start, calibrate, auth, inventory, resume, fetch, assemble, gitlab_*, finish) |
| `davinci-feasibility/gen_flow.py` | Builds the export from the sources |
| `davinci-feasibility/check_flow.py` | Validator |
| `davinci-feasibility/test_functions.js` | Node tests |

The export format was checked against the prior iteration's live-imported export and against real DaVinci exports in [pingcli-plugin-terraformer](https://github.com/pingidentity/pingcli-plugin-terraformer) `tests/testdata/flows` (Apache-2.0). No files were copied from either.
