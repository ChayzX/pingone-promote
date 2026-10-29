# Runbook: set up the DaVinci Request flow

The flow lets someone pick resources and open a GitLab merge request with the same `requests/<id>/request.json` the local app makes. Planning, approval, deployment and rollback stay in the existing GitLab pipeline. This runbook takes the flow from nothing to a first merge request.

> **Proof of concept: sign-in is off.** The flow starts straight at the resource picker, with no Entra sign-in and no DaVinci Admin check, and the MR doesn't name the requester. Anyone who can launch the flow can open a merge request. Nothing deploys without the MR approval and the protected deployment approval in GitLab, but keep the flow policy unpublished or limited to a test audience until sign-in is back. To restore it, see [Turning sign-in back on](#turning-sign-in-back-on).

> **Only importing the JSON?** [IMPORT.md](IMPORT.md) is the short version of Path B.

**Time:** about 45 minutes. **People:** someone who can run your PingOne Terraform (or a PingOne org admin for the console path) and a GitLab Maintainer on the promotion project.

There are two ways to do it. Use **Path A (Terraform)** unless you can't run Terraform against the tenant; **Path B (console)** gets the same result by hand.

| Step | Path A: Terraform | Path B: console |
| --- | --- | --- |
| 1. Check prerequisites | Same | Same |
| 2. GitLab token | Same | Same |
| 3. Create everything in PingOne and DaVinci | One `terraform apply` | About six console tasks |
| 4. First run and checks | Same | Same |

---

## 1. Check prerequisites

Tick each one before you start; every item has bitten a first run.

- [ ] **GitLab pipeline already works** for requests from the local app (see [docs/gitlab.md](../../docs/gitlab.md)). This flow only replaces the local app.
- [ ] **Promotion project is private** and its **default branch is protected**. The flow refuses to submit otherwise.
- [ ] **Configuration Management is enabled** on every environment you'll list, and the pipeline worker can take snapshots. (The worker roles fixed for the 403s are what this flow's worker copies.)
- [ ] **A DaVinci-enabled host environment** for the flow. Prefer one that is never a promotion source or target.
- [ ] **Your DaVinci connections to reuse**, if any: HTTP, Functions, Variables and Flow Conductor. Copy each connection's 32-character ID from **DaVinci → Connections**.
- [ ] **DaVinci can reach GitLab.** gitlab.com needs nothing extra; a private GitLab needs an allowlisted ingress.

Collect these values (IDs, not secrets) as you go:

| Value | Where to find it | Terraform variable | `values.json` key (Path B) |
| --- | --- | --- | --- |
| PingOne domain | Your console URL (`console.pingone.com` → `pingone.com`) | `pingone_domain` | `apiOrigin`, `authOrigin` |
| Organization ID | Admin console → **Organization** | `organization_id` | `organizationId` |
| Host environment ID | The DaVinci environment for the flow | `host_environment_id` | `hostEnvironmentId` |
| Up to four environments (name + ID) | The environments requesters choose between | `environments` | `env1Name`, `env1Id` … `env4Name`, `env4Id` |
| Region code | Derived from the domain (`.com` → `us`) | automatic | `sourceRegion`, `targetRegion` |
| GitLab URL, project ID, environment | Project → **Settings → General** | `gitlab_url`, `gitlab_project_id`, `gitlab_environment` | `gitlabUrl`, `gitlabProjectId`, `gitlabEnvironment` |
| Existing connection IDs | DaVinci → **Connections** | `connection_ids` | `httpConnectionId`, `functionsConnectionId`, `errorConnectionId`, `variablesConnectionId` |

Keep the filled-in values in a private file (`terraform.tfvars` or `values.json`); both names are git-ignored or kept outside the repo. Don't commit tenant IDs to this public repository.

## 2. Create the GitLab token

1. In the promotion project, go to **Settings → Access tokens → Add new token**.
2. Name `davinci-request-flow`, role **Developer**, scope **api**, and an expiry date your policy allows. Put the expiry in your calendar: an expired token makes submissions fail with `GitLab returned HTTP 401`.
3. Store the token where your Terraform reads secrets (CI variable or vault) as `TF_VAR_gitlab_token`. On Path B, keep it ready to paste in step 3.

**Check:** the token is project-scoped and Developer. It can open merge requests; it can't merge to the protected branch or approve a deployment, which is what keeps it harmless.

---

## 3A. Path A: Terraform

The module is in [`terraform/`](terraform/). It creates:

| Created | Where | Purpose |
| --- | --- | --- |
| Worker app + secret, with your worker roles on source and target | Host env | Snapshots and promotion-variable reads |
| Any of the HTTP, Functions, Error or Variables connections you don't reuse | Host env | Flow steps |
| Company secret variables `promoteWorkerBasicAuth`, `promoteGitlabToken` | Host env | Built from the worker secret and your token; nobody copies them by hand |
| The flow, its 44 flow-scoped variables, deploy, enable | Host env | The flow itself |
| DaVinci application + flow policy | Host env | How the flow is launched |

