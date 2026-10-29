# Runbook: set up the DaVinci Request flow

The flow lets a DaVinci Admin sign in with your Entra SSO, pick resources, and open a GitLab merge request with the same `requests/<id>/request.json` the local app makes. Planning, approval, deployment and rollback stay in the existing GitLab pipeline. This runbook takes the flow from nothing to a first merge request.

**Time:** about an hour. **People:** someone who can run your PingOne Terraform (or a PingOne org admin for the console path) and a GitLab Maintainer on the promotion project.

There are two ways to do it. Use **Path A (Terraform)** unless you can't run Terraform against the tenant; **Path B (console)** gets the same result by hand.

| Step | Path A: Terraform | Path B: console |
| --- | --- | --- |
| 1. Check prerequisites | Same | Same |
| 2. GitLab token | Same | Same |
| 3. Create everything in PingOne and DaVinci | One `terraform apply` | About ten console tasks |
| 4. Configure the OIDC connection | Console, about 5 minutes | Part of step 3 |
| 5. First run and checks | Same | Same |

---

## 1. Check prerequisites

Tick each one before you start; every item has bitten a first run.

- [ ] **GitLab pipeline already works** for requests from the local app (see [docs/gitlab.md](../../docs/gitlab.md)). This flow only replaces the local app.
- [ ] **Promotion project is private** and its **default branch is protected**. The flow refuses to submit otherwise.
- [ ] **Configuration Management is enabled** on the source and target environments, and the pipeline worker can take snapshots. (The worker roles fixed for the 403s are what this flow's worker copies.)
- [ ] **A DaVinci-enabled host environment** for the flow. Prefer one that is never a promotion source or target.
- [ ] **The Entra external IdP** exists in the admin environment. Note its ID from **Integrations → External IdPs** in the admin console.
- [ ] **Your DaVinci connections to reuse**, if any: HTTP, Functions, Error Message and Variables. Copy each connection's 32-character ID from **DaVinci → Connections**.
- [ ] **DaVinci can reach GitLab.** gitlab.com needs nothing extra; a private GitLab needs an allowlisted ingress.

Collect these values (IDs, not secrets) as you go:

| Value | Where to find it | Terraform variable | `values.json` key (Path B) |
| --- | --- | --- | --- |
| PingOne domain | Your console URL (`console.pingone.com` → `pingone.com`) | `pingone_domain` | `apiOrigin`, `authOrigin` |
| Organization ID | Admin console → **Organization** | `organization_id` | `organizationId` |
| Admin environment ID | Environment holding admin accounts | `admin_environment_id` | `adminEnvironmentId` |
| Entra external IdP ID | Admin env → **Integrations → External IdPs** | `entra_identity_provider_id` | — |
| Host environment ID | The DaVinci environment for the flow | `host_environment_id` | `hostEnvironmentId` |
| Source / target environment IDs | Environments you promote between | `source_environment_id`, `target_environment_id` | `sourceEnvironmentId`, `targetEnvironmentId` |
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
| OIDC web app + secret, sign-on policy sending users to the Entra IdP | Admin env | Admin sign-in for the flow |
| Worker app + secret, roles on source/target and Identity Data Read Only on the admin env | Host env | Snapshots, promotion-variable reads, role check |
| OIDC & OAuth IdP connection (and any connection you don't reuse) | Host env | Sign-in step of the flow |
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

   That worker needs rights to create applications, sign-on policies and role assignments in the admin and host environments, and DaVinci Admin on the host environment.
3. Run:

   ```sh
   cd examples/davinci-request-flow/terraform
   terraform init
   terraform plan -out plan.tfplan     # review: ~60 resources to add, 0 to change or destroy
   terraform apply plan.tfplan
   ```

   In a pipeline, use your normal remote state backend; this module declares none.
4. Note the outputs: `terraform output`, and `terraform output -raw signin_app_client_secret` for step 4.

**If the plan fails on `data.pingone_role`:** a role name in `worker_role_names` doesn't match a built-in role exactly. Check the spelling in the admin console under **Roles**.

**Updating the flow later:** change the code, run `python3 ../gen_flows.py --terraform .` in `terraform/`, then `plan` and `apply`. A changed graph redeploys automatically.

## 3B. Path B: console

1. **Sign-in app (admin environment).** Add an **OIDC Web App**: grant Authorization Code, response type Code, auth method Client Secret Basic. Create a **sign-on policy** with a single **External Identity Provider** step pointing at the Entra IdP, and assign it to the app. Leave the redirect URI for step 4.
2. **Worker (host environment).** Add a **Worker** app. Give it your worker roles (for example Promotion Admin) on the source and target environments, and **Identity Data Read Only** on the admin environment.
3. **Company secrets (DaVinci → Variables).** Create `promoteWorkerBasicAuth` (type secret, context company) with the value from `printf '%s:%s' "$CLIENT_ID" "$CLIENT_SECRET" | base64`, and `promoteGitlabToken` (type secret, company) with the GitLab token.
4. **Connections.** Reuse your HTTP, Functions, Error Message and Variables connections, and create a new **OIDC & OAuth IdP** connection (configure it in step 4 below). Copy all five IDs.
5. **Generate the export** with your IDs and connection IDs in a private `values.json` (copy `values.example.json`):

   ```sh
   python3 gen_flows.py out values.json && python3 check_flows.py out/*.json
   ```

   The five `…ConnectionId` keys wire every node to your connections, so nothing needs re-pointing after import.
6. **Import** `out/pingone-promote-request.flow.json` (**Flows → Add Flow → Import from JSON**), then **Deploy**.
7. **Launch policy.** In **DaVinci → Applications**, add an application and a flow policy that runs this flow at 100% weight.

---

## 4. Configure the OIDC connection (both paths)

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

---

## 5. First run and checks

Launch the flow through its flow policy (output `flow_policy_id`) the way your team launches other DaVinci flows, or run it from the flow editor while testing.

| # | Do | Expect |
| --- | --- | --- |
| 1 | Sign in as an admin **without** DaVinci Admin | "Access denied…"; no snapshots taken, nothing sent to GitLab |
| 2 | Sign in as a DaVinci Admin | Redirect to Entra, back to **Choose resources** listing source and target resources |
| 3 | Pick one low-risk resource, build | **Review request** shows the promotion-variable check and the redacted `request.json` |
| 4 | Cancel | "Nothing was sent to GitLab." |
| 5 | Repeat 2–3 and submit | **Merge request opened** with a link; MR has one new `requests/<id>/request.json` and the Promotion variables section |
| 6 | Watch the MR pipeline | `validate_request` passes |
| 7 | Merge, then follow the normal pipeline | `plan_promotion` runs as it does for local-app requests |
| 8 | Pick a connector with a secret and no promotion variable | Review page and MR say it needs a sensitive promotion variable |

**While doing these, confirm the items that couldn't be checked without a live DaVinci.** If one is wrong, it shows up as an error at that node:
- REST nodes use `httpMethod`, `bodyType`, `rawBody`;
- the sign-on capability is `loginFirstFactor` (its `accessToken` output is confirmed by Ping's connector docs);
- the polling and paging loops are allowed;
- `globalThis.crypto` is available in functions (there's a fallback either way);
- the promotion-variable reads return what the check expects.

Note the results in the README's live-check list.

## Troubleshooting

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| "Access denied: … DaVinci Admin role" for a real admin | Role is on another environment, or `davinciAdminRoleId` points at the wrong role | Give DaVinci Admin on the organization or the source environment; leave `davinci_admin_role_id` empty to look it up by name |
| "Role assignments could not be read" | Worker lacks Identity Data Read Only on the admin env | Add it (Terraform does) |
| Snapshot step fails with HTTP 403 | Worker roles on source or target | Match the pipeline worker's roles that fixed Configuration Management |
| Sign-in loops or "redirect_uri mismatch" | Redirect URI on the app ≠ the connection's Redirect URL | Step 4 |
| "GitLab project must be private…" | Project visibility, ID typo or archived project | Fix the project or `gitlab_project_id` |
| "GitLab default branch must be protected" | Branch protection removed | Re-protect it |
| "create promotion branch: GitLab returned HTTP 401/403" | Token expired, wrong scope or role | New token (step 2), then update `promoteGitlabToken` |
| "… HTTP 400" on create branch | A branch for that request ID already exists | Rare; start the flow again for a new ID |
| "Promotion variables could not be checked" | Promotion-variable API not enabled or role missing | Advisory only; the GitLab plan still enforces PingOne's rules |
| Request too large | Very large selection | Promote in smaller batches |

## Operating it

- **Rotate the worker secret.** Path A: `terraform apply -replace=pingone_application_secret.worker`. Path B: generate a new secret and update `promoteWorkerBasicAuth`.
- **Rotate the GitLab token.** Create a new one, update `TF_VAR_gitlab_token` (or the variable), apply, then revoke the old one.
- **Add an environment pair.** Deploy a second copy with different `source_environment_id`/`target_environment_id` and a different `name_prefix`.
- **Remove everything.** Path A: `terraform destroy`. Path B: delete the flow, the DaVinci application, the two company variables, the OIDC connection, and the two apps.
- **Audit.** Every request is a merge request naming the admin; every deployment is a protected GitLab job; PingOne records each native promotion.
