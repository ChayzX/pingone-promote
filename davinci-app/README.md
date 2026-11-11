# PingOne Promote: DaVinci app (reference)

> **Setting up?** Follow the step-by-step checklist in the [main README](../README.md#setup-what-you-do-by-hand). This page is the full reference: every setting, the security model, and how to rebuild the export.

**PingOne Promote - request app** is a DaVinci flow. The shipped export has no sign-in: people type their name, and it is recorded as unverified (see [§1](#1-sign-in-and-launching-the-app)). In the flow they:
1. pick a **source env** and a **main flow**, or bring the **whole environment**;
2. preview the change, optionally with applications and flow policies, things to **leave out** (flows, applications, flow policies, connections, variables) and values that are the same in every env;
3. submit it as a request to the config repo in GitLab;
4. watch its **rollout** (qa → stage → prod).

The flow does light work only. It writes a single file, `.pingone-promote/request.json`, on a `pingone-promote/<requestId>` branch. The GitLab request job does the export, the merge and the merge request.

- Import file: [`flow/pingone-promote-app.flow.json`](flow/pingone-promote-app.flow.json)
- Design, screens, states and budgets: [`DESIGN.md`](DESIGN.md)
- Time needed: about an hour the first time, most of it on PingOne and GitLab setup.

## Prerequisites

1. **PingOne with DaVinci.** You need:
   - the **source env** (usually dev), whose flows are read;
2. **A read-only DaVinci worker.** Add a **Worker** application in the env that will hold it.
   - Give it **only** the **DaVinci Admin Read Only** role, on each source env.
   - Note its client ID and secret. The secret goes into DaVinci only (see [Configure](#configure-after-import)).
3. **The config repo in GitLab**, with the enterprise pipeline and the **request job** `pingone-promote:request`. The request job is installed from `examples/gitlab-ci/pingone-promote.yml` (backend PR #96); see [§3](#3-gitlab-setup-config-repo).
4. **A GitLab project access token** on the config repo, set up as follows:
   - role **Developer**;
   - scope **`api`**;
   - the shortest expiry you can manage.
   It is the only GitLab credential the app holds. There is no trigger token.
5. A DaVinci admin who can import flows and create company variables, and a PingOne admin who can create the worker application.
6. To rebuild or check the export: Python 3 and Node 18 or later. No tenant is needed for that.

## 1. Sign-in and launching the app

**The shipped export has no sign-in.** Its first node checks the settings, and its first screen asks the person for their name. That name is recorded on every request and merge request as `Requested-by: <name> (unverified)`. Nothing checks it.

What protects you with sign-in off:
- **The launch link is the only gate.** Anyone who has it can raise requests. Share it only with the people who should, and do not post it in a public channel.
- **Requests cannot change an environment.** A request only commits `.pingone-promote/request.json` to a `pingone-promote/<id>` branch. The CI job exports `dev` with the read-only worker and opens a merge request. `main` stays protected, the MR needs approval, and the env jobs stay manual, with prod approval. That review and approval are the real controls.
- **Who did what is in GitLab, not in the app.** Treat `Requested-by` as a hint. The person who approves and merges the MR, and whoever runs each env job, is recorded by GitLab.

**Launching the app.** In DaVinci:
1. add an **Application** called *PingOne Promote*;
2. give it a **Flow Policy** that runs **PingOne Promote - request app** at 100%;
3. give operators the policy's launch link. This is the same launch method as the feasibility flow. A widget page also works.

### Optional: sign-in through a subflow

If you later get PingOne SSO, or any other sign-in your DaVinci apps already use, build the export with `python3 gen_app.py --sign-in subflow`. Its first node is a **Flow Conductor** node that calls your sign-in subflow, and the app then checks the identity and `ppAllowedGroups`. **Do not import that build without a sign-in subflow to point it at.** Its placeholder subflow ID `f0d00a7e00000000000000000000fe11` does not exist, so the flow waits on it and times out.

The subflow must finish **only** for a signed-in user, and must return these outputs from a **Flow Conductor return** node:

| Output | For example, from an OpenID Connect sign-in |
| --- | --- |
| `userId` | `sub` |
| `username` | `preferred_username` |
| `name` | `name` |
| `groups` | `groups`: an array, or comma-separated names |

With this build, the app refuses to start if `userId` or `username` is empty, or if no group matches `ppAllowedGroups`. `*` admits every signed-in user, which is not recommended. DESIGN.md §12 lists the connector names as assumptions to verify.

## 2. Import (DaVinci console)

1. Choose **DaVinci → Flows → Add Flow → Import from JSON**, then `flow/pingone-promote-app.flow.json`. The flow is called **PingOne Promote - request app**.
2. Point each node at your own connection. The export carries placeholders:

   | Placeholder connection ID | Connector |
   | --- | --- |
   | `a1f0d00a000000000000000000000002` | Http (REST calls and screens) |
   | `a1f0d00a000000000000000000000003` | Functions (custom functions and A = B) |
   | `a1f0d00a000000000000000000000005` | Variables (Save Value) |
   | `a1f0d00a000000000000000000000006` | Flow Conductor (only in a `--sign-in subflow` build) |

3. Only for a `--sign-in subflow` build: open the first node, **Sign in with PingOne SSO (your sign-in subflow)**, and choose your sign-in subflow. The shipped export has no such node.
4. Set these in **Flow → Settings**:
   - **Flow Timeout:** at least 1800 s, so a person can watch a rollout. See DESIGN.md §12, item 10.
   - **Flow HTTP Response Timeout:** 15 s.
   - **Logging:** Info. Debug logs node inputs and outputs, and tokens are among them.
5. Make the flow version live with the flow editor's publish button. After that, any change to the variables needs the same step.

You can skip steps 2 and 3 by generating a private copy with your IDs filled in (see [Rebuild and check](#rebuild-and-check)).

## Configure after import

**Company variables (set once; kept across re-imports)** (**DaVinci → Variables**). A re-import keeps the values of company variables and resets flow variables, so every setting is a company variable. The import declares them all **without values**; the app uses the default shown below while a variable is empty, so you only set what you need. The two secrets are secret variables: the flow never shows them and never passes them to a function or a screen.

Secrets (always set these):

| Variable | Value |
| --- | --- |
| `ppWorkerBasicAuth` | `base64(clientId:clientSecret)` of the read-only worker, **on one line, without `Basic ` in front**. macOS or Linux: `printf '%s:%s' "$ID" "$SECRET" \| base64 \| tr -d '\n'` (Linux `base64` otherwise breaks the line after 76 characters, and PingOne answers HTTP 400 "invalid authorization header"). PowerShell: `[Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes("${ID}:${SECRET}"))`. The worker's **Token Endpoint Authentication Method** must be **Client Secret Basic** |
| `ppGitlabToken` | The GitLab project access token (Developer, `api`) |

Settings (empty means the default; a variable with no default must be set):

| Variable | Default when empty | Set to |
| --- | --- | --- |
| `ppWorkerEnvironmentId` | empty | the env that holds the worker app |
| `ppSourceEnvs` | empty | the envs a person may pick as the source: `dev=<envId>`, or several separated by commas. The names must be in the job's `PINGONE_PROMOTE_SOURCES`, and must not be in the rollout |
| `ppRolloutEnvs` | `qa,stage,prod` | the rollout order of your pipeline. It must equal the job's `PINGONE_PROMOTE_ROLLOUT` |
| `ppAllowedGroups` | empty | only in a `--sign-in subflow` build: `Promote Operators` (comma-separated names), or `*`. The shipped export ignores it |
| `ppApiOrigin` / `ppAuthOrigin` | `https://api.pingone.com` / `https://auth.pingone.com` | your region, for example `.eu`, `.ca` or `.asia` |
| `ppGitlabApi` | `https://gitlab.com/api/v4` | your GitLab API base |
| `ppGitlabProjectId` | empty | the config repo's numeric ID, or its `group/project` path |
| `ppDefaultBranch` | `main` | the protected default branch |
| `ppApplyJobPattern` | `apply:{env}` | the name of each env's job in the post-merge pipeline |
| `ppEnvironmentPattern` | `{env}` | the GitLab environment name for each env |
| `ppMrLabel` | `pingone-promote` | the label the request job puts on its MRs (`PINGONE_PROMOTE_MR_LABELS`) |
| `ppBranchPrefix` | `pingone-promote/` | the request branch prefix, when your push rules require one, for example `CIAM-` (branches `CIAM-20261111-101500-abc123`). It must end with `/` or `-`, and must equal the CI file's `PINGONE_PROMOTE_BRANCH_PREFIX` and its rule. See [Branch names](#branch-names) |
| `ppRunTimeoutSeconds` / `ppPipelineAppearSeconds` | `600` / `120` | how long to wait for a request job / when to say "no pipeline yet" |
| `ppStatusRefreshSeconds` / `ppStatusMaxMinutes` | `30` / `30` | rollout refresh interval / how long the automatic refresh runs |
| `ppChildPipelines` | `yes` | `no` if your env jobs are never in child pipelines |
| `ppPathFlows` / `ppFlowsListQuery` | `/v1/environments/{envId}/flows` / `attributes=id,name` | change only if the flow-list call turns out to differ |
| `ppPathConnections` / `ppPathVariables` | `/v1/environments/{envId}/connectorInstances` / `/v1/environments/{envId}/variables` | the connection list and the variable list, for the optional **Exclude items** step (connections and variables are found there). Assumptions (DESIGN.md §12, item 17): change them if they differ, or set one to `none` to stop offering that kind. They use only `{envId}`. Both are read once per source env per session (1 token + its pages) |
| `ppPathApplications` / `ppPathFlowPolicies` | `/v1/environments/{envId}/davinciApplications` / `/v1/environments/{envId}/davinciApplications/{appId}/flowPolicies` | the application list and one application's flow policy list, for the optional "Add an application or flow policy" step. These paths are assumptions (DESIGN.md §12): change them if they differ, or set `ppPathApplications` to `none` to switch the step off. They use only `{envId}` and `{appId}` |

Runtime state (`ppState`, `ppFlows`, `ppUrl`, `ppBody`, `ppLastFailed`) is internal: flow-instance variables, one set per session, and they need no setup. Never make one of them a company variable; company variables are shared by every user.

To switch an optional value off, set it to `none` (`ppPathApplications`, `ppPathFlowPolicies`, `ppPathConnections`, `ppPathVariables`, `ppFlowsListQuery`); empty means the default.

A missing or malformed setting stops the app with "PingOne Promote is not set up yet". The screen lists the setting **names** and what is wrong with each, never the values.

## 3. GitLab setup (config repo)

The request job's side is specified in the backend's `docs/gitlab-ci-integration.md` and ADR 0017. The steps below are what the app depends on.

1. **DaVinci's project access token.** Go to **Settings → Access tokens**, choose role *Developer* and scope `api`, and set an expiry. Store it only in `ppGitlabToken`.
   - The app uses it for exactly three kinds of call: `POST /repository/commits` (the request file), `GET` status reads, and `DELETE` of a `<prefix><requestId>` branch (default prefix `pingone-promote/`).
   - The validator and the tests enforce this; see DESIGN.md §4.
2. **The job's own token.** This is a **different** project access token, also Developer with `api`, stored as the masked, protected CI variable `PINGONE_PROMOTE_GITLAB_TOKEN`. DaVinci never holds it.
3. **The CI file.**
   1. Copy the backend's `examples/gitlab-ci/pingone-promote.yml` to `.gitlab/pingone-promote.yml`.
   2. Add `- local: '/.gitlab/pingone-promote.yml'` after the existing enterprise `include:`.
   3. That file defines:
      - `pingone-promote:request`: runs in `.pre` on push to `pingone-promote/*` when `.pingone-promote/request.json` exists. It uploads `result.json` `when: always`, and uses `resource_group: pingone-promote-requests`;
      - `pingone-promote:check`: runs in MR pipelines.
4. **CI/CD variables.** Set these as `docs/gitlab-ci-integration.md` lists them:
   - `PINGONE_PROMOTE_WORKER_CLIENT_ID`, `PINGONE_PROMOTE_WORKER_CLIENT_SECRET` and `PINGONE_PROMOTE_WORKER_ENVIRONMENT_ID`: the read-only worker for the source env;
   - `PINGONE_PROMOTE_REGION_CODE`;
   - `PINGONE_PROMOTE_GITLAB_TOKEN`;
   - optionally `PINGONE_PROMOTE_TERRAFORMER_SHA256` (leave it unset for the first test).
   In the CI file, set `PINGONE_PROMOTE_SOURCES` and `PINGONE_PROMOTE_ROLLOUT` to the same env names as `ppSourceEnvs` and `ppRolloutEnvs`. If your push rules require a branch prefix, see [Branch names](#branch-names).
5. **Protect `pingone-promote/*`** (or `<your prefix>*`) as a wildcard. Allow only the token bot users to push and merge if your tier lets you pick users, otherwise Developers and Maintainers (see `docs/gitlab-ci-integration.md`, "Who may push request branches"). Protected variables then reach the request job, and only project members can create request branches.
   - On a protected branch, DaVinci's Developer token cannot **delete** it. Cancel then says so (HTTP 403), and a Maintainer deletes the branch.
6. **Protect `main`.** Allow nobody to push, require MR approval, and do not let either token's bot approve.
7. **Protect every rollout environment.** Only Maintainers or a release group may run its jobs, and **prod** needs an approval. The manual env jobs and these approvals are the safety boundary, not DaVinci.
8. **Label.** Create `pingone-promote`, matching `ppMrLabel` and `PINGONE_PROMOTE_MR_LABELS`.

## End-to-end test against a disposable GitLab project

Use a **private, disposable** project and a **sandbox** PingOne env. Never use production.

**Setup.**
1. Copy a small terraformer-shaped config into the project.
2. Install the request job as in [§3](#3-gitlab-setup-config-repo): `.gitlab/pingone-promote.yml`, the `local:` include and the CI/CD variables.
3. Add stub env jobs. Each one (`apply:qa`, `apply:stage`, `apply:prod`) should be `when: manual`, carry `environment: { name: <env> }`, and have `script: [echo applied]`.
4. Protect `prod` with one required approval.

The full operator test plan, with the exact names, is `docs/acceptance/live-acceptance.md`. A short version:

1. **Search.** Open the launch link and type your name. Search for a name that matches nothing, then one that matches two flows, then one exact flow ID. The two-flow search must list both, and the app must not pick one.
2. **Preview.** Preview with the defaults. The commit appears on `pingone-promote/<requestId>`, `pingone-promote:request` runs in `.pre`, and Predicted changes, Readiness and Secrets appear. `result.json` has `status: previewed` or `blocked`. Nothing else is committed.
3. **Readiness.** Fill in the env values, tick each secret, choose Update for one differing dependency, and preview again. The same branch gets an update commit (`last_commit_id`).
4. **Submit.** Submit (mode MR). The job makes one commit that adds the change and deletes `request.json`, and opens a labelled MR (`status: submitted`). The app shows **Open merge request**. Submitting the same request again shows "already submitted" and the same MR.
5. **Rollout.** Choose **Watch the rollout**, then merge the MR in GitLab. qa shows **Needs approval** (a manual job) until you run it, then **Live**. prod shows **Needs approval (n of m approvals)**. Make one env job fail (`exit 1`) and check that it shows **Failed**.
6. **Branch mode.** Repeat steps 2 to 4 with **Branch only**. You get a branch link and no MR. The rollout shows "branch only" until you open an MR by hand.
7. **Cancel.** Preview a new request and choose **Cancel request**: the branch is deleted. Push a commit to another request branch by hand and try again: the app refuses.
8. **Options.** After a preview that asks for an env value, tick *Same in all environments* and preview again: `request.json` has `inlineValues` and no per-env value for it. Add an application and one of its flow policies by name.
9. **Result download fallback.** If your GitLab redirects artifact downloads, the screens show "Summary only" (read from the job log's last line) instead of the change list.
10. **Slow GitLab.** Pause the project's runners and submit. The working screen says "waiting for a runner", and after `ppRunTimeoutSeconds` it shows "GitLab is slow" with **Keep waiting** and **Check GitLab**. Resume the runners and choose Check GitLab: the app resumes.
11. **Requests.** The list shows the MR with *Requested by*, and the open request branches.
12. **Whole environment and exclusions.** Choose **Whole environment** on the new request screen: the flow search disappears and Continue needs the tick. The preview leads with counts by type and folds the list; the MR title starts with `[whole environment]`. Use **Exclude items** to leave out a flow, then a connection that the flows use: if Git does not manage it, give its ID for each environment. See the cases E-15 to E-17 of `docs/acceptance/live-acceptance.md`.
13. **Leaks.** With logging at Info, check that no screen shows a token, a secret or an env ID. Also check that `request.json` holds names and values only, never secret values.

### Branch names

Request branches are `<prefix><requestId>`. The request ID is the time plus a random part, `YYYYMMDD-HHMMSS-xxxxxx`, so the default gives `pingone-promote/20261111-101500-abc123`. If GitLab push rules require another name, set the prefix in three places, all to the same value:

1. DaVinci company variable `ppBranchPrefix`, for example `CIAM-`;
2. `PINGONE_PROMOTE_BRANCH_PREFIX` in `.gitlab/pingone-promote.yml`;
3. the `pingone-promote:request` rule in the same file: `$CI_COMMIT_BRANCH =~ /^CIAM-/`.

The prefix must end with `/` or `-`. With `CIAM-`, branches look like `CIAM-20261111-101500-abc123`. That also satisfies a rule such as `^CIAM-[0-9]+`, because the request ID starts with the date. The app only lists, reads and deletes branches that match `<prefix>` plus its own request ID format, so a person's `CIAM-1234-fix-login` branch is never touched.

**Protection with a shared prefix.** The request job needs protected CI variables, which GitLab gives only to protected branches. Protecting `CIAM-*` protects every `CIAM-` branch, including people's own: they can still push and merge as Developers, but cannot force-push or delete. If your push rule accepts any name that starts with `CIAM-`, use a more specific prefix such as `CIAM-promote-`, and protect only `CIAM-promote-*`.

## Rebuild and check

```sh
cd davinci-app
python3 gen_app.py                 # fn/*.js -> flow/pingone-promote-app.flow.json (deterministic; sign-in off)
python3 check_app.py --self-test   # wiring, trust boundary, repo-wide ID/secret scan, broken-copy self-test
node test_app.js                   # unit tests + the exported graph against fake PingOne and fake GitLab
# the sign-in subflow build, checked and tested the same way:
python3 gen_app.py --sign-in subflow --out out/sso.flow.json && python3 check_app.py out/sso.flow.json && node test_app.js out/sso.flow.json
```

- To write a private copy with your connection IDs (and, with `--sign-in subflow`, your sign-in subflow ID):
  1. start from `values.example.json`;
  2. run `gen_app.py --values values.json --out out/private.flow.json`;
  3. check it with `check_app.py out/private.flow.json --allow-connection-ids`.
  `values.json` and `out/` are git-ignored.
- Env IDs, the project ID, the group list and secrets cannot be preset in any copy. They are set in DaVinci only.
- **The contract with the request job is in `fn/_contract.js` alone.** Change it there, regenerate, and run the checks.

## Cleanup (test projects)

1. In GitLab, delete the `pingone-promote/*` branches, close the test MRs, and revoke the project token.
2. In PingOne, delete the worker app's secret or the worker app itself, and the test application if you made one.
3. In DaVinci, delete the flow, its application and flow policy, and the company variables `ppWorkerBasicAuth` and `ppGitlabToken`.