Every name starts with `name_prefix` (default `PingOne Promote`), so the objects are easy to find and remove.

**Variable names don't collide.** The two secrets are company variables with names used only here. All 44 flow variables are flow-scoped, so they can't clash with company or flow-instance variables of the same name elsewhere. `terraform destroy` removes exactly what it created and nothing shared.

1. Copy `terraform/terraform.tfvars.example` to `terraform/terraform.tfvars` and fill it in from the table in step 1.
2. Point Terraform at your PingOne admin worker, the same one your existing PingOne Terraform uses:

   ```sh
   export PINGONE_CLIENT_ID=... PINGONE_CLIENT_SECRET=... PINGONE_ENVIRONMENT_ID=... PINGONE_REGION_CODE=NA
   export TF_VAR_gitlab_token=...   # from your vault or CI
   ```

   That worker needs rights to create applications and role assignments in the host environment, and DaVinci Admin on the host environment.
3. Run:

   ```sh
   cd examples/davinci-request-flow/terraform
   terraform init
   terraform plan -out plan.tfplan     # review: about 55 resources to add, 0 to change or destroy
   terraform apply plan.tfplan
   ```

   In a pipeline, use your normal remote state backend; this module declares none.
4. Note the outputs: `terraform output`. `flow_policy_id` is what you launch.

**If the plan fails on `data.pingone_role`:** a role name in `worker_role_names` doesn't match a built-in role exactly. Check the spelling in the admin console under **Roles**.

**Updating the flow later:** change the code, run `python3 ../gen_flows.py --terraform .` in `terraform/`, then `plan` and `apply`. A changed graph redeploys automatically. If `enable_signin` and the generated flow disagree, the plan stops and tells you which command to run.

## 3B. Path B: console

1. **Worker (host environment).** Add a **Worker** app. Give it your worker roles (for example Promotion Admin) on every listed environment.
2. **Company secrets (DaVinci → Variables).** Create `promoteWorkerBasicAuth` (type secret, context company) with the value from `printf '%s:%s' "$CLIENT_ID" "$CLIENT_SECRET" | base64`, and `promoteGitlabToken` (type secret, company) with the GitLab token.
3. **Connections.** Reuse your HTTP, Functions, Variables and Flow Conductor connections. Copy the four IDs.
4. **Generate the export** with your IDs and connection IDs in a private `values.json` (copy `values.example.json`; leave `"requireSignIn": false`):

   ```sh
   python3 gen_flows.py out values.json && python3 check_flows.py out/*.json
   ```

   The `…ConnectionId` keys wire every node to your connections, so nothing needs re-pointing after import.
5. **Import** `out/pingone-promote-request.flow.json` (**Flows → Add Flow → Import from JSON**), then **Deploy**.
6. **Launch policy.** In **DaVinci → Applications**, add an application and a flow policy that runs this flow at 100% weight.

---

## 4. First run and checks

Launch the flow through its flow policy (output `flow_policy_id`) the way your team launches other DaVinci flows, or run it from the flow editor while testing.

| # | Do | Expect |
| --- | --- | --- |
| 1 | Launch the flow | **Choose resources** lists source and target resources, with the "sign-in is off" note |
| 2 | Pick one low-risk resource, build | **Review request** shows the promotion-variable check and the redacted `request.json` |
| 3 | Cancel | "Nothing was sent to GitLab." |
| 4 | Repeat 1–2 and submit | **Merge request opened** with a link; MR has one new `requests/<id>/request.json` and the Promotion variables section |
| 5 | Watch the MR pipeline | `validate_request` passes |
| 6 | Merge, then follow the normal pipeline | `plan_promotion` runs as it does for local-app requests |
| 7 | Pick a connector with a secret and no promotion variable | Review page and MR say it needs a sensitive promotion variable |

