# Import the DaVinci Request flow (proof of concept)

The import file is [`export/pingone-promote-request.flow.json`](export/pingone-promote-request.flow.json). Sign-in is **off**: the flow starts with picking environments, and the GitLab MR and deployment approvals still gate every change. No Terraform needed.

## What you have to set (checklist)

After importing, these are the only values the flow needs. Everything else is internal to the flow.

| Where | Name | Required? | What to put | Example |
| --- | --- | --- | --- | --- |
| Company variable (secret) | `promoteWorkerBasicAuth` | Yes | `base64(clientId:clientSecret)` of the worker app ([how](#3-set-the-variables-and-settings)) | `YWJj…` |
| Company variable (secret) | `promoteGitlabToken` | Yes | GitLab project access token on the promotion project, Developer + `api` | `glpat-…` |
| Flow variable | `hostEnvironmentId` | Yes | Environment where the **worker app** lives (its token URL) | `9f1c…` |
| Flow variable | `env1Name`, `env1Id` | Yes | First environment: button label and environment ID | `Dev`, `2b7e…` |
| Flow variable | `env2Name`, `env2Id` | Yes | Second environment | `Prod`, `c41a…` |
| Flow variable | `env3Name`/`env3Id`, `env4Name`/`env4Id` | No | Third and fourth. A slot with an empty ID is unused and its button is hidden, whatever its name | `Test`, `Stage` |
| Flow variable | `gitlabProjectId` | Yes | Numeric ID of the one promotion project | `12345678` |
| Flow variable | `env1GitlabEnvironment` … `env4GitlabEnvironment` | No | GitLab environment that deploys **to** that environment; empty = `gitlabEnvironment` | `dev`, `test` |
| Flow variable | `gitlabUrl`, `gitlabEnvironment` | Preset | `https://gitlab.com`, `production` | |
| Flow variable | `apiOrigin`, `authOrigin`, `sourceRegion`, `targetRegion` | Preset | North America: `https://api.pingone.com`, `https://auth.pingone.com`, `us`, `us` | |
| Each node | Connections | Yes | HTTP, Functions, Variables and Flow Conductor connections ([table](#2-import)) | |
| PingOne | Worker app roles | Yes | **Promotion Admin** on every environment in `env1Id` … `env4Id` | |
| GitLab | One environment per target in the promotion project | Yes | Scoped `PINGONE_TARGET_ENVIRONMENT_ID` per GitLab environment, pipeline from `gitlab/make_pipeline.py` ([details](RUNBOOK.md#one-project-several-environments)) | |

## Updating from an earlier version

If you imported a previous file, this is what's different:

1. **Re-import** the new file (and delete the old flow). Importing resets flow variables to placeholders, so set the flow variables in the checklist again. The two **company** secrets carry over; don't touch them.
2. **`sourceEnvironmentId` and `targetEnvironmentId` are no longer settings.** The requester picks them at run time from the `envN…` slots. Put your environments in `env1Name`/`env1Id` … `env4Name`/`env4Id` instead.
3. **One GitLab project.** `env1GitlabProjectId` … `env4GitlabProjectId` are gone; every request goes to `gitlabProjectId`. New and optional: `env1GitlabEnvironment` … `env4GitlabEnvironment`, the GitLab environment in that project that deploys to each target.
4. **Debug logging:** the file asks for Debug on every flow, but DaVinci may keep its default (Error) on import. Check **Flow → Settings → Logging** on all five flows and set **Debug** for the proof of concept. Debug logs include node inputs and outputs (tokens among them), so set it back to Info afterwards.
5. **Every screen is styled, and failures show a named error screen** instead of DaVinci's generic error page. Each screen's styles are in its node's **CSS** field and the auto-continue in its **Script** field, as in Ping's own flows (DaVinci ignores `<style>`/`<script>` inside the HTML).
6. **Resource picker like the local app:** search, type filter, dependencies and destination-mapping dropdowns with suggestions. The main flow also reads PingOne's saved promotion mappings for the chosen pair, so the worker app needs read access to promotion mappings in the source environment (a failure only shows a warning).
7. **The flow is now a main flow plus four subflows** in one import file. Delete the old single flow, import, point the Flow Conductor nodes at your Flow Conductor connection, and deploy all five. Set the flow variables on the **main** flow only; the subflows get their values from it.
8. **Worker roles:** the worker app needs Promotion Admin on **every** listed environment, not just two.
9. **Nothing to fix by hand in the REST nodes any more.** The body fix you made on the token node is now in the file for all six POST nodes.
10. **Expect a few "working" pages** while snapshots are prepared and read. They continue by themselves; click **Continue** if one doesn't.

## 1. Before importing

1. **Worker app.** In the DaVinci host environment, add a **Worker** application. Give it **Promotion Admin** on each environment you'll list (all four slots), the role set that fixed the pipeline worker's Configuration Management 403s.
2. **GitLab token.** In the promotion project: **Settings → Access tokens**, role **Developer**, scope **api**. Note its expiry date.
3. **Connection IDs.** In **DaVinci → Connections**, note the IDs of the HTTP, Functions, Variables and **Flow Conductor** connections the flows should use.

## 2. Import

**Flows → Add Flow → Import from JSON**, choose the file. It holds five flows, imported together: the main flow **PingOne Promote - Request** and its subflows **1 Choose environments**, **2 Snapshot environment**, **3 Choose resources and review** and **4 Submit to GitLab**.

The flows' nodes point at placeholder connections:

| Placeholder ID | Connection |
| --- | --- |
| `a1f0d00a000000000000000000000002` | HTTP |
| `a1f0d00a000000000000000000000003` | Functions |
| `a1f0d00a000000000000000000000005` | Variables |
| `a1f0d00a000000000000000000000006` | Flow Conductor (the main flow's four subflow nodes) |

To skip re-pointing nodes after import, generate your own copy with your connection IDs filled in (below).

## 3. Set the variables and settings

**Company variables** (DaVinci → Variables; the import creates them empty):

| Name | Value |
| --- | --- |
| `promoteWorkerBasicAuth` | `printf '%s:%s' "$WORKER_CLIENT_ID" "$WORKER_CLIENT_SECRET" \| base64` |
| `promoteGitlabToken` | The GitLab token from step 1 |

**Flow variables** (the flow's Variables panel):

| Name | Value |
| --- | --- |
| `hostEnvironmentId` | Environment holding the worker app (used for its token URL) |
| `env1Name` / `env1Id` … `env4Name` / `env4Id` | Up to four environments. The requester picks source and target from these as buttons labelled with the names. Leave a pair empty to hide that slot. |
| `env1GitlabEnvironment` … `env4GitlabEnvironment` | Optional: the GitLab environment (in the promotion project) that deploys to that environment, named in the merge request. Empty = `gitlabEnvironment`. |
| `gitlabUrl` | `https://gitlab.com`, already set |
| `gitlabProjectId` | The promotion project's numeric ID |
| `gitlabEnvironment` | `production`, already set |
| `apiOrigin` / `authOrigin` / `sourceRegion` / `targetRegion` | Preset for North America (`pingone.com`, `us`); change for other regions |

`adminEnvironmentId`, `organizationId` and `davinciAdminRoleId` are only used when sign-in is on.

> **One GitLab project for every environment.** All requests go to `gitlabProjectId`. In that project, each environment you deploy to is a GitLab environment (`dev`, `test`, `production` …) with its own scoped variables and approvals, and the pipeline from `gitlab/make_pipeline.py` runs each environment's jobs only for requests that target it. Name a target's GitLab environment in `envNGitlabEnvironment` (Terraform: `gitlab_environment` in `environments`); empty uses `gitlabEnvironment`. See the runbook's [One project, several environments](RUNBOOK.md#one-project-several-environments).

## 4. Deploy and launch

**Deploy all five flows**, the four subflows first (a subflow node runs the subflow's deployed version). Then, in **DaVinci → Applications**, add an application with a flow policy that runs the **main flow** at 100% weight, and launch it the way your team launches other DaVinci flows. Keep that policy limited to POC testers while sign-in is off.

## 5. First run

1. Launch. If a page titled **Settings need attention** appears, it names each flow variable that is still a placeholder or malformed; fix those, deploy and launch again. Re-importing resets flow variables to the file's values, so expect this after every re-import.
2. Pick the source, then the target. **Choose resources** lists both environments' resources.
3. Pick one low-risk resource and build: **Review request** shows the promotion-variable check and the redacted `request.json`.
4. Submit: **Merge request opened** links to an MR adding `requests/<id>/request.json`.
5. In GitLab, `validate_request` passes; after merge, the normal pipeline plans the promotion.

Errors and fixes are in the runbook's [troubleshooting table](RUNBOOK.md#troubleshooting).

## Generate a pre-filled copy (optional)

Copy `values.example.json` to a private `values.json`, outside the repo or uncommitted, and fill in your IDs, including the `…ConnectionId` keys (`flowConnectionId` is the Flow Conductor connection). Then run:

```sh
python3 gen_flows.py out values.json && python3 check_flows.py out/*.json
```

`out/pingone-promote-request.flow.json` then imports with your connections and values already set. Keep that file private; it contains your tenant IDs.
