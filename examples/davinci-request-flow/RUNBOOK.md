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
- [ ] **Your DaVinci connections to reuse**, if any: HTTP, Functions, Error Message and Variables. Copy each connection's 32-character ID from **DaVinci → Connections**.
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
3. **Connections.** Reuse your HTTP, Functions, Error Message and Variables connections. Copy the four IDs.
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

## Several target environments

The pipeline in each promotion project deploys to exactly one PingOne environment, its `PINGONE_TARGET_ENVIRONMENT_ID`, and refuses any request for another. So for every environment you want to deploy **to**:

1. **Create a private promotion project** from the same template: `.gitlab-ci.yml` and `select-request.sh` from `examples/gitlab/`, default branch protected. See [docs/gitlab.md](../../docs/gitlab.md).
2. **Set its CI/CD variables:** `PINGONE_TARGET_ENVIRONMENT_ID` and `PINGONE_TARGET_REGION` for that environment, and the pipeline worker's credentials with roles on that environment. Then add a protected deployment environment with the approvers that environment needs; Prod can require more people than Test.
3. **Point the flow at it:** put the project's numeric ID in that environment's slot, `envNGitlabProjectId` (Terraform: `gitlab_project_id` in `environments`).
4. **Let the flow's GitLab token reach every project.** A group access token (Developer, `api`) on a group holding all the promotion projects is simplest.

An environment you only promote **from**, often Dev, needs no project. If the requester picks a target with no project (and `gitlabProjectId` is empty), the flow says so before building anything.

| Example | Source | Target | Merge request goes to |
| --- | --- | --- | --- |
| Dev → Test | Dev | Test | Test's project, whose pipeline deploys to Test |
| Test → Prod | Test | Prod | Prod's project, with Prod's approvers |

## Troubleshooting

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| Snapshot step fails with HTTP 403 | Worker roles on source or target | Match the pipeline worker's roles that fixed Configuration Management |
| "No GitLab project is set up for <name>" | The chosen target has no `envNGitlabProjectId` and `gitlabProjectId` is empty | Set the project for that environment ([Several target environments](#several-target-environments)) |
| "request destination differs from protected runner target policy" (in GitLab) | The project's pipeline targets another environment | Fix that environment's project ID in the flow, or the project's `PINGONE_TARGET_ENVIRONMENT_ID` |
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
- **Several targets: one GitLab project per target.** The pipeline deploys only to its trusted `PINGONE_TARGET_ENVIRONMENT_ID`, so set up one private promotion project per environment you deploy to, each with that environment as its target and its own approvals. Put each project ID in that environment's slot (`envNGitlabProjectId`, or `gitlab_project_id` in Terraform). The flow sends each request to the chosen target's project; a slot without one uses `gitlabProjectId`. See the runbook's [Several target environments](RUNBOOK.md#several-target-environments).
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
