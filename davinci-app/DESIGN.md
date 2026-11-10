# PingOne Promote in DaVinci: design

The DaVinci-hosted front end of pingone-promote. It is the primary front end. It stays primary until it is shown to be a poor home for the tool.

DaVinci does **light work only**:
- the PingOne SSO sign-in;
- the forms;
- one flow-list read (plus, only when asked, the application, flow policy, connection and variable lists of the exclude step);
- one commit of a small request file;
- status reads.

Export, neutralizing, the additive block-level merge (#93), `terraform fmt` and the merge request are done by the GitLab request job, built by another engineer. No export or translation logic is ported into DaVinci JavaScript.

```
Person ─ PingOne SSO ─▶ DaVinci app flow
  1 source env + main flow search ── PingOne: token + GET flows?attributes=id,name (read-only worker)
    or "Whole environment" (scope all, no search)
  2 options, readiness; optional: exclude items (own step: token + one list per kind, cached)
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
| `fn/<step>.js` | One custom function each: `signin`, `render`, `handle`, `p1_token`, `p1_list`, `gl_commit`, `gl_read`, `gl_trace`, `gl_delete`. |
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
- **What to bring into Git** (`Selected items`, the default, or `Whole environment`): two toggle buttons under the source env.
  - *Selected items* is the flow search below.
  - *Whole environment* hides the flow search and shows a **required** tick box, "Bring the whole <source> environment into Git" (the source name follows the source env list). **Continue** without the tick is refused on the screen with the reason, before anything is committed. With the tick the request is created with `scope: "all"` and `confirmAll: true`, and no flow. The next screen is the review screen.
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
- Secrets: each required secret shows its Terraform variable, Vault field and Vault path, and whether `customer_secrets.yml` already declares it.
  - An optional input maps it to an existing `customer_secrets.yml` name (`secretMappings`: names only, never values).
  - For a secret this request adds (`declared: false`), the Vault tick is an operator checklist, required before Submit.
- Env values are text, or JSON when they start with `[` or `{`, for lists such as redirect URIs.
- The app applies the job's request rules before any commit (`contract.validateRequest`). Examples: a one-line title of at most 255 characters, a commit message of at most 2000 characters, env values only for the source or rollout envs, and no secret-looking values.

### 1.2a Options added to the review screen
```
│ Applications and flow policies   Application: Admin Console                          │
│                                  [Add an application or flow policy]                 │
│ Readiness                                                                           │
│   qa  ✕ 1 to fix  qa: base_url [            ]                                       │
│   [ ] Same in all environments: use the source value for base_url                   │
│       identical everywhere: qa, stage, prod all get the same value; not asked per env│
```
- **Same in all environments (`inlineValues`).** Each value row of the readiness list has the tick. The source value becomes a literal in every env, so the per-env value is not asked. Typed values for that variable are dropped before the commit. The boxes for one variable move together (the screen script). After a preview the variable stays listed under "Same in all environments" so it can be taken back. The job decides whether the variable can be inlined; if not, it names `inlineValues` in `error.field`.

### 1.2b Add an application or flow policy (optional step)
```
│ Add an application or flow policy                                                  │
│ In this request: Applications (1 of 20) Admin Console [Remove ...]                 │
│ Find an application  [ web      ] [Search applications]                            │
│ 1 match   Web Portal  <id>   [Add this application] [Its flow policies]            │
│ Flow policies of Web Portal  [ sign in ] [Search flow policies]                    │
│ 2 matches. Choose one; the app never picks for you                                 │
│ [Back to the options]                                                              │
```
- Search is the flow search: case-insensitive substring or exact ID, every match listed, nothing picked. Empty result: "No application in dev matches ...".
- Sent as `applications` / `flowPolicies`: `{name}` only (the job refuses an id), at most 20 each.
- A name shared by two listed items is refused ("Two applications are named ..."): the job selects by name and could not tell them apart.
- PingOne lists flow policies **per application** (`.../davinciApplications/{appId}/flowPolicies`). Listing every application's policies would cost 1 + N calls in one browser request, so the step reads only the policies of the one application the person opens with **Its flow policies**. This is why it is a separate step and not part of the search screen.
- The list responses can carry client secrets. Only `id` and `name` are kept (`p1_list`); nothing else enters the state or a screen.
- Off when `ppPathApplications` is `none` (no button, no calls).

### 1.2c Exclude items (optional step, in both scopes)
```
│ Exclude items                                                                      │
│ Left out of this request (1)   Flow: Login - MFA                  [Stop excluding] │
│ Find an item   What to exclude [Flow ▾]  Name or ID [ login          ] [Search]    │
│ 2 matches. Choose one; the app never picks for you                                 │
│   Login        <id>  [Exclude this flow]                                           │
│   Login - MFA  <id>  [Exclude this flow]                                           │
│ [Back to the options]                                                              │
```
- It sits next to the include step on the review screen ("Leave out" block, button **Exclude items**). It works in both scopes and has the include search UX: none ("No flow in dev matches ..."), several (all listed, the app picks none) or one.
- Kinds: flow, application, connection, variable, and **flow policy** (reached through an application's **Its flow policies** button, because PingOne lists policies per application, exactly as in 1.2b). Connections and variables are listed from `ppPathConnections` and `ppPathVariables` (assumption 15); a kind whose path is `none` is not offered.
- Sent as `exclude: [{type, id?, name}]`: a flow by ID and name, everything else by name (a variable as `<name>_<context>`, the way the job labels it). At most 200. The job resolves each entry like an include (exact ID, exact name, unique substring) and lists candidates when several match.
- The app refuses what the job would refuse: excluding the main flow, an application or flow policy the request includes, a name two listed items share, or the 201st item.
- **Own REST budget.** A first search of a kind costs 1 token + that list's pages and is then cached for the session (like flows); every later search, add, remove and "Back" costs 0. This is why it is a separate step and not part of the review screen, which makes no PingOne calls. The lists can carry secrets (connection properties, variable values); only `id`, `name` (and a variable's `context`) are kept.

### 1.2d Dependencies: Update, Keep Git version, Exclude
```
│ Shared definitions                                                                 │
│   pingone_davinci_connector_instance.http   ( ) Update (•) Keep Git version ( ) Exclude │
│   pingone_davinci_flow.mfa_sub  (new)       (•) Update                      ( ) Exclude │
│ Readiness                                                                          │
│   dev (source env) ✓ Ready   dev: ID of pingone_davinci_flow.mfa_sub  [<id>] filled from the export │
│   qa    ✕ 1 to fix           qa: ID of pingone_davinci_flow.mfa_sub   [        ]    │
```
- A dependency that differs from Git (or is changed in both) has three choices, defaulting to **Keep Git version**. A new dependency has **Update** and **Exclude** (there is no Git version to keep). The selected item has Update and Keep only: excluding it is refused (here and by the job). A whole-environment request lists only the resources that differ; the rest is added, or left out through the exclude list.
- **Exclude** a dependency that Git already manages behaves like Keep (reason "excluded; Git-managed copy kept").
- **Exclude** a dependency that Git does not manage makes the shared definitions refer to it by a root variable `<type>_<label>_id`. Its ID is asked per environment in Readiness, next to the other missing values, as a text input labelled "ID of <address>"; the **source** env's input is prefilled from the export (`changes[].sourceId`) and only a changed value is sent. These IDs are never offered as "Same in all environments". A missing ID is a `missing_value` blocker with `var` and `address`, so Submit stays off until each environment has one.

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

### 1.3a When result.json cannot be downloaded
If `GET /jobs/:id/artifacts/result.json` answers a redirect (3xx) or 404 for a finished job, the app reads the job log:
- `GET /jobs/:id/trace` on its own REST node, with `Range: bytes=-65536`; the function cuts the text to its last 64 KiB again in case the header is ignored;
- the LAST line starting `PINGONE_PROMOTE_RESULT ` is parsed (`contract.resultFromLog`): status, links, summary counts, blocked envs and the typed error;
- the screens show a "Summary only" notice: the change list, the diff and the required-secrets list are not available in this mode. Submit stays possible; the person must check Vault for new secrets themselves;
- no log text is kept or shown. With no usable line (none, invalid, or for another request) the screen says the job log has no result line and offers **Check GitLab**.

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

For `scope: "all"` the preview leads with **Changes by type** (a table of counts per resource type from `summary.byType`), then a folded **Full list** (`<details>`, closed) of the resources that change or differ. Resources identical to Git are counted, not listed (a large environment would otherwise put thousands of rows in the state); the state keeps at most 2000 rows and says so when more were not kept. **Excluded** items are listed apart, with their reason (`excluded; referenced by ID variable ...` or `excluded; Git-managed copy kept`) and, for a dependency, a tag.

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
                                  │SCOPE:ALL / SCOPE:SELECTED (toggle)  START_ALL (needs the tick) ──▶ options
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
[Check settings (sign-in is off)] fn signin, the one start node ──stop──▶ [Cannot continue] (end screen)
   (with --sign-in subflow: [Sign in with PingOne SSO (your sign-in subflow)] startUiSubFlow comes first,
    then [Check sign-in and settings] fn signin)
   │ ok (keep state; ppFlows={}, ppBody="")
   ▼
[Render screen] fn render → keep state → [App screen] (buttonValue, ppForm) → [Handle input] fn handle
   ▲  │ keep state (ppState, ppUrl, ppBody), then branch on action:
   │  ├─ token  → REST PingOne: worker token (POST)      → fn p1_token  ─list→ REST PingOne: list flows (GET)
   │  │                                                                         → fn p1_list ─list (next page)→ ↺
   │  ├─ commit → REST GitLab: commit request file (POST) → fn gl_commit
   │  ├─ read   → REST GitLab: read (GET)                 → fn gl_read  ─read→ ↺ (queue)  ─delete→
   │  │             ─trace→ REST GitLab: read job log (GET, Range) → fn gl_trace (artifact 3xx or 404 only)
   │  │                                                     REST GitLab: delete request branch (DELETE) → fn gl_delete
   │  ├─ signout → [Left the app] (end screen)
   │  └─ render (default)
   └────────── every handler routes back to Render screen
```
- 86 nodes: 44 steps, 42 evaluators.
- There are 6 REST nodes (the sixth reads the job log), 9 custom functions, 2 screens that continue (App screen, and Render's save) and 2 end screens.
- Every REST call's false branch goes through a "call failed, continue" save into the same handler, which reads the status code. This is the feasibility pattern.
- The URL of every REST node is `{{global.variables.ppUrl}}`. A function builds it, and only through `lib.gl`, the allow-list of GitLab endpoints, or the PingOne URL builders.

**Why one main flow.** All state lives in flow-instance variables:
- `ppState`: about 3 to 10 KB;
- `ppFlows`: the id and name list of one env;
- `ppUrl` and `ppBody`: the next call.

Splitting the flow into subflows would mean passing that state across subflow boundaries on every hop. That adds DaVinci behaviour we have not confirmed, and buys nothing. The only subflow is the sign-in, which is the operator's own flow.

### Sign-in off (the shipped export)

`gen_app.py` builds the export with sign-in off by default (`--sign-in none`). The first node is **Check settings (sign-in is off)**, the `signin` step with the literal input `signIn = none`, and the first screen, `identify`, asks for a name of 1 to 80 characters. Braces, angle brackets, quotes and backslashes are removed. The name is recorded as `requestedBy = "<name> (unverified)"`, and the header says "Not signed in: <name> (unverified)". `ppAllowedGroups` is not read. Until a name is given, `handle` accepts only Continue and Leave, and makes no REST call. The launch link is the only gate, so the controls that matter are GitLab's: protected `main`, MR approval and manual env jobs with prod approval. The request branch and the read-only export cannot change an environment.

### Sign-in subflow contract (`--sign-in subflow`)

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
- **Request branch pipelines run whatever `.gitlab-ci.yml` the branch has.** The backend recommends (ADR 0017) protecting the wildcard `pingone-promote/*` with Developers and Maintainers allowed to push and merge. That makes the protected CI variables available to the request job, and lets only project members create request branches.
  - Keep the request job's PingOne worker read-only. The job only reads the source env.
  - The job's own token (`PINGONE_PROMOTE_GITLAB_TOKEN`) is a second project token, masked and protected. It is never DaVinci's token.
  - On protected request branches, a Developer token cannot delete a branch. Cancel then explains this (HTTP 403) and asks for a Maintainer.
- `requestedBy` is asserted by DaVinci after SSO. GitLab sees the token's bot as the author. Trust in the field rests on the token living only in DaVinci. The backend kept it a plain username (ask 4).

### DaVinci variables and connections

**Company variables, settings** (non-secret; declared WITHOUT values, so a re-import cannot overwrite them; an empty variable means the default in `lib.DEFAULTS`, and `none` switches an optional path off). The ID and group settings have no default and must be set:

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
| `ppPathApplications` / `ppPathFlowPolicies` | `/v1/environments/{envId}/davinciApplications` / `.../davinciApplications/{appId}/flowPolicies` | the application list and one application's flow policy list (assumed paths, section 12). `none` in `ppPathApplications` switches the step off |

**Company secrets:** `ppWorkerBasicAuth` and `ppGitlabToken`.

**Trust boundary for variables.** Company variables are shared by every user and session of the company, so they hold configuration and secrets only. Per-session state is never stored there: it would leak between users and race between concurrent sessions. `check_app.py` fails the export when a state variable (`ppState`, `ppFlows`, `ppUrl`, `ppBody`, `ppForm`, `ppMain`, `ppLastFailed`) is not flow-instance, when a setting is not a company variable, or when any flow-scoped variable remains (a re-import resets those). Only the sign-in check function reads the settings; no screen, and no function other than that one, reads a company variable, and the two secrets are read only in REST headers.

**Flow-instance variables:** `ppState`, `ppFlows`, `ppUrl`, `ppBody` and `ppLastFailed`.

**Connections:**

| Connector | Used by |
| --- | --- |
| Http | REST calls and screens |
| Functions | custom functions and A = B |
| Variables | Save Value |
| Flow Conductor | the sign-in subflow call |

The export carries placeholder connection IDs, as the feasibility flow does. Point them at your connections after import, or generate a private copy with `--values`.

## 5. The contract (final: requestVersion 1, resultVersion 1, in `fn/_contract.js` only)

Source of truth: `internal/request/request.go` and `result.go`, `docs/gitlab-ci-integration.md` and ADR 0017 (PR #96).

- **Branch:** `pingone-promote/<requestId>`.
  - `requestId` must match `^[A-Za-z0-9][A-Za-z0-9_-]{2,63}$`. The app makes `YYYYMMDD-HHMMSS-xxxxxx` (UTC and 6 random base-36 characters), which is safe in branch names and carries no tenant data.
  - `branchName` is always sent, and always equals the branch.
- **File:** `.pingone-promote/request.json`. The job refuses unknown fields and anything over 256 KiB.
  - Always sent: `requestVersion: 1`, `requestId`, `requestedBy` (one line, at most 128 characters), `source`, `flows`, `mode: "mr"|"branch"`, `branchName` and `dryRun`.
  - `source` must not be a rollout env. The settings check refuses a `ppSourceEnvs` name that is also in `ppRolloutEnvs`.
  - `flows` holds at most 20 entries of `{id, name}`. The `id` is sent only when it is a DaVinci ID (32 hex or a UUID); the name is checked by the job.
  - `scope: "all"` and `confirmAll: true` are sent for a whole-environment request (with `flows: []`); a selected request leaves both out. The job refuses `scope: "all"` without `confirmAll: true`, `confirmAll` without `scope: "all"`, and applies the 1-to-20 rule only to a selected request. The app checks the same (`contract.validateRequest`).
  - Sent when set:
    - `dependencyChoices: {<address>: "update"|"keep"|"exclude"}` (the address must match `^pingone_[a-z0-9_]+\.…`; excluding the selected item is refused);
    - `exclude: [{type: flow|application|flowPolicy|connection|variable, id?, name}]` (at most 200; the app never sends `address`; an entry that is also included is refused, naming both);
    - `envValues: {<env>: {<root variable>: <JSON value>}}`, for the source or rollout envs only, never `import_ids`, never secret-looking;
    - `secretMappings: {<terraformer secret var>: <customer_secrets.yml name>}`;
    - `commitMessage` (at most 2000 characters), `title` (one line, at most 255) and `description` (at most 64 KiB).
  - `inlineValues` (names, sorted), from the review screen (section 1.2a); `applications` and `flowPolicies` (`{name}` only, at most 20 each), from section 1.2b.
  - Not sent: `connectorEnvLocal` and `baseCommit`. They are optional in the job and can be added in `contract.buildRequest` without other changes.
  - `contract.validateRequest` applies all of these rules before any commit, using the job's field paths.
- **Preview:**
  - the first Preview runs `POST /repository/commits` with `{branch, start_branch: <default>, commit_message, actions: [{action: "create", file_path, encoding: "base64", content}]}`;
  - later Previews and the Submit send the same call with `action: "update"` and `last_commit_id: <the app's last request commit>`.
- **Submit:** the same as a later Preview, with `dryRun: false`. The job then makes ONE commit that deletes `request.json`, so the next pipeline skips the job.
- **Commit message:** `pingone-promote: preview|submit request <id>`, followed by the trailers `Requested-by: <username>` and `Request-id: <id>`. The app never writes `PingOne-Promote-Request:`; that trailer marks the job's own commit, for idempotency.
- **Finding the job:**
  1. `GET /pipelines?ref=<branch>&sha=<commit>`;
  2. `GET /pipelines/:id/jobs`;
  3. the job named `pingone-promote:request` in `.pre` of the parent pipeline (the highest id, if retried). It is in `resource_group: pingone-promote-requests`, so `waiting_for_resource` reads as "waiting for another request to finish";
  4. when that job is `success`, `failed` or `canceled`, `GET /jobs/:id/artifacts/result.json`. The job uploads it `when: always`, and exits 1 for `blocked` and `failed`.
- **`result.json` (resultVersion 1):**
  - `status`:
    - `previewed`: a Preview;
    - `submitted`: done;
    - `already_submitted`: done, showing the existing commit and MR;
    - `no_changes`: done; the screen says "No changes", with no rollout to watch;
    - `blocked`: shows Readiness;
    - `failed`: shows the typed error.
  - Links: `mrUrl`, `mrIid`, `branchUrl`, and `diffUrl` (the commit when one was made, otherwise the compare view).
  - `changes[]`:
    - `{address, role: selected|dependency|environment|excluded, action, status, file?, from?, reason?, var?, sourceId?}`;
    - `action` is one of `added`, `updated`, `kept_differs`, `unchanged`, `moved` (shown with `from`) or `excluded`;
    - `status` is one of `new`, `changed-in-source`, `matches`, `git-newer`, `changed-in-both` or `not-compared` (excluded), and has its own column;
    - for an excluded dependency referenced by an ID variable, `var` is the variable and `sourceId` the source env's ID (an ID, not a secret).
    - Dependencies that differ, and any address `changed-in-both`, get Update / Keep Git version / Exclude; a new or already excluded dependency gets Update / Exclude.
  - `scope` and `summary`: counts per action, used for the summary line, and `summary.byType` (per Terraform resource type: `total` and the same counts), used first in a whole-environment preview. `changes` is complete; the app drops `unchanged` rows of a whole-environment result (counted in `summary`) and keeps at most 2000 rows.
  - `requiredSecrets[]`: `{key, varName, field, declared, vault}`. `key` is the `secretMappings` key.
  - `blockers`: keyed by env or `_request` (shown as "Whole request"). Each item is `{code, var, address, message}`. A `missing_value` with a `var` becomes an inline input; every other code is explained in words (`contract.BLOCKER_TEXT`).
  - `error`: `{code: conflict|preflight|partial|blocked, type: validation|export|merge|gitlab|internal, message, retryable, field?, candidates?}`.
    - `candidates` (an ambiguous name) are listed.
    - `field` is pointed out next to that input, without blocking it.
    - A `retryable` error lets the same Submit run again without an edit.
  - `warnings` are shown, redacted.
  - `diff` (up to 1 MiB; 512 KiB for `scope: "all"`) is dropped on read and never kept in state; the screens link to `diffUrl`.
  - An unknown status, action or code stays visible as a problem. It is never dropped.
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
| Search applications, first time for a source env | 1 token + 1 per page (budget 2 + pages; capped at 20 pages) | 4 (3 test pages) | applications can be large; only `id,name` are kept |
| Its flow policies (one application) | 1 token + 1 per page of that application's policies | 3 | names only |
| Search flow policies / Search applications again / Add / Remove / Back to the options | 0 | 0 | — |
| Exclude items: search a kind, first time for a source env (flows, applications, connections, variables) | 1 token + 1 per page of that list (budget 2 + pages; capped at 20 pages) | 4 (3 test pages) | `id,name` (and a variable's context) only; connection properties and variable values are never kept |
| Exclude items: Its flow policies (one application) | 1 token + 1 per page of that application's policies | 3 | names only |
| Exclude items: search again / Exclude this / Stop excluding / Back to the options / Whole environment (toggle, Continue) | 0 | 0 | — |
| Working, automatic check | ≤ 3: pipelines by ref and sha → jobs → result.json; **4** when result.json is a redirect or 404 and the job log is read | 3 | jobs page ≤ 100 jobs; result.json, **up to about 1 MiB** because of its `diff` (dropped on read, never kept in state); a whole-environment result also lists every change (thousands of rows, a few MiB at most), of which the state keeps the ones that matter, at most 2000 |
| Check GitLab | ≤ 3 (file → branch → MRs), or file → pipelines → jobs → result.json (→ job log): ≤ 5 | 5 | small; job log: last 64 KiB |
| Cancel | 2 (branch, then delete) | 2 | small |
| Requests | 2 (MRs by label, request branches) | 2 | ≤ 20 rows each |
| Rollout, each check | 3 base (MR → pipeline → jobs) + bridges/child jobs + ≤ 2 per unsettled env; hard cap `lib.MAX_CALLS` = 10 | 9 (with a child pipeline and "Live via !N") | jobs ≤ 100; deployments 5 per env |

Other budgets:
- The flow-instance state stays under about 20 KB: no flow bodies, no diffs, no job logs.
- Each function returns in milliseconds; there is no JSON larger than one GitLab page.
- One browser request makes at most 10 GitLab reads. Above the cap, the screen says "some details were not checked this time" and the next check carries on.
- Export size: about 600 KB, because each of the 9 functions embeds the contract and the helpers, as the feasibility flow did.

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
| **blocked** | "Readiness: some environments are missing values", with the readiness rows. `_request` blockers show as "Whole request" | fill them in |
| Ambiguous flow name (`preflight` with `candidates`) | the candidates by name and address | search again and choose one |
| A field the job refused (`error.field`) | the message next to that input, "GitLab: …" | edit it |
| `retryable: true` | "GitLab says this may work if you try again"; Submit stays on for the same options | Submit again |
| `already_submitted` | "This request was already submitted. Nothing new was written", with the existing MR or branch | Watch the rollout |
| `no_changes` (on submit) | "No changes: … The job wrote nothing and opened no merge request." | New request |
| The app's own checks (title, commit message, env values, secret mappings) | the error summary and the field, before any commit | edit (the buttons turn back on) |
| Cancel on a protected request branch (HTTP 403) | "request branches are protected here. Ask a Maintainer to delete it" | GitLab |
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

## 11. Contract asks for the backend engineer: resolved

The backend (PR #96) accepted all eleven asks. The app now uses them as follows:

| # | Ask | Resolution in the final contract | Used by the app |
| --- | --- | --- | --- |
| 1 | `changes[].role` | `role: selected|dependency` on every change | the selected flow never gets a keep/update choice |
| 2 | Structured blockers | `{code, var, address, message}`, keyed by env or `_request`, with a fixed code list | `missing_value` becomes an inline input; other codes are explained |
| 3 | `requiredSecrets` shape | `{key, varName, field, declared, vault}`; `key` is the `secretMappings` key, and `secretMappings` is a name-to-name map | mapping input; the Vault tick only for `declared: false` |
| 4 | `requestedBy` | stays a username string (at most 128 characters, one line); the job writes `Requested-by:` and `Request-id:` trailers and repeats them in the MR description | Requests list reads both from the MR description |
| 5 | `diffUrl` | the commit page when the job committed, otherwise the compare view | "Review the change / commit in GitLab" |
| 6 | Always upload `result.json`; leave `request.json` on a typed error | `artifacts: when: always`; on any typed error except `partial`, nothing is committed and the request file stays | the next Submit updates it with `last_commit_id` |
| 7 | MR label | `pingone-promote` by default (`PINGONE_PROMOTE_MR_LABELS`) | `ppMrLabel` must match |
| 8 | `commitMessage` | supported, at most 2000 characters; default `PingOne: <flow names> from <source>`; trailers are appended | sent from the options screen |
| 9 | Branch mode deletes `request.json` | the one job commit always deletes it, in both modes | Check GitLab's "branch exists, file gone" means the job took the request |
| 10 | Idempotency | `PingOne-Promote-Request:` trailer; a repeat reports `already_submitted` with the existing commit and MR | shown as done, with the existing link |
| 11 | Job in the parent pipeline | `.pre`, parent pipeline, push rules plus `exists: [.pingone-promote/request.json]` | the job-finding path needs no bridges |

Open points, none of them blocking:
- `connectorEnvLocal` and `baseCommit` are not sent.
- Ask 12 (withdrawn): the app no longer has a "Stop managing" feature, so no `gitOnly[]` list is needed. The request job has no `remove` field either; taking a resource out of Git is a manual change.
- Ask 13 (new): `PINGONE_PROMOTE_RESULT` carries no `requiredSecrets`, so a submit read from the log cannot be gated on the Vault tick.

## 12. Assumptions to verify in a live tenant
1. (Only for a `--sign-in subflow` build; the shipped export has no sign-in, see "Sign-in off" below.) A `startUiSubFlow` node returns the subflow's outputs as `{{local.<node>.payload.output.<field>}}`, and the sign-in subflow can return `userId`, `username`, `name` and `groups`.
2. A Custom HTML screen whose HTML is entirely `{{local.<render>.payload.output.html}}` renders that HTML unescaped, and binds `skbutton` handlers on buttons inside it. The feasibility flow injected HTML fragments the same way. Two more points:
   - the screen's hidden-looking text field `ppForm` is captured;
   - a click handler registered in the capture phase runs before DaVinci reads the form.
3. Once the script removes `disabled` from Submit, DaVinci submits it like any other button.
4. DaVinci does not resolve `{{…}}` inside substituted values. If it does, the brace escaping already prevents it on screens, and the commit body carries user text only as base64.
5. The REST connector sends `DELETE`. It returns `rawResponse.statusCode` on 204 with an empty body. A non-2xx response takes the false branch, with the status code still set; the feasibility flow found this for GET and POST.
6. `GET /jobs/:id/artifacts/result.json` with a `PRIVATE-TOKEN` returns the JSON directly. If your GitLab redirects artifact downloads to object storage, the REST node must follow the redirect. Otherwise the fallback is the job log's last line, `PINGONE_PROMOTE_RESULT {...}` (compact: status, links, summary, blocked envs, error), read with `GET /jobs/:id/trace`. The app now does this: `contract.resultFromLog` reads the last `PINGONE_PROMOTE_RESULT` line from the last 64 KiB. It assumes the REST node does not follow the redirect (the 3xx reaches the function) and that the project token may read `GET /jobs/:id/trace`.
7. The project token, at Developer level with `api` scope, can do all of these:
   - create a branch through the Commits API (`start_branch`);
   - read pipelines, jobs, artifacts, environments and deployments;
   - delete an unprotected branch.
   Branch pipelines also run for commits made by a project bot.
8. The deployment list includes `sha`, `status: blocked` and `created_at`. `GET /deployments/:id` includes `approvals[]` and `pending_approval_count` on your GitLab tier.
9. Job names follow `ppApplyJobPattern` (`apply:{env}`) and environment names follow `ppEnvironmentPattern`. Adjust both to the real enterprise pipeline.
10. Check whether the DaVinci flow timeout (1800 s in the export) counts from the start of the flow or from the last interaction. If it counts from the start, a long rollout watch ends: raise the timeout, or reopen the app and use Requests.
11. The PingOne flow list honours `attributes=id,name` and pages with `_links.next`.
12. Function code size, now about 72 KB for the largest function (the contract and helpers are embedded in each), is within DaVinci's limit. The feasibility flow imported functions of about 37 KB; if the import refuses large functions, this is the first thing to shrink.
13. **Assumed (new): the application and flow policy lists.** `GET /v1/environments/{envId}/davinciApplications` (key `davinciApplications`) and `GET /v1/environments/{envId}/davinciApplications/{appId}/flowPolicies` (key `flowPolicies`), from ADR 0007. They are taken from the SDK, not run against a tenant, and assumed readable by the DaVinci Admin Read Only worker. Both are flow variables (`ppPathApplications`, `ppPathFlowPolicies`); the response is assumed to page with `_links.next` and to carry `id` and `name`.
14. **Assumed (new): `Range` on the job log.** `GET /jobs/:id/trace` honours `Range: bytes=-65536`. If it ignores it, the whole log reaches the function, which keeps only the last 64 KiB, but the transfer is larger. A text body is assumed to arrive as a string in `rawResponse.body`.
15. **Assumed (new): the job only has a result line in the log when it ran far enough.** The log line may be missing after a crash; the app then reports that and offers Check GitLab.
16. **Assumed (new): company variables survive a re-import.** Re-importing the flow keeps the values of existing company variables (it does not overwrite them with the declaration), while flow variables are reset to the export. The export therefore declares the settings as company variables with empty values; if a re-import did overwrite them, the operator would have to set them again, as before with flow variables, and the secrets (also declared empty) would show it first. We found no export in the repository (terraformer fixtures, the feasibility export) that shows company variables with values in a flow export, so this is unverified: import twice in the sandbox, set `ppMrLabel`, import again, and check that it is still set. Also check that an empty company string variable reads as an empty string (not an error) in `{{global.company.variables.X}}`, because the app relies on that to apply its defaults.
17. **Assumed (new): the connection and variable lists.** `GET /v1/environments/{envId}/connectorInstances` (key `connectorInstances`) and `GET /v1/environments/{envId}/variables` (key `variables`), from ADR 0007, for the "Exclude items" step. They are taken from the SDK, not run against a tenant, assumed readable by the DaVinci Admin Read Only worker, to page with `_links.next` and to carry `id` and `name` (a variable also `context`). Both are company variables (`ppPathConnections`, `ppPathVariables`, defaults from `lib.DEFAULTS`; `none` switches the kind off). A variable is sent as `<name>_<context>` and a connection by its `name`, which assumes the request job's catalog labels match them (terraformer labels a variable `<name>_<context>` and a connection by its name). The lists can include secrets (connection properties, variable values); the app keeps only `id`, `name` and `context`.
18. **Assumed (new): the exclude lists are cheap enough.** A first search of a kind in a large environment reads every page (capped at 20 pages of 100); the step is separate so the review screen stays free of PingOne calls. If a kind is too large, set its path to `none` and exclude it by editing the request or by address in `request.json`.
19. **Assumed (new): the job reports `var` and `sourceId` on an excluded dependency.** The prefill of the source env's ID relies on `changes[].sourceId` (ADR 0015). Without it the input is empty and the source env is asked like the others.
