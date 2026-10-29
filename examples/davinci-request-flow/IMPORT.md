# Import the DaVinci Request flow (proof of concept)

The import file is [`export/pingone-promote-request.flow.json`](export/pingone-promote-request.flow.json). Sign-in is **off**: the flow starts at the resource picker, and the GitLab MR and deployment approvals still gate every change. No Terraform needed.

## 1. Before importing

1. **Worker app.** In the DaVinci host environment, add a **Worker** application. Give it **Promotion Admin** on the source and target environments, the role set that fixed the pipeline worker's Configuration Management 403s.
2. **GitLab token.** In the promotion project: **Settings → Access tokens**, role **Developer**, scope **api**. Note its expiry date.
3. **Connection IDs.** In **DaVinci → Connections**, note the IDs of the HTTP, Functions, Error Message and Variables connections the flow should use.

## 2. Import

**Flows → Add Flow → Import from JSON**, choose the file.

The flow's nodes point at placeholder connections:

| Placeholder ID | Connection |
| --- | --- |
| `a1f0d00a000000000000000000000002` | HTTP |
| `a1f0d00a000000000000000000000003` | Functions |
| `a1f0d00a000000000000000000000004` | Error Message |
| `a1f0d00a000000000000000000000005` | Variables |

To skip re-pointing nodes after import, generate your own copy with your connection IDs filled in (below).

## 3. Set the variables

**Company variables** (DaVinci → Variables; the import creates them empty):

| Name | Value |
| --- | --- |
| `promoteWorkerBasicAuth` | `printf '%s:%s' "$WORKER_CLIENT_ID" "$WORKER_CLIENT_SECRET" \| base64` |
| `promoteGitlabToken` | The GitLab token from step 1 |

**Flow variables** (the flow's Variables panel):

| Name | Value |
| --- | --- |
| `hostEnvironmentId` | Environment running the flow and holding the worker |
| `sourceEnvironmentId` / `targetEnvironmentId` | Environments you promote between |
| `gitlabUrl` | `https://gitlab.com`, already set |
| `gitlabProjectId` | Numeric project ID of the private promotion-requests project |
| `gitlabEnvironment` | `production`, already set |
| `apiOrigin` / `authOrigin` / `sourceRegion` / `targetRegion` | Preset for North America (`pingone.com`, `us`); change for other regions |

`adminEnvironmentId`, `organizationId` and `davinciAdminRoleId` are only used when sign-in is on.

## 4. Deploy and launch

**Deploy** the flow. Then, in **DaVinci → Applications**, add an application with a flow policy that runs it at 100% weight, and launch it the way your team launches other DaVinci flows. Keep that policy limited to POC testers while sign-in is off.

## 5. First run

1. Launch: **Choose resources** lists the source and target resources.
2. Pick one low-risk resource and build: **Review request** shows the promotion-variable check and the redacted `request.json`.
3. Submit: **Merge request opened** links to an MR adding `requests/<id>/request.json`.
4. In GitLab, `validate_request` passes; after merge, the normal pipeline plans the promotion.

Errors and fixes are in the runbook's [troubleshooting table](RUNBOOK.md#troubleshooting).

## Generate a pre-filled copy (optional)

Copy `values.example.json` to a private `values.json`, outside the repo or uncommitted, and fill in your IDs, including the four `…ConnectionId` keys. Then run:

```sh
python3 gen_flows.py out values.json && python3 check_flows.py out/*.json
```

`out/pingone-promote-request.flow.json` then imports with your connections and values already set. Keep that file private; it contains your tenant IDs.
