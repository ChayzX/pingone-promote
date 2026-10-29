# DaVinci front door for PingOne Promote

A DaVinci flow that lets PingOne admins request a promotion without the local app. It never changes PingOne. Its only write is a GitLab merge request that adds `intents/<id>/intent.json`. Merging that request starts the existing protected pipeline: `build-request`, then plan, then deployment approval, then apply. See [docs/gitlab.md](../../docs/gitlab.md#intents-from-the-davinci-front-door).

**Status: preview, not yet imported into a live tenant.** The custom functions and the intent format are tested (below). The flow export follows the node shapes of real DaVinci exports. Some field names in it are inferred, though, and are listed under [Check after import](#check-after-import).

## What the flow does

1. **Sign in.** The OIDC & OAuth IdP connector signs the admin in against the PingOne environment that holds admin accounts, using that environment's existing sign-on policy (for example, SSO through Entra).
2. **Identify.** It calls `userinfo` with the admin's access token. The `sub` claim it gets back is the only identity the flow trusts.
3. **Authorize.** A read-only worker reads the admin's role assignments. Only the **DaVinci Admin** role, scoped to the organization or to the source environment, gets through. Everyone else sees "Access denied". The role is found by name, and the `davinciAdminRoleId` variable can override it.
4. **Select.** The flow lists the source environment's applications. The admin enters resource IDs, optional `sourceId=targetId` mappings and a reason.
5. **Build.** A custom function validates the input with the same rules as `pingone-runner validate --intent`. It builds the intent with the signed-in identity as `requestedBy`, never anything typed into the form.
6. **Confirm.** The admin reviews the exact intent file.
7. **Submit.** The flow commits the intent to `front-door/<id>` and opens a merge request into the default branch. It then shows the merge request link, or the GitLab error.

## Trust boundaries

| Credential | Lives in | Can do | Can never do |
| --- | --- | --- | --- |
| Admin's own login and MFA | Admins' PingOne environment (via its existing SSO) | Sign in to this flow | Anything beyond its own session |
| Read-only worker | DaVinci secret variable `frontDoorWorkerBasicAuth` | Read source applications, role assignments and the role list | Write to any environment |
| GitLab token (project access token, Developer role, `api` scope) | DaVinci secret variable `frontDoorGitLabToken` | Push a new branch and open a merge request | Push to the protected default branch, approve deployments, read protected variables |
| Production worker | GitLab protected, masked variable | Snapshot, plan and execute in the approved job | Be read by DaVinci |

Host the flow in an environment that is never a promotion source or target, so the tool can't promote itself.

## Setup

1. **Generate the export.** Copy `values.example.json` to a private `values.json`, fill it in, and generate:

   ```sh
   python3 gen_flow.py front-door.flow.json values.json
   python3 check_flow.py front-door.flow.json
   ```

   Keep `values.json` and the generated export out of public repositories.

2. **Create the sign-in app.** In the environment that holds admin accounts, add an **OIDC Web App** (Authorization Code grant). Give it the same sign-on policy the admin console uses. You'll add its redirect URI in step 4.

3. **Create the read-only worker.** In the host environment, create a **Worker** app and grant only:
   - **Configuration Read Only** on the source environment;
   - **Identity Data Read Only** on the admins' environment.

   Enable it, then base64-encode `clientId:clientSecret` for the `frontDoorWorkerBasicAuth` secret (step 6):

   ```sh
   printf '%s:%s' "$WORKER_CLIENT_ID" "$WORKER_CLIENT_SECRET" | base64
   ```

4. **Create the DaVinci connections.** In the host environment's DaVinci, create **Http**, **Functions**, **Error Message** and **OIDC & OAuth IdP**. Configure the OIDC & OAuth IdP connection as follows:
   - **Auth Type:** OpenId
   - **Issuer URL:** `https://auth.pingone.com/<adminEnvironmentId>/as`
   - **Authorization endpoint:** `https://auth.pingone.com/<adminEnvironmentId>/as/authorize`
   - **Token endpoint:** `https://auth.pingone.com/<adminEnvironmentId>/as/token`
   - **UserInfo endpoint:** `https://auth.pingone.com/<adminEnvironmentId>/as/userinfo`
   - **App ID:** the sign-in app's client ID
   - **Client Secret:** the sign-in app's secret
   - **Scope:** `openid profile email`

   Copy the connection's **Redirect URL** into the sign-in app's redirect URIs, then enable the app.

5. **Import the flow.** Go to **Flows → Add Flow → Import from JSON**. Point each node at your connection; the export uses placeholder connection IDs `a1f0d00a…0001` to `…0004`.

6. **Set the secrets.** Set the company secret variables `frontDoorWorkerBasicAuth` and `frontDoorGitLabToken`. The export always carries them empty.

7. **Check the inferred fields.** Work through [Check after import](#check-after-import), then deploy and test with the cases below.

## Check after import

These fields were written from DaVinci's documented display names, not from a captured export. Open each listed node and confirm the value shows in the right field:

- **Make REST API Call** nodes (`userinfoap`, `workertokn`, `rolesgetap`, `roleslstap`, `appslistap`, `gitcommit1`, `gitmrcreat`): confirm **HTTP Method**, **Headers** and, on the three POST nodes, **Body = raw** with the raw body shown below. The export uses `httpMethod`, `bodyType` and `rawBody`.
- **Sign in** (`signinoidc`): confirm the capability is the connector's **Sign On**. The export uses `loginFirstFactor`. Also confirm the access-token output is named `accessToken`, which the userinfo node uses.
- **Custom functions** (`authzcheck`, `appsformat`, `buildinten`, `resultchck`): confirm the **Output Schema** carries the listed fields, so later nodes can reference them.

The table below also lets you rebuild any node by hand.

## Test cases

| Case | Expected |
| --- | --- |
| Admin with DaVinci Admin (organization or source environment) submits one valid ID | A merge request opens with `intents/<id>/intent.json`; its pipeline runs `validate --intent` |
| Admin without DaVinci Admin, or with it only on another environment | "Access denied: You need the DaVinci Admin role…" |
| Duplicate ID, `../x`, or a bad mapping line | "The request was not submitted: …"; no branch is created |
| Cancel on the confirm page | "Cancelled. Nothing was submitted." |
| GitLab token missing or wrong | "Submission failed: GitLab rejected the commit (HTTP 401)." |
| Merge the merge request | The protected push pipeline's `plan_promotion` runs `build-request`, then plan; apply waits for approval |

## Tests

```sh
go build -o pingone-runner ../../cmd/pingone-runner
node test_functions.js ./pingone-runner    # the 4 custom functions, plus the real runner validating a generated intent
python3 gen_flow.py /tmp/fd.json && python3 check_flow.py /tmp/fd.json
```

`check_flow.py` checks for one start node, every node reachable, every `{{local.<node>…}}` reference pointing to an earlier step, every referenced variable declared, and every evaluator's false branch wired.

## Node reference

| Node | Step | Connector · capability | Configuration |
| --- | --- | --- | --- |
| `signinoidc` | Sign in with PingOne (admin SSO) | PingOne Admin SSO · `loginFirstFactor` |  |
| `userinfoap` | Who signed in (userinfo) | Http · `makeRestApiCall` | `GET https://auth.pingone.com/{{global.flow.variables.adminEnvironmentId}}/as/userinfo`<br>Headers: `Authorization: Bearer {{local.signinoidc.payload.output.accessToken}}` |
| `workertokn` | Read-only worker token | Http · `makeRestApiCall` | `POST https://auth.pingone.com/{{global.flow.variables.hostEnvironmentId}}/as/token`<br>Headers: `Authorization: Basic {{global.company.variables.frontDoorWorkerBasicAuth}}; Content-Type: application/x-www-form-urlencoded`<br>Raw body: `grant_type=client_credentials` |
| `rolesgetap` | Signed-in admin's role assignments | Http · `makeRestApiCall` | `GET https://api.pingone.com/v1/environments/{{global.flow.variables.adminEnvironmentId}}/users/{{local.userinfoap.payload.output.rawResponse.body.sub}}/roleAssignments`<br>Headers: `Authorization: Bearer {{local.workertokn.payload.output.rawResponse.body.access_token}}` |
| `roleslstap` | PingOne role list (find DaVinci Admin) | Http · `makeRestApiCall` | `GET https://api.pingone.com/v1/roles`<br>Headers: `Authorization: Bearer {{local.workertokn.payload.output.rawResponse.body.access_token}}` |
| `authzcheck` | Allow only DaVinci Admins (org or source env) | Functions · `customFunction` | Inputs: userinfoStatus, userinfoBody, roleAssignmentsStatus, roleAssignmentsBody, rolesListStatus, rolesListBody, davinciAdminRoleId, organizationId, sourceEnvironmentId |
| `authzallow` | Decision is allow? | Functions · `AEqualsB` | `{{local.authzcheck.payload.output.decision}}` equals `allow` |
| `deniedpage` | Access denied | Error Message · `customErrorMessage` | Access denied: {{local.authzcheck.payload.output.reason}} |
| `appslistap` | List source applications | Http · `makeRestApiCall` | `GET https://api.pingone.com/v1/environments/{{global.flow.variables.sourceEnvironmentId}}/applications`<br>Headers: `Authorization: Bearer {{local.workertokn.payload.output.rawResponse.body.access_token}}` |
| `appsformat` | Format application list | Functions · `customFunction` | Inputs: applicationsStatus, applicationsBody |
| `selectform` | Choose resources | Http · `customHTMLTemplate` | Fields: selectedRaw, mappingsRaw, description, buttonValue |
| `buildinten` | Build intent.json and GitLab bodies | Functions · `customFunction` | Inputs: userId, username, sourceEnvironmentId, targetEnvironmentId, region, selectedRaw, mappingsRaw, description, defaultBranch |
| `buildvalid` | Intent valid? | Functions · `AEqualsB` | `{{local.buildinten.payload.output.status}}` equals `ok` |
| `invalidreq` | Invalid request | Error Message · `customErrorMessage` | The request was not submitted: {{local.buildinten.payload.output.error}} |
| `confirmpge` | Confirm request | Http · `customHTMLTemplate` | Fields: buttonValue |
| `submitchck` | Submit pressed? | Functions · `AEqualsB` | `{{local.confirmpge.payload.output.buttonValue}}` equals `SUBMIT` |
| `cancelledm` | Cancelled | Http · `customHtmlMessage` | Cancelled. Nothing was submitted. |
| `gitcommit1` | Commit intents/<id>/intent.json | Http · `makeRestApiCall` | `POST {{global.flow.variables.gitlabApiBase}}/projects/{{global.flow.variables.gitlabProjectId}}/repository/commits`<br>Headers: `PRIVATE-TOKEN: {{global.company.variables.frontDoorGitLabToken}}; Content-Type: application/json`<br>Raw body: `{{local.buildinten.payload.output.commitBody}}` |
| `gitmrcreat` | Open merge request | Http · `makeRestApiCall` | `POST {{global.flow.variables.gitlabApiBase}}/projects/{{global.flow.variables.gitlabProjectId}}/merge_requests`<br>Headers: `PRIVATE-TOKEN: {{global.company.variables.frontDoorGitLabToken}}; Content-Type: application/json`<br>Raw body: `{{local.buildinten.payload.output.mrBody}}` |
| `resultchck` | Check GitLab results | Functions · `customFunction` | Inputs: commitStatus, mrStatus, mrBody |
| `resultokay` | Submitted? | Functions · `AEqualsB` | `{{local.resultchck.payload.output.result}}` equals `ok` |
| `donepage01` | Merge request opened | Http · `customHtmlMessage` | Merge request opened: {{local.resultchck.payload.output.mrUrl}} . A reviewer merges it; the protected pipeline then plans the promotion and waits for deployment approval. |
| `gitfailed1` | GitLab submission failed | Error Message · `customErrorMessage` | Submission failed: {{local.resultchck.payload.output.error}} |
