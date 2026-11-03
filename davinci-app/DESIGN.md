# PingOne Promote in DaVinci: design

The DaVinci-hosted front end of pingone-promote. It is the primary front end. It stays primary until it is shown to be a poor home for the tool.

DaVinci does **light work only**:
- the PingOne SSO sign-in;
- the forms;
- one flow-list read;
- one commit of a small request file;
- status reads.

Export, neutralizing, the additive block-level merge (#93), `terraform fmt` and the merge request are done by the GitLab request job, built by another engineer. No export or translation logic is ported into DaVinci JavaScript.

```
Person ─ PingOne SSO ─▶ DaVinci app flow
  1 source env + main flow search ── PingOne: token + GET flows?attributes=id,name (read-only worker)
  2 options, readiness
  3 Preview ── POST /repository/commits  branch pingone-promote/<requestId>  (.pingone-promote/request.json, dryRun:true)
               branch push pipeline ▶ .pre job pingone-promote:request ▶ result.json artifact (writes nothing)
  4 Submit  ── POST /repository/commits  same branch, update request.json (dryRun:false, last_commit_id)
               job ▶ commits the generated change, deletes request.json, opens the MR (mode mr) or leaves the branch
  5 Rollout ── MR state ▶ post-merge pipeline on main ▶ jobs per env ▶ GitLab Deployments API per env
  Cancel   ── DELETE /repository/branches/pingone-promote/<requestId>, only while its head is the request commit
```

Files:

| File | What it is |
| --- | --- |
| `fn/_contract.js` | **The contract with the request job, in one place**: branch prefix, file path, job name, artifact name, request fields, commit body, result normalisation, typed-error copy. |
| `fn/_lib.js` | Helpers: parsing, escaping, redaction, base64, settings check, the **GitLab URL allow-list** (`lib.gl`), the read queue, search, and the Preview/Submit rules. |
| `fn/<step>.js` | One custom function each: `signin`, `render`, `handle`, `p1_token`, `p1_list`, `gl_commit`, `gl_read`, `gl_delete`. |
| `gen_app.py` | Builds `flow/pingone-promote-app.flow.json` from the sources. The output is deterministic. |
| `check_app.py` | Validator for wiring, the trust boundary and a repo-wide secret and ID scan. `--self-test` checks that broken copies of the export are refused. |
| `test_app.js` | Runs the exported graph against a fake PingOne and a fake GitLab, with a simulated person clicking through the screens. |

## 1. Screens

There is one **App screen** node. Its HTML comes from **Render screen**, which picks one of the screens below from the state. Every screen has the same header and navigation: New request, Requests, "Signed in as …", Leave the app.

Each screen is a form with two fields:
- `buttonValue`: which button was pressed;
- `ppForm`: a JSON object that the screen's script builds from every control marked `data-pp`. This is because DaVinci captures only the form fields that are declared. The dynamic env-value and secret rows cannot be declared ahead of time, so they travel inside this one field.

### 1.0 Sign in (the operator's subflow) and the stop screens
```
┌ PingOne sign-on (your tenant's sign-in subflow: password / MFA / existing session) ┐
└──────────────────────────────────────────────────────────────────────────────────────┘
        │ userId, username, name, groups
        ▼
┌ PingOne Promote ─────────────────────────────────────────────────────────┐
│ You do not have access to PingOne Promote                                │   "Cannot continue" (end)
│ • You are signed in as pat, but you are not in a group that may use this │   also: "Sign-in required",
│   app. Ask your PingOne administrator for access.                        │   "not set up yet" (lists
└──────────────────────────────────────────────────────────────────────────┘    setting NAMES, never values)
```

### 1.1 New request: Source env and flow search
```
┌ PingOne Promote ─────────────────────────────────────────────────────────────────┐
│ [New request] [Requests]                         Signed in as Pat   [Leave the app] │
├────────────────────────────────────────────────────────────────────────────────────┤
│ New request                                                                        │
│ Pick the source env and the main flow to bring into Git. Its shared definitions    │
│ and subflows come with it; the rollout then carries the same commit through        │
│ qa → stage → prod.                                                                 │
│ Source env          [ dev        ▾]                                                │
│ Main flow name or ID [ login                    ]   [Search flows]                 │
│                                                                                    │
│ 2 matches. Choose one; the app never picks for you                                 │
│ ┌───────────────┬──────────────────────────────────┬──────────────────┐            │
│ │ Main flow     │ Flow ID                          │                  │            │
│ │ Login         │ 3f…                              │ [Use this flow]  │            │
│ │ Login - MFA   │ 9a…                              │ [Use this flow]  │            │
│ └───────────────┴──────────────────────────────────┴──────────────────┘            │
└────────────────────────────────────────────────────────────────────────────────────┘
 empty: "No flow in dev matches "zzz". Try part of the name, or the flow ID."
 empty env: "... (the env has no flows the app can read; ask your DaVinci administrator to check the worker's role)"
```
- Search follows #93: a case-insensitive substring of the name, or an exact flow ID.
- One match shows a single primary **Use this flow** button. Several matches are all listed, and the app never picks one.
- The list (`id,name` only) is read once per source env per session. Later searches in that env filter it in DaVinci and make no calls.

### 1.2 Review the change (options and readiness)
```
│ Review the change                                                                  │
│ Source env dev · Flow Registration · Request 20261103-140501-k3j9q2 · qa → stage → prod │
│ (i) No preview yet. Preview runs the request job without writing anything ...      │
│ How to hand the change to GitLab  (•) Open a merge request  ( ) Branch only        │
│ Request branch: pingone-promote/20261103-140501-k3j9q2                             │
│ Title [PingOne Promote: Registration from dev ]   Commit message [ ... ]          │
│ Description (optional) [                      ]                                    │
│ Shared definitions                                                                 │
│   pingone_davinci_connector_instance.http  (•) Keep Git version  ( ) Use source   │
│ Readiness                                                                          │
│   qa    ✕ 1 missing   qa: base_url [https://qa.example.com      ]                  │
│   stage ✕ 1 missing   stage: base_url [                         ]                  │
│   prod  ✓ Ready                                                                    │
│ Secrets  davinci_variable_PingFedAuthNCreds_value                                  │
│   Variable name [davinci_variable_..._value]  Vault field [pingfed_authn_creds]   │
│   [x] It is in Vault for qa, stage, prod        (not verifiable here)              │
│ [Preview]  [Submit request] Readiness: 2 environment(s) are missing values.        │
│            Changing an option turns Submit back on.        [Cancel request]        │
│ [Back to flow search]                                                              │
```
- The Shared definitions, Readiness and Secrets rows come from the **last preview's** `result.json`. Before the first preview, the screen explains that previewing lists them.
- Defaults follow #93:
  - the selected flow is always updated, and has no choice;
  - a new dependency is added;
  - a dependency that differs defaults to **Keep Git version**.
- Env values are asked for every rollout env except the source. A value that looks like a credential is refused with "Secrets come from Vault".
- The secret mapping sends names only (`varName`, `field`), never values. The Vault tick is an operator checklist, required before Submit.

### 1.3 Working (Preview running / Submit running)
```
│ Submit running                                                                     │
│ The request job writes the change to the request branch and opens a merge request. │
│ (◌) The job is running (1m 05s)                         ← aria-live="polite"       │
│ Checks again by itself in 10 seconds.   [Check now]                                │
│ Open the pipeline · Open the job                                                   │
│ [Stop waiting]                                                                     │
 slow:    "GitLab has not started a pipeline for the request after 2m 10s. GitLab may be busy,
           or CI may be off for request branches."  /  "No runner has picked up the job yet."
 timeout: "GitLab is slow. The app stopped checking after 10m 0s. The outcome of your submit is
           unknown until GitLab answers."  [Keep waiting] [Check GitLab]   (no automatic checks)
```

### 1.4 Preview
```
│ Preview: ready        Result of the request job, without writing anything. Open the job │
│ Predicted changes                                                                  │
│ 1 new, 1 changed in source, 1 differs; git version kept, 1 matches git.            │
│ ┌──────────────────────────────────────────────┬──────────────────────────────┐    │
│ │ Shared definition (address)                  │ Predicted change             │    │
│ │ pingone_davinci_flow.login  (selected flow)  │ ~ Changed in source          │    │
│ │ pingone_davinci_flow.mfa_sub                 │ + New                        │    │
│ │ pingone_davinci_connector_instance.http      │ ! Differs; Git version kept  │    │
│ └──────────────────────────────────────────────┴──────────────────────────────┘    │
│ ▸ 1 matching Git                                                                   │
│ Review the change in GitLab (diffUrl)                                              │
│ Readiness  qa ✓ Ready · stage ✓ Ready · prod ✓ Ready                               │
│ Secrets    davinci_variable_..._value  ✓ Confirmed in Vault                        │
│ [Preview] [Submit request]  [Cancel request]   [Edit options]                     │
```
The symbol is never the only cue: each one sits next to its label, and the symbols are `aria-hidden`.

### 1.5 Request submitted
```
│ Request submitted                                                                  │
│ The merge request is open. The rollout starts after it is merged.                  │
│ Open merge request ↗          (branch mode: "Open the branch ↗")                    │
│ Changes written  (table as in Preview)                                             │
│ [Watch the rollout]  [New request]                                                 │
```

### 1.6 Rollout (request status)
```
│ Rollout: PingOne Promote: Registration from dev                                    │
│ Merged. The pipeline on main carries the same commit through each environment.     │
│ Open merge request ↗ · Open the pipeline ↗                                         │
│ ┌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌┐ ┌─────────────┐ ┌─────────────────┐ ┌─────────────┐             │
│ ╎ dev             ╎ │ qa          │ │ stage           │ │ prod        │             │
│ ╎ Source env      ╎ │ ✓ Live      │ │ ⏸ Needs approval │ │ ○ Waiting   │             │
│ ╎ (pipeline plan  ╎ │             │ │ 1 of 2 approvals│ │             │             │
│ ╎  job only)      ╎ └─────────────┘ └─────────────────┘ └─────────────┘             │
│ └╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌┘                                                                 │
│ Last checked 09:41:30 UTC. Checks again every 30 seconds. [Pause automatic checks]  │
│ [Check now] [All requests]                                                          │
```
States: **Live**, Live via !N (a newer commit that contains ours), **Needs approval** (a protected-environment approval, or a manual job nobody has run yet), Running, **Failed**, Canceled, **Waiting** (with "waits for the merge", "branch only: no merge request yet", or "the pipeline has not started").

### 1.7 Requests
```
│ Requests                     Requests the app has made, newest first.             │
│ Merge requests                                                                     │
│  !41  PingOne Promote: Registration…  pat.operator  merged  2026-11-03 09:10  [Rollout] │
│ Request branches without a merge request                                           │
│  pingone-promote/2026…-k3j9q2  Previewed, not submitted  …  [Cancel request]       │
│  pingone-promote/2026…-x81bq0  Branch only               …  [Rollout]              │
 empty: "No merge requests with the pingone-promote label yet."
```

## 2. State machine

### 2.1 Screens and buttons
```
                 ┌──────────── NAV_NEW (any screen) ─────────────┐
 sign-in ─▶ signin ─(denied / settings)─▶ Cannot continue (end)  │
               │ok                                               ▼
               └──────────────▶ search ──SEARCH──▶ search (list read once per env)
                                  │PICK:n (new requestId, branch name, default options)
                                  ▼
            ┌─────────────── options ◀──EDIT── preview ◀─────────────┐
            │ PREVIEW / SUBMIT │  ▲  CHECK / CANCEL                  │ result (dryRun)
            ▼                  │  │                                  │ or typed error on submit
         [commit] ─2xx─▶ working ─POLL (auto)─▶ working ... ─result─┘
            │4xx: error (known)  │STOP (submit ⇒ unknown)            └─result (submit ok)─▶ submitted
            │5xx/none: unknown   │timeout ⇒ auto off (submit ⇒ unknown) KEEP / CHECK             │WATCH
            ▼                                                                                   ▼
          options (banner)                                     requests ◀─NAV_REQUESTS─▶ status (REFRESH auto 30s,
                                                                 │OPEN:n / OPENB:n ──────────▶  PAUSE / RESUME)
                                                                 │CANCELB:n ─▶ [GET branch ─▶ DELETE] ─▶ requests
 any screen ──SIGNOUT──▶ Left the app (end)
```

### 2.2 Submit state (`s.sub.state`): the #57 rule

| State | Entered when | Preview | Submit | Leaves by |
| --- | --- | --- | --- | --- |
| `idle` | new request; an edit after `error`; Check GitLab says "not written" | on | on, unless the current preview shows blockers, an error or no change, or a secret is not ticked | Submit |
| `running` | Submit commit accepted | off | off | result, timeout, Stop |
| `error` | a typed error in `result.json`, or GitLab refused the commit (4xx) | on | **off for these exact options**, with the reason | **any edit** of the options (the screen script turns the button back on; the server checks again) |
| `unknown` | 5xx or no answer on the commit; job ended without `result.json`; timeout; Stop waiting during Submit | off | off | **Check GitLab** only |
| `done` | `result.json` without an error; or Check GitLab found the MR or branch | off | off | New request |

The server always enforces this table: `lib.canPreview` and `lib.canSubmit` run again in **Handle input**, so a forced click is refused with the same reason. "Options" here means mode, title, commit message, description, dependency choices, env values and secret mappings, compared as one key (`lib.optionsKey`).

### 2.3 Check GitLab (resolving `unknown`), at most 3 reads
1. `GET files/.pingone-promote%2Frequest.json?ref=<branch>`
   - If it holds exactly what was last sent, that commit landed: the app sets `lastCommit` from `last_commit_id`, then finds the pipeline by ref and sha and resumes waiting.
   - If it holds an older request, the last write did not land. The state goes back to `idle` ("Nothing changed; you can try again").
2. If the file is missing, `GET branches/<branch>`:
   - The branch is missing:
     - if nothing was ever written, "nothing was written";
     - if earlier commits existed, "the branch no longer exists" and a new request is needed.
   - The branch exists but the file is gone: the job took the Submit. Go to step 3.
3. `GET merge_requests?source_branch=<branch>`:
   - an MR is found: `done`, with its link;
   - mode is branch: `done`, with the branch link;
   - otherwise: "the job has not opened the merge request yet; check again".

### 2.4 Cancel
- **Handle input** reads the branch (`GET branches/<branch>`).
- The branch is deleted only if both of these hold:
  - its head commit title is `pingone-promote: (preview|submit) request <requestId>`;
  - in the request flow, its head sha is the app's own `lastCommit`.
- Otherwise the app refuses: "the request job (or someone) has already committed …; close it in GitLab instead".
- From Requests, a branch whose head is a request commit can be canceled the same way.

## 3. The graph

```
[Sign in with PingOne SSO (your sign-in subflow)]            startUiSubFlow, the one start node
   → [Check sign-in and settings] fn signin ──stop──▶ [Cannot continue] (end screen)
   │ ok (keep state; ppFlows={}, ppBody="")
   ▼
[Render screen] fn render → keep state → [App screen] (buttonValue, ppForm) → [Handle input] fn handle
   ▲  │ keep state (ppState, ppUrl, ppBody), then branch on action:
   │  ├─ token  → REST PingOne: worker token (POST)      → fn p1_token  ─list→ REST PingOne: list flows (GET)
   │  │                                                                         → fn p1_list ─list (next page)→ ↺
   │  ├─ commit → REST GitLab: commit request file (POST) → fn gl_commit
   │  ├─ read   → REST GitLab: read (GET)                 → fn gl_read  ─read→ ↺ (queue)  ─delete→
   │  │                                                     REST GitLab: delete request branch (DELETE) → fn gl_delete
   │  ├─ signout → [Left the app] (end screen)
   │  └─ render (default)
   └────────── every handler routes back to Render screen
```
- 76 nodes: 39 steps, 37 evaluators.
- There are 5 REST nodes, 8 custom functions, 2 screens that continue (App screen, and Render's save) and 2 end screens.
- Every REST call's false branch goes through a "call failed, continue" save into the same handler, which reads the status code. This is the feasibility pattern.
- The URL of every REST node is `{{global.variables.ppUrl}}`. A function builds it, and only through `lib.gl`, the allow-list of GitLab endpoints, or the PingOne URL builders.

**Why one main flow.** All state lives in flow-instance variables:
- `ppState`: about 3 to 10 KB;
- `ppFlows`: the id and name list of one env;
- `ppUrl` and `ppBody`: the next call.

Splitting the flow into subflows would mean passing that state across subflow boundaries on every hop. That adds DaVinci behaviour we have not confirmed, and buys nothing. The only subflow is the sign-in, which is the operator's own flow.

### Sign-in subflow contract

The main flow's first node is a **Flow Conductor → Start UI Subflow** node. Point it at your tenant's PingOne SSO sign-in subflow, for example the PingOne sign-on template that your other DaVinci apps already use, with password, MFA and existing-session reuse.

The subflow must finish only for an authenticated user, and must return these outputs:

| Output | Used for |
| --- | --- |
| `userId` | must be non-empty, or nothing runs |
| `username` | `requestedBy` in `request.json`, and `Requested-by:` in the commit message |
| `name` | "Signed in as …" |
| `groups` | an array of names, or comma-separated text; checked against `ppAllowedGroups` (`*` admits every signed-in user) |

**Check sign-in and settings** reads these from `{{local.<subflow node>.payload.output.<field>}}`. If your subflow returns them under other names, rewire that function's four inputs. Nothing else in the flow reads the identity.

## 4. Trust boundary

| Lives in DaVinci (company secret variables) | Why | It can | It cannot |
| --- | --- | --- | --- |
| `ppWorkerBasicAuth`: base64(clientId:clientSecret) of a **read-only DaVinci worker** (role: DaVinci Admin Read Only on the source envs) | Flow search | read flow names and IDs | change anything in PingOne |
| `ppGitlabToken`: **one project access token** on the config repo, **Developer** role, `api` scope | Commit the request file, read status, delete a request branch | push to unprotected branches, read the API, open MRs | push to protected `main`, approve or merge MRs on its own (with approval rules), run jobs for protected environments |

The **safety boundary is GitLab, not DaVinci**:
- protected `main` with required MR approval;
- the per-env manual jobs and protected-environment approvals in the enterprise pipeline;
- the PingOne credentials that **write**, which live only in GitLab CI variables, are protected, scoped to protected refs and environments, and never in DaVinci.

The request job's read-only export credentials are also GitLab CI variables.

What the app itself guarantees (enforced by `check_app.py` and `test_app.js`):
- Each secret is referenced only in the header of the REST nodes that need it:
  - the worker credential goes only to the PingOne token call;
  - the bearer token goes only to the PingOne list call;
  - the GitLab token goes only to the three GitLab calls.
- No function input, screen, saved value or request body references a secret or the access token.
- PingOne: one token POST, every other call a GET, and only the flow list (`attributes=id,name`).
- GitLab: the only write is `POST /repository/commits`. Its body is built by `contract.commitBody`:
  - one action;
  - the file `.pingone-promote/request.json`;
  - branch `pingone-promote/<requestId>`;
  - `start_branch` set to the default branch on create, `last_commit_id` on update.
- The only delete is `DELETE /repository/branches/pingone-promote/<requestId>`, after the head check.
- Every other GitLab call is a GET from the `lib.gl` allow-list, so there is no `play`, `retry`, `merge` or `approve` endpoint.
- Screens:
  - every dynamic value goes through `lib.esc`, which also escapes `{` and `}`, so text from users or APIs can never form a `{{reference}}`;
  - links are shown only when they start with the configured GitLab web origin;
  - environment IDs, tokens and setting values are never rendered.
- `request.json` travels as base64 (`encoding: base64`), so user text never sits as raw text in the REST body template.
- API error text is passed through `lib.redact`, which removes tokens, JWTs, UUIDs and `x=secret` pairs.

Residual risks, and what the operator should do about them:
- **A Developer `api` token can do more than the app does.** Keep it in a project, not a group, and give it the shortest practical expiry. Protect every rollout environment, and let only Maintainers or a release group run its jobs. Require MR approval on `main`.
- **Request branches are unprotected, and their pipelines run whatever `.gitlab-ci.yml` the branch has.** Anyone who can push to them can change what the `.pre` job runs.
  - Scope the request job's read-only PingOne export credentials so they are as harmless as possible.
  - Optionally protect `pingone-promote/*`, allowing push only for the project token's bot user and Maintainers.
  - Make protected variables available only to protected refs.
- `requestedBy` is asserted by DaVinci after SSO. GitLab sees the token's bot as the author. Trust in the field rests on the token living only in DaVinci. See ask 4 for a signed form.

### DaVinci variables and connections

**Flow variables** (non-secret; the export ships the defaults, and the ID and group settings empty):

| Variable | Default | Meaning |
| --- | --- | --- |
| `ppApiOrigin` / `ppAuthOrigin` | `https://api.pingone.com` / `https://auth.pingone.com` | PingOne region |
| `ppWorkerEnvironmentId` | *(empty)* | the env of the read-only worker app |
| `ppSourceEnvs` | *(empty)* | the envs a person may pick as the source, as `dev=<envId>, qa=<envId>`. Usually just `dev`. |
| `ppPathFlows` / `ppFlowsListQuery` | `/v1/environments/{envId}/flows` / `attributes=id,name` | the flow-list call |
| `ppRolloutEnvs` | `qa,stage,prod` | the rollout order shown on the strip; env values are asked for each |
| `ppGitlabApi` / `ppGitlabProjectId` | `https://gitlab.com/api/v4` / *(empty)* | the config repo |
| `ppDefaultBranch` | `main` | `start_branch`, and the post-merge pipeline ref |
| `ppApplyJobPattern` / `ppEnvironmentPattern` | `apply:{env}` / `{env}` | how a rollout env maps to its job name and GitLab environment name |
| `ppMrLabel` | `pingone-promote` | the label the Requests list filters on |
| `ppAllowedGroups` | *(empty: must be set)* | PingOne groups that may use the app, or `*` |
| `ppRunTimeoutSeconds` / `ppPipelineAppearSeconds` | `600` / `120` | when to stop checking a job; when to say "no pipeline yet" |
| `ppStatusRefreshSeconds` / `ppStatusMaxMinutes` | `30` / `30` | rollout auto-refresh interval, and how long it runs before pausing |
| `ppChildPipelines` | `yes` | look in child pipelines (bridges) when an env's job is not in the parent |

**Company secrets:** `ppWorkerBasicAuth` and `ppGitlabToken`.

**Flow-instance variables:** `ppState`, `ppFlows`, `ppUrl`, `ppBody` and `ppLastFailed`.

**Connections:**

| Connector | Used by |
| --- | --- |
| Http | REST calls and screens |
| Functions | custom functions and A = B |
| Variables | Save Value |
| Flow Conductor | the sign-in subflow call |

The export carries placeholder connection IDs, as the feasibility flow does. Point them at your connections after import, or generate a private copy with `--values`.

## 5. The contract (provisional, in `fn/_contract.js` only)

- **Branch:** `pingone-promote/<requestId>`, where `requestId` is `YYYYMMDD-HHMMSS-xxxxxx` (UTC and 6 random base-36 characters). It is safe in branch names and carries no tenant data.
- **File:** `.pingone-promote/request.json`. It has exactly these fields, in this order:
  - `requestVersion: 1`, `requestId`, `requestedBy`, `source`;
  - `flows: [{id, name}]`;
  - `dependencyChoices: {<address>: "update"|"keep"}`;
  - `envValues: {<env>: {<var>: <value>}}`;
  - `secretMappings: {<key>: {varName, field}}`;
  - `mode: "mr"|"branch"`;
  - `branchName` (the request branch);
  - `commitMessage`, `title`, `description`, `dryRun`.
- **Preview:**
  - the first Preview runs `POST /repository/commits` with `{branch, start_branch: <default>, commit_message, actions: [{action: "create", file_path, encoding: "base64", content}]}`;
  - later Previews and the Submit send the same call with `action: "update"` and `last_commit_id: <the app's last request commit>`.
- **Submit:** the same as a later Preview, with `dryRun: false`.
- **Commit message:** `pingone-promote: preview|submit request <id>`, followed by the trailers `Requested-by: <username>` and `Request-id: <id>`.
- **Finding the job:**
  1. `GET /pipelines?ref=<branch>&sha=<commit>`;
  2. `GET /pipelines/:id/jobs`;
  3. the job named `pingone-promote:request` (the highest id, if retried);
  4. when that job is `success`, `failed` or `canceled`, `GET /jobs/:id/artifacts/result.json`.
- **`result.json`:**
  - `status`;
  - `mrUrl` or `branchUrl`;
  - `changes: [{address, action: added|updated|kept_differs|unchanged}]`;
  - `requiredSecrets`;
  - `blockers: {<env>: [...]}`;
  - `error: {code: conflict|preflight|partial|blocked, message}`.
  - The app also accepts and uses `diffUrl`, `changes[].role` and structured blockers `{code, var, message}`; see the asks in section 11. Unknown actions or codes stay visible as problems; they are never dropped.
- **Status:**
  1. `GET /merge_requests/:iid`, or by `source_branch` in branch mode;
  2. `merge_commit_sha` (or `squash_commit_sha`);
  3. `GET /pipelines?ref=<default>&sha=<merge sha>` (the post-merge pipeline, never the MR head pipeline);
  4. its jobs, plus `GET /pipelines/:id/bridges` and the child pipelines' jobs, when an env's job is missing;
  5. per env, only when the job alone cannot settle the state (`manual`, `canceled`, `skipped`, `created`, or missing in a finished pipeline): `GET /deployments?environment=<env>`;
  6. for a `blocked` deployment: `GET /deployments/:id`, for "n of m approvals";
  7. for a newer successful deployment: `GET /repository/merge_base` (does it contain ours?), then `GET /repository/commits/:sha/merge_requests`, giving "Live via !N".

## 6. Performance budgets

The prior iteration was slow because of three things. This design removes each one:
- native snapshot polling;
- about 118 REST calls in one browser request;
- tens of MB of payload.

Every wait happens **in the browser**: the screen's script submits the auto button after a timer. DaVinci never sleeps and never loops on a timer.

REST calls per browser request (one screen submission). The "measured" column is the highest count over all test scenarios; `test_app.js` asserts each budget on every request of every scenario.

| Browser request | REST calls (budget) | Measured max | Payload |
| --- | --- | --- | --- |
| Sign-in, settings check, first screen | 0 | 0 | — |
| Search, first time for a source env | 1 token + 1 per 100-flow page (budget 2 + pages; capped at 20 pages) | 4 (3 test pages) | `id,name` only: about 100 bytes per flow |
| Search again in the same env | 0 | 0 | — |
| Use this flow, Edit, Back, Pause | 0 | 0 | — |
| Preview / Submit | 1 (the commit) | 1 | request.json, 1 to 5 KB |
| Working, automatic check | ≤ 3: pipelines by ref and sha → jobs → result.json | 2 | jobs page ≤ 100 jobs; result.json |
| Check GitLab | ≤ 3 (file → branch → MRs), then the working check | 3 | small |
| Cancel | 2 (branch, then delete) | 2 | small |
| Requests | 2 (MRs by label, request branches) | 2 | ≤ 20 rows each |
| Rollout, each check | 3 base (MR → pipeline → jobs) + bridges/child jobs + ≤ 2 per unsettled env; hard cap `lib.MAX_CALLS` = 10 | 9 (with a child pipeline and "Live via !N") | jobs ≤ 100; deployments 5 per env |

Other budgets:
- The flow-instance state stays under about 20 KB: no flow bodies, no diffs, no job logs.
- Each function returns in milliseconds; there is no JSON larger than one GitLab page.
- One browser request makes at most 10 GitLab reads. Above the cap, the screen says "some details were not checked this time" and the next check carries on.
- Export size: about 400 KB, because each of the 8 functions embeds the contract and the helpers, as the feasibility flow did.

## 7. Polling and timeouts

| What | When | Stops |
| --- | --- | --- |
| Request job (Working) | first check after 3 s, then 5 s, then every 10 s | when `result.json` is read; at `ppRunTimeoutSeconds` (600 s), when it turns off and offers Keep waiting / Check GitLab; on Stop waiting |
| "No pipeline yet" note | after `ppPipelineAppearSeconds` (120 s) with no pipeline for ref and sha | when one appears |
| "Waiting for a runner" note | job `pending` or `created` for longer than 120 s | when it starts |
| Rollout | every `ppStatusRefreshSeconds` (30 s) while the MR is open, the post-merge pipeline is active, or any env is Running or Needs approval | when the pipeline finished with nothing waiting ("no more automatic checks"); after `ppStatusMaxMinutes` (30), when the person can resume; on Pause |
| Hidden tab | timers do not fire while `document.hidden`; the check runs when the tab is visible again | — |

DaVinci limits:
- Every REST node keeps the tenant's HTTP timeout (the export records 15 s).
- The flow timeout recorded in the export is 1800 s. Each screen hand-back starts a new browser request, so no request runs long. Section 12 lists what to verify about the flow-level timeout.

## 8. When GitLab is slow
- **Commit slow or failing.** No answer, or a 5xx, means `unknown`: Preview and Submit go off, and Check GitLab appears. The app never retries a write by itself.
- **Pipeline does not appear.** After 2 minutes the app explains that GitLab may be busy, or CI may be off for request branches. After 10 minutes it stops checking. Keep waiting restarts the clock. Check GitLab re-reads the request file, and resumes if the commit is there.
- **Runner queue.** The app shows "The job is waiting for a runner" and its age. It keeps checking until the timeout.
- **Reads slow.** The read cap keeps one request short: the next automatic check continues. A non-2xx read shows "GitLab did not answer as expected (HTTP n)" without changing the request's state.
- **Rollout.** The app shows the last checked time, Check now, and Pause automatic checks.

## 9. Errors and empty states

| Situation | What the person sees | Next |
| --- | --- | --- |
| No identity from sign-in / not in an allowed group / settings incomplete | "Sign-in required" / "You do not have access …" / "not set up yet", listing variable names and issues (never values) | end screen |
| Worker token refused | "PingOne did not let the app read flows (HTTP 401) …" (redacted) | fix the worker; search again |
| No flow matches / env has no readable flows | an empty-state sentence with a hint | refine the search |
| Several matches | the table of all matches; "the app never picks for you" | Use this flow |
| Field errors | an error summary at the top (`role=alert`, focused), with each field marked `aria-invalid` | fix them |
| Preview job failed, no `result.json` | "The preview did not finish …", with a job link | Preview again |
| Preview with blockers | Readiness shows ✕ n missing per env, with inline inputs | fill them in, Preview |
| Preview: nothing to change | "No changes: Git already matches dev for this flow." Submit is off | — |
| **conflict** (typed, or a GitLab 400 on `last_commit_id` / branch exists) | "Submit failed: GitLab changed while this request was open." | edit (re-enables Submit), Preview |
| **preflight** | "Submit failed: The request did not pass the checks", with the job message | edit, Preview |
| **partial** | "The request was only partly written", with a branch link | edit to submit again, or open the branch; Cancel is refused because the branch has moved on |
| **blocked** | "Readiness: some environments are missing values", with the readiness rows | fill them in |
| Unknown outcome | "Outcome unknown … Preview and Submit stay off until Check GitLab finds out." | Check GitLab |
| Branch deleted elsewhere | "The request branch no longer exists. Start a new request." | New request |
| Requests list empty | "No merge requests with the pingone-promote label yet." | — |
| MR closed without merge | "The merge request was closed without merging. Nothing rolls out." | — |
| Branch mode, no MR yet | the strip shows Waiting, "branch only: no merge request yet" | open an MR in GitLab; the app picks it up |

The **#57 rule** is in section 2.2:
- an edit clears the error and turns Submit back on;
- an unknown outcome keeps Preview and Submit off until Check GitLab resolves it.

## 10. Accessibility basics
- **Structure.** Each screen has a single `h1`, which gets focus when the screen loads. An error summary gets focus instead when one is shown. Sections use `h2`. Tables use `th scope=col`. Navigation is a `<nav aria-label="App">`.
- **Forms.** Every control has a `<label for>`. Hints and errors are tied to their control with `aria-describedby`, and invalid fields carry `aria-invalid`. Radio groups are `fieldset`/`legend`. Enter in a text box runs the screen's main action.
- **Status.**
  - Working progress is in an `aria-live="polite"` region.
  - Notes use `role=status`, or `role=alert` for failures.
  - States always have text: Live, Needs approval, Failed and the others. The symbols ✓ ⏸ ✕ ○ are `aria-hidden`. Colour is never the only cue.
- **Disabled buttons** keep `aria-disabled` and are described by a visible reason ("Readiness: 2 environment(s) are missing values").
- **Timing.**
  - Automatic checks can be paused (WCAG 2.2.1).
  - They never move focus.
  - They stop on a hidden tab.
  - The spinner respects `prefers-reduced-motion`.
- **Visual.**
  - Focus is a visible 3 px outline.
  - Text contrast is at least 4.5:1: the ink colour is `#16202c` on white, and the muted colour is `#4f5b69`.
  - Buttons are at least 36 px high.
  - Links that open GitLab say so in screen-reader text.

**Vocabulary:**
- Used: Source env, Rollout, Shared definitions, Readiness, Request, Predicted changes, Keep Git version / Use source, Live / Needs approval / Failed / Waiting.
- Never on a screen: the words the v2 plan bans (the `BANNED` list in `test_app.js`), and "plan" only in "pipeline plan job". `test_app.js` checks the visible text of every screen.

## 11. Contract asks for the backend engineer
1. **`changes[].role: "selected"|"dependency"`.** The app has to know which address is the chosen flow, which never gets a keep/update choice (#93). Without the field, every differing row gets a choice.
2. **Structured blockers `{code, var, message}`.** With a `var`, the readiness row becomes an inline input. Plain strings are shown, but cannot be fixed inline.
3. **`requiredSecrets` as `[{key, varName, field}]`.** The app's suggested names and Vault fields come from here. Please also say whether `secretMappings` keys are these `key`s.
4. **`requestedBy`.** The app sends the PingOne username as a string. It also writes `Requested-by:` and `Request-id:` trailers in the commit message, and expects the job to copy both lines into the MR description, which the Requests list reads. If you want it structured (`{id, username}`), or signed, tell us. A signed form needs a shared secret in DaVinci, which the trust model currently avoids.
5. **`diffUrl`** (or a compare URL) in `result.json`, for "Review the change in GitLab".
6. **Always upload `result.json`** (`artifacts: when: always`). On a typed error (other than `partial`), commit nothing and **leave `request.json` in place**, so that the next Submit can update it with `last_commit_id`.
7. **MR label.** Label the MR `pingone-promote` (configurable as `ppMrLabel`); the Requests list filters on it.
8. **`commitMessage`.** The update list in the change message omitted `commitMessage`, but also said the schema is unchanged. The app sends it; confirm the job uses it for the generated commit.
9. **Branch mode.** When the job leaves the branch, is `request.json` deleted there too? Check GitLab treats "branch exists, file gone" as "the job took it". Please keep that true.
10. **Idempotency.** If the same `requestId` with `dryRun: false` is pushed twice (for example a retried commit), the second run should report the existing MR rather than open another.
11. **Job name.** It is `pingone-promote:request`, in the `.pre` stage of the branch push pipeline, and the app depends on that. If the job runs in a child pipeline, the app needs the parent's bridge to point to it. Today only status reads look at bridges.

## 12. Assumptions to verify in a live tenant
1. A `startUiSubFlow` node returns the subflow's outputs as `{{local.<node>.payload.output.<field>}}`, and the sign-in subflow can return `userId`, `username`, `name` and `groups`.
2. A Custom HTML screen whose HTML is entirely `{{local.<render>.payload.output.html}}` renders that HTML unescaped, and binds `skbutton` handlers on buttons inside it. The feasibility flow injected HTML fragments the same way. Two more points:
   - the screen's hidden-looking text field `ppForm` is captured;
   - a click handler registered in the capture phase runs before DaVinci reads the form.
3. Once the script removes `disabled` from Submit, DaVinci submits it like any other button.
4. DaVinci does not resolve `{{…}}` inside substituted values. If it does, the brace escaping already prevents it on screens, and the commit body carries user text only as base64.
5. The REST connector sends `DELETE`. It returns `rawResponse.statusCode` on 204 with an empty body. A non-2xx response takes the false branch, with the status code still set; the feasibility flow found this for GET and POST.
6. `GET /jobs/:id/artifacts/result.json` with a `PRIVATE-TOKEN` returns the JSON directly. If your GitLab redirects artifact downloads to object storage, the REST node must follow the redirect, or the job must also publish the result where a GET can read it (ask 6).
7. The project token, at Developer level with `api` scope, can do all of these:
   - create a branch through the Commits API (`start_branch`);
   - read pipelines, jobs, artifacts, environments and deployments;
   - delete an unprotected branch.
   Branch pipelines also run for commits made by a project bot.
8. The deployment list includes `sha`, `status: blocked` and `created_at`. `GET /deployments/:id` includes `approvals[]` and `pending_approval_count` on your GitLab tier.
9. Job names follow `ppApplyJobPattern` (`apply:{env}`) and environment names follow `ppEnvironmentPattern`. Adjust both to the real enterprise pipeline.
10. Check whether the DaVinci flow timeout (1800 s in the export) counts from the start of the flow or from the last interaction. If it counts from the start, a long rollout watch ends: raise the timeout, or reopen the app and use Requests.
11. The PingOne flow list honours `attributes=id,name` and pages with `_links.next`.
12. Function code size, about 45 KB per function, is within DaVinci's limit. The feasibility flow imported functions of about 37 KB.
