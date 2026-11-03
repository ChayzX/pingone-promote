# PingOne Promote: DaVinci app (operator guide)

**PingOne Promote - request app** is a DaVinci flow. People sign in to it with PingOne SSO. In the flow they:
1. pick a **source env** and a **main flow**;
2. preview the change, optionally with applications and flow policies, values that are the same in every env, and resources to stop managing;
3. submit it as a request to the config repo in GitLab;
4. watch its **rollout** (qa → stage → prod).

The flow does light work only. It writes a single file, `.pingone-promote/request.json`, on a `pingone-promote/<requestId>` branch. The GitLab request job does the export, the merge and the merge request.

- Import file: [`flow/pingone-promote-app.flow.json`](flow/pingone-promote-app.flow.json)
- Design, screens, states and budgets: [`DESIGN.md`](DESIGN.md)
- Time needed: about an hour the first time, most of it on PingOne and GitLab setup.

## Prerequisites

1. **PingOne with DaVinci.** You need:
   - the **source env** (usually dev), whose flows are read;
   - the env where operators sign in. This is often your workforce or administrators env.
2. **A read-only DaVinci worker.** Add a **Worker** application in the env that will hold it.
   - Give it **only** the **DaVinci Admin Read Only** role, on each source env.
   - Note its client ID and secret. The secret goes into DaVinci only (see [Configure](#configure-after-import)).
3. **The config repo in GitLab**, with the enterprise pipeline and the **request job** `pingone-promote:request`. The request job is installed from `examples/gitlab-ci/pingone-promote.yml` (backend PR #96); see [§3](#3-gitlab-setup-config-repo).
4. **A GitLab project access token** on the config repo, set up as follows:
   - role **Developer**;
   - scope **`api`**;
   - the shortest expiry you can manage.
   It is the only GitLab credential the app holds. There is no trigger token.
5. A DaVinci admin who can import flows and create company variables, and a PingOne admin who can create applications and groups.
6. To rebuild or check the export: Python 3 and Node 18 or later. No tenant is needed for that.

## 1. PingOne SSO: application, policy and groups

The app opens only after a PingOne sign-in. Its first node calls **your sign-in subflow**. The rest of the app trusts only what that subflow returns.

1. **A group for operators.** In the env where operators sign in, create a group such as `Promote Operators` and add the people who may raise requests. You will put its name in `ppAllowedGroups`.
2. **The PingOne application, recommended.** Add an **OIDC Web App** called *PingOne Promote* in the same env.
   - **Sign-on policy:** your workforce policy, with MFA.
   - **Access:** restrict it to the `Promote Operators` group, so PingOne itself refuses everyone else.
   - **Attribute mapping:** add `groups` → *Group Names* (`memberOfGroupNames`), next to the standard `sub`, `preferred_username` and `name`.
   - **Redirect URI:** the callback URL of the DaVinci OIDC connection in the next step.
   - **Scopes:** `openid profile`.
3. **The sign-in subflow.** Use the sign-in flow your other DaVinci apps already use, or build a small one:
   - an **OpenID Connect** connection to the PingOne application above. It reuses the operator's PingOne session and asks for MFA as the policy says;
   - then a **Flow Conductor return** node that outputs:

     | Output | From |
     | --- | --- |
     | `userId` | `sub` |
     | `username` | `preferred_username` |
     | `name` | `name` |
     | `groups` | `groups`: an array, or comma-separated names |

   - The subflow must finish **only** for a signed-in user.
   - The app refuses to start if `userId` or `username` is empty, or if no group matches `ppAllowedGroups`. `*` admits every signed-in user, which is not recommended.
4. **Launching the app.** In DaVinci:
   1. add an **Application** called *PingOne Promote*;
   2. give it a **Flow Policy** that runs **PingOne Promote - request app** at 100%;
   3. give operators the policy's launch link. This is the same launch method as the feasibility flow and the prior iteration. A widget page also works.

> The connector and capability names in step 3 are what we expect to find; DESIGN.md §12 lists them as assumptions to verify. If your subflow returns the identity under other names, rewire the four inputs of **Check sign-in and settings**. Nothing else reads the identity.

## 2. Import (DaVinci console)

1. Choose **DaVinci → Flows → Add Flow → Import from JSON**, then `flow/pingone-promote-app.flow.json`. The flow is called **PingOne Promote - request app**.
2. Point each node at your own connection. The export carries placeholders:

   | Placeholder connection ID | Connector |
   | --- | --- |
   | `a1f0d00a000000000000000000000002` | Http (REST calls and screens) |
   | `a1f0d00a000000000000000000000003` | Functions (custom functions and A = B) |
   | `a1f0d00a000000000000000000000005` | Variables (Save Value) |
   | `a1f0d00a000000000000000000000006` | Flow Conductor (the sign-in subflow call) |

3. Open the first node, **Sign in with PingOne SSO (your sign-in subflow)**, and choose your sign-in subflow. It points at the placeholder `f0d00a7e00000000000000000000fe11` until you do.
4. Set these in **Flow → Settings**:
   - **Flow Timeout:** at least 1800 s, so a person can watch a rollout. See DESIGN.md §12, item 10.
   - **Flow HTTP Response Timeout:** 15 s.
   - **Logging:** Info. Debug logs node inputs and outputs, and tokens are among them.
5. Make the flow version live with the flow editor's publish button. After that, any change to the variables needs the same step.

You can skip steps 2 and 3 by generating a private copy with your IDs filled in (see [Rebuild and check](#rebuild-and-check)).

## Configure after import

**Company variables** (**DaVinci → Variables**). The import creates both as empty **secret** variables. The flow never shows them, and never passes them to a function or a screen.

| Variable | Value |
| --- | --- |
| `ppWorkerBasicAuth` | `base64(clientId:clientSecret)` of the read-only worker, for example `printf '%s:%s' "$ID" "$SECRET" \| base64` |
| `ppGitlabToken` | The GitLab project access token (Developer, `api`) |

**Flow variables** (the flow's **Variables** panel). Re-importing resets them.

| Variable | Default | Set to |
| --- | --- | --- |
| `ppWorkerEnvironmentId` | empty | the env that holds the worker app |
| `ppSourceEnvs` | empty | the envs a person may pick as the source: `dev=<envId>`, or several separated by commas. The names must be in the job's `PINGONE_PROMOTE_SOURCES`, and must not be in the rollout |
| `ppRolloutEnvs` | `qa,stage,prod` | the rollout order of your pipeline. It must equal the job's `PINGONE_PROMOTE_ROLLOUT` |
| `ppAllowedGroups` | empty | `Promote Operators` (comma-separated names), or `*` |
| `ppApiOrigin` / `ppAuthOrigin` | `https://api.pingone.com` / `https://auth.pingone.com` | your region, for example `.eu`, `.ca` or `.asia` |
| `ppGitlabApi` | `https://gitlab.com/api/v4` | your GitLab API base |
| `ppGitlabProjectId` | empty | the config repo's numeric ID, or its `group/project` path |
| `ppDefaultBranch` | `main` | the protected default branch |
| `ppApplyJobPattern` | `apply:{env}` | the name of each env's job in the post-merge pipeline |
| `ppEnvironmentPattern` | `{env}` | the GitLab environment name for each env |
| `ppMrLabel` | `pingone-promote` | the label the request job puts on its MRs (`PINGONE_PROMOTE_MR_LABELS`) |
| `ppRunTimeoutSeconds` / `ppPipelineAppearSeconds` | `600` / `120` | how long to wait for a request job / when to say "no pipeline yet" |
| `ppStatusRefreshSeconds` / `ppStatusMaxMinutes` | `30` / `30` | rollout refresh interval / how long the automatic refresh runs |
| `ppChildPipelines` | `yes` | `no` if your env jobs are never in child pipelines |
| `ppPathFlows` / `ppFlowsListQuery` | `/v1/environments/{envId}/flows` / `attributes=id,name` | change only if the flow-list call turns out to differ |
| `ppPathApplications` / `ppPathFlowPolicies` | `/v1/environments/{envId}/davinciApplications` / `/v1/environments/{envId}/davinciApplications/{appId}/flowPolicies` | the application list and one application's flow policy list, for the optional "Add an application or flow policy" step. These paths are assumptions (DESIGN.md §12): change them if they differ, or leave `ppPathApplications` empty to switch the step off. They use only `{envId}` and `{appId}` |

A missing or malformed setting stops the app with "PingOne Promote is not set up yet". The screen lists the setting **names** and what is wrong with each, never the values.

## 3. GitLab setup (config repo)

The request job's side is specified in the backend's `docs/gitlab-ci-integration.md` and ADR 0017. The steps below are what the app depends on.

1. **DaVinci's project access token.** Go to **Settings → Access tokens**, choose role *Developer* and scope `api`, and set an expiry. Store it only in `ppGitlabToken`.
   - The app uses it for exactly three kinds of call: `POST /repository/commits` (the request file), `GET` status reads, and `DELETE` of a `pingone-promote/<requestId>` branch.
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
   - `PINGONE_PROMOTE_TERRAFORMER_SHA256`.
   In the CI file, set `PINGONE_PROMOTE_SOURCES` and `PINGONE_PROMOTE_ROLLOUT` to the same env names as `ppSourceEnvs` and `ppRolloutEnvs`.
5. **Protect `pingone-promote/*`** as a wildcard, with Developers and Maintainers allowed to push and merge (the backend's recommendation). Protected variables then reach the request job, and only project members can create request branches.
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

1. **Search.** Open the launch link and sign in. Search for a name that matches nothing, then one that matches two flows, then one exact flow ID. The two-flow search must list both, and the app must not pick one.
2. **Preview.** Preview with the defaults. The commit appears on `pingone-promote/<requestId>`, `pingone-promote:request` runs in `.pre`, and Predicted changes, Readiness and Secrets appear. `result.json` has `status: previewed` or `blocked`. Nothing else is committed.
3. **Readiness.** Fill in the env values, tick each secret, choose Use source for one differing dependency, and preview again. The same branch gets an update commit (`last_commit_id`).
4. **Submit.** Submit (mode MR). The job makes one commit that adds the change and deletes `request.json`, and opens a labelled MR (`status: submitted`). The app shows **Open merge request**. Submitting the same request again shows "already submitted" and the same MR.
5. **Rollout.** Choose **Watch the rollout**, then merge the MR in GitLab. qa shows **Needs approval** (a manual job) until you run it, then **Live**. prod shows **Needs approval (n of m approvals)**. Make one env job fail (`exit 1`) and check that it shows **Failed**.
6. **Branch mode.** Repeat steps 2 to 4 with **Branch only**. You get a branch link and no MR. The rollout shows "branch only" until you open an MR by hand.
7. **Cancel.** Preview a new request and choose **Cancel request**: the branch is deleted. Push a commit to another request branch by hand and try again: the app refuses.
8. **Options.** After a preview that asks for an env value, tick *Same in all environments* and preview again: `request.json` has `inlineValues` and no per-env value for it. Tick *Stop managing (keeps it in PingOne)* on a dependency row plus the confirm box: the result shows that row as stopped, and the resource is still in PingOne. Add an application and one of its flow policies by name.
9. **Result download fallback.** If your GitLab redirects artifact downloads, the screens show "Summary only" (read from the job log's last line) instead of the change list.
10. **Slow GitLab.** Pause the project's runners and submit. The working screen says "waiting for a runner", and after `ppRunTimeoutSeconds` it shows "GitLab is slow" with **Keep waiting** and **Check GitLab**. Resume the runners and choose Check GitLab: the app resumes.
11. **Requests.** The list shows the MR with *Requested by*, and the open request branches.
12. **Leaks.** With logging at Info, check that no screen shows a token, a secret or an env ID. Also check that `request.json` holds names and values only, never secret values.

## Rebuild and check

```sh
cd davinci-app
python3 gen_app.py                 # fn/*.js -> flow/pingone-promote-app.flow.json (deterministic)
python3 check_app.py --self-test   # wiring, trust boundary, repo-wide ID/secret scan, broken-copy self-test
node test_app.js                   # unit tests + the exported graph against fake PingOne and fake GitLab
```

- To write a private copy with your connection IDs and your sign-in subflow ID:
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