**While doing these, confirm the items that couldn't be checked without a live DaVinci.** If one is wrong, it shows up as an error at that node:
- REST nodes use `httpMethod`, `bodyType`, `rawBody`;
- the polling and paging loops are allowed;
- `globalThis.crypto` is available in functions (there's a fallback either way);
- the promotion-variable reads return what the check expects.

Note the results in the README's live-check list.

## One project, several environments

You keep one private promotion project for the whole lifecycle. The runner still deploys a request only to its job's trusted `PINGONE_TARGET_ENVIRONMENT_ID`, so the trust moves from "one project per target" to "one GitLab environment per target" inside the same project:

1. **Generate the pipeline** for your environment names and commit it as the project's `.gitlab-ci.yml`, together with `examples/gitlab/select-request.sh` and `examples/davinci-request-flow/gitlab/for-this-environment.sh` (same paths):
   ```bash
   python3 gitlab/make_pipeline.py ../gitlab/.gitlab-ci.yml dev test production > .gitlab-ci.yml
   ```
   [`gitlab/example.gitlab-ci.yml`](gitlab/example.gitlab-ci.yml) is the output for `dev test production`. The suite checks it stays in step with the tool's pipeline.
2. **Create a protected GitLab environment per name** (**Operate → Environments**), each with at least one deployment approval. The runner refuses an environment without one, Dev included. Prod can require more approvers than Test.
3. **Scope the trusted variables to each environment** (**Settings → CI/CD → Variables**, *Environment scope*): `PINGONE_TARGET_ENVIRONMENT_ID`, `PINGONE_TARGET_REGION`, the worker credentials, and optionally `PROMOTION_ENVIRONMENT` set to the same name. Don't leave an unscoped (`*`) copy of these; a job with no scoped target stops.
4. **Point the flow at it:** `gitlabProjectId` is the one project. If a target's GitLab environment isn't `gitlabEnvironment`, set it in that slot's `envNGitlabEnvironment` so the merge request names the right one.

What happens after a merge: the plan job of **every** environment starts. Each reads the request's target; only the environment whose scoped `PINGONE_TARGET_ENVIRONMENT_ID` matches it plans ("This request targets …"), and the others finish at once ("Not for dev …") without touching PingOne. Run the manual deploy job of the environment named in the merge request. The deploy job of another environment has no plan and stops. The runner re-checks the target in every job, so a mistake here fails closed.

Rollback, preflight and evidence are manual web pipelines as before, plus `PROMOTION_GITLAB_ENVIRONMENT=<name>` to choose the environment.

| Example | Source | Target | Jobs that act |
| --- | --- | --- | --- |
| Dev → Test | Dev | Test | `plan_promotion_test`, then `apply_promotion_test` (Test's approvers) |
| Test → Prod | Test | Prod | `plan_promotion_production`, then `apply_promotion_production` (Prod's approvers) |

## Troubleshooting

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| "Failed: 2 Snapshot source environment" (or another numbered step) | That subflow stopped without returning, usually on its own error screen, or it isn't deployed | Open that subflow's run in **Analytics**; deploy every subflow after importing |
| A subflow node does nothing or errors at once | It still points at the placeholder Flow Conductor connection, or the subflow isn't deployed | Pick your Flow Conductor connection on the main flow's four subflow nodes; deploy the subflows |
| DaVinci's generic error page (red triangle) | A step failed with nothing to catch it. Since this version every API call has a failure path, so you should see a named error screen instead | Open **Analytics** for the flow (log level is Debug); the last step names where it stopped. Send that step and its response |
| Last step in analytics is a "…: preparing" or "…: reading" page | The page submitted itself twice (fixed: it now submits once) or the next status call failed | Re-import this version; click **Continue** by hand if a page stays |
| Screens show as plain, unstyled text, or a "working" page doesn't continue | An older file had its styles and script inside the HTML, which DaVinci ignores | Re-import this version: each screen's **CSS** and **Script** fields carry them |
| "Failed: <step>" screen | That API call returned an error; the screen gives the HTTP status and the usual cause | 403 on a PingOne step: give the worker app the pipeline worker's roles on that environment |
| "Settings need attention: Fix these flow variables…" | A flow variable is a placeholder, malformed or empty. The flow checks them first, before any API call | Set each named variable in **Flow → Variables**, deploy, launch again |
| Token step fails with HTTP 400 `INVALID_REQUEST` (analytics shows validation errors) | The token URL `{authOrigin}/{hostEnvironmentId}/as/token` is wrong: usually `hostEnvironmentId` reset to the file's placeholder by a re-import, or a custom domain/trailing slash in `authOrigin` | Set `hostEnvironmentId` to the worker app's environment and `authOrigin` to `https://auth.pingone.com` (your region's domain) |
| Token step fails with HTTP 401 `invalid_client` | Worker client ID or secret wrong, or the worker app is in a different environment than `hostEnvironmentId` | Re-paste the worker credentials in the HTTP connector headers / variables |
| Snapshot step fails with HTTP 403 | Worker roles on source or target | Match the pipeline worker's roles that fixed Configuration Management |
| "request destination differs from protected runner target policy" (in GitLab) | A job ran for an environment whose scoped `PINGONE_TARGET_ENVIRONMENT_ID` isn't the request's target | Use the jobs of the environment named in the merge request; check the variable's environment scope |
| "Not for <env>: this request targets …" (in GitLab) | Normal: that environment's job skips requests for other targets | Nothing; use the target environment's jobs |
| "GitLab project must be private…" | Project visibility, ID typo or archived project | Fix the project or `gitlab_project_id` |
| "GitLab default branch must be protected" | Branch protection removed | Re-protect it |
| "create promotion branch: GitLab returned HTTP 401/403" | Token expired, wrong scope or role | New token (step 2), then update `promoteGitlabToken` |
| "… HTTP 400" on create branch | A branch for that request ID already exists | Rare; start the flow again for a new ID |
| "Promotion variables could not be checked" | Promotion-variable API not enabled or role missing | Advisory only; the GitLab plan still enforces PingOne's rules |
| Request too large | Very large selection | Promote in smaller batches |

## Operating it

- **Rotate the worker secret.** Path A: `terraform apply -replace=pingone_application_secret.worker`. Path B: generate a new secret and update `promoteWorkerBasicAuth`.
- **Rotate the GitLab token.** Create a new one, update `TF_VAR_gitlab_token` (or the variable), apply, then revoke the old one.
- **Change the environments.** Edit the `environments` list (Path A) or the `envN…` flow variables (Path B). Up to four.
- **One GitLab project for every environment.** All requests go to `gitlabProjectId`. In that project, each environment you deploy to is a GitLab environment (`dev`, `test`, `production` …) with its own scoped variables and approvals, and the pipeline from `gitlab/make_pipeline.py` runs each environment's jobs only for requests that target it. Name a target's GitLab environment in `envNGitlabEnvironment` (Terraform: `gitlab_environment` in `environments`); empty uses `gitlabEnvironment`. See the runbook's [One project, several environments](RUNBOOK.md#one-project-several-environments).
- **Remove everything.** Path A: `terraform destroy`. Path B: delete the flow, the DaVinci application, the two company variables, and the worker app.
- **Audit.** Every request is a merge request (naming the admin once sign-in is on); every deployment is a protected GitLab job; PingOne records each native promotion.

## Turning sign-in back on

Sign-in adds the admin's own Entra login (through PingOne SSO), a DaVinci Admin role check before anything runs, and the requester's name in the MR. The code and its tests are already in the kit; turning it on takes four steps.

1. **Values.** Add `admin_environment_id` (the environment holding admin accounts) and `entra_identity_provider_id` (the Entra external IdP, from the admin console's **Integrations → External IdPs**), and set `enable_signin = true`. On Path B, set `"requireSignIn": true` and `adminEnvironmentId` in `values.json`.
2. **Regenerate the flow:** `python3 ../gen_flows.py --terraform . --signin` (Path B: `python3 gen_flows.py out values.json`).
3. **Apply.** Terraform adds:
   - the OIDC web app with a sign-on policy that sends users to the Entra IdP;
   - the OIDC & OAuth IdP connection;
   - Identity Data Read Only for the worker on the admin environment.

   On Path B, create these by hand: an **OIDC Web App** (Authorization Code, Client Secret Basic) whose sign-on policy has one **External Identity Provider** step pointing at the Entra IdP, a new **OIDC & OAuth IdP** connection, and the extra worker role. Then re-import.
4. **Configure the OIDC connection** (below), and add these test cases to your first run:
   - an admin **without** DaVinci Admin gets "Access denied…", with no snapshots taken;
   - a DaVinci Admin goes to Entra and back;
   - the MR names them.

### Configure the OIDC connection

In **DaVinci → Connections**, open the flow's **OIDC & OAuth IdP** connection and set:

| Field | Value |
| --- | --- |
| Auth Type | OpenId |
| Issuer URL | Terraform output `oidc_issuer`, i.e. `https://auth.pingone.com/<admin env ID>/as` |
| Authorization / Token / UserInfo Endpoint | `<issuer>/authorize`, `<issuer>/token`, `<issuer>/userinfo` |
| App ID | Output `signin_app_client_id` (Path B: the sign-in app's client ID) |
| Client Secret | `terraform output -raw signin_app_client_secret` (Path B: the app's secret) |
| Scope | `openid profile email` |

Then compare the connection's **Redirect URL** with Terraform output `oidc_redirect_uri`.
- **They match:** you're done.
- **They differ:** set `oidc_redirect_uri` in `terraform.tfvars` to the connection's value and apply again.
- **Path B:** paste the connection's Redirect URL into the sign-in app's redirect URIs.

**Sign-in troubleshooting:**

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| "Access denied: … DaVinci Admin role" for a real admin | Role is on another environment, or `davinciAdminRoleId` points at the wrong role | Give DaVinci Admin on the organization or the source environment; leave `davinci_admin_role_id` empty to look it up by name |
| "Role assignments could not be read" | Worker lacks Identity Data Read Only on the admin env | Add it (Terraform does) |
| Sign-in loops or "redirect_uri mismatch" | Redirect URI on the app ≠ the connection's Redirect URL | See the comparison step above |

Also confirm on the first sign-in that the sign-on capability exports as `loginFirstFactor`; its `accessToken` output is confirmed by Ping's connector docs.
