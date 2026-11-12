# Hosted live acceptance cases

Status on `feat/server`: **unverified**. No tenant or GitLab live acceptance run is claimed. Fixture tests cannot establish tenant API permissions, exporter completeness, provider behavior, GitLab policies, or host deployment behavior. Run these cases only in a disposable test tenant and a non-production configuration project. Record the app/provider versions, permissions, results, and redacted evidence without committing credentials or tenant data.

## Export and selection

The provider inventory is pinned to `pingidentity/terraform-provider-pingone` 1.24.0. The read-only provider importer, allowlisted API inventory, schema renderer and cross-service graph are implemented. Local fixtures cover their boundaries, including a real pinned Terraform/provider population read against a loopback fake API. The following cases still require an actual tenant and GitLab project; implementation is not live acceptance evidence.

- For every supported provider resource family, create exactly one representative item in the source, including one notification template. Search, select that item alone, preview and confirm that its address, configuration, dependencies and secrets are handled by the declared policy. Record each family separately; no family may be inferred from another family's fixture.
- Search for an unreferenced image by its exact UUID, select it, search again with a different name, and confirm its choice survives. Preview with and without dependencies. The server may fetch only the original public image from the configured region and source environment; unsafe URLs, redirects and assets exceeding 4 MiB each or 8 MiB total must fail safely. Confirm missing images leave other catalog results available. A whole export cannot discover an unreferenced image because PingOne has no image list endpoint.
- Use an unreadable required credential and confirm preview asks for a Vault secret name rather than a secret value. Use a missing required nonsecret field and confirm every rollout environment must supply it. Generated client secrets must remain computed provider outputs.
- Select no dependencies and verify only explicitly selected objects appear. Then enable transitive dependencies and verify only actual dependencies are added. Repeat with each dependency explicitly excluded and confirm the behavior is clearly reported and cannot silently create an incomplete request.
- Exercise include lists, exclude lists, and include/exclude overlap. Confirm exclusions win or the overlap is rejected according to the documented UI contract, and that no excluded resource is emitted.
- Exercise references from DaVinci objects to core PingOne objects and from core objects to DaVinci objects. Verify both directions produce valid addresses and complete dependency graphs without copying users, sessions, user data, audit logs, or environment-specific generated client secrets.
- Verify built-in/default objects use explicit adoption IDs. Remove a required adoption ID and confirm readiness is blocked instead of recreating the built-in object.

## Merge and publish

- Submit an unchanged selected item against an existing equal definition and confirm the preview is a no-op with no unnecessary file write.
- Change an existing resource and verify the preview presents keep/update/exclude choices. Keep must retain the Git version; update must replace only the selected definition; exclude must omit it.
- Introduce a conflicting definition and verify it cannot be submitted without an explicit supported resolution. Confirm missing values for every rollout environment block submission.
- Move the GitLab default branch after preview and before submit. Submission must require a new preview rooted at the new immutable SHA.
- Simulate a concurrent write or uncertain commit response. The server must preserve the request branch for reconciliation and must not delete or overwrite an uncertain/concurrent write.
- Verify a merge request uses only the configured request branch namespace and allowed Terraform/secret-declaration paths. Confirm enterprise CI alone plans/applies after normal review and merge.
- Change the rollout, fill a previously missing secret reference, and verify the preview shows the exact resulting HCL and secret declaration. Confirm secret values never appear in the preview, GitLab diff, service logs, or error output.

## HTTP and identity

- Confirm the default bind is `127.0.0.1:8080`. A forged `X-Forwarded-Host` or forwarded scheme does not alter Host validation, generated origin checks or cookie behavior.
- Try an unlisted Host, missing/invalid CSRF token, cross-origin POST, and an unexpected POST method on each mutating route. Each request must be rejected without changing draft or GitLab state.
- Type a name and confirm every UI and durable record labels it `(unverified)`. Confirm the name confers no access to another session's draft and is never presented as authenticated identity.
- Inspect response headers and rendered pages for strict CSP, escaped user data, private/no-store caching, HttpOnly/SameSite cookies, and absence of inline script allowances.
- Restart the process with an open draft. Confirm the draft is gone and that previously submitted requests in GitLab remain visible.

## Deployment matrix

Run the same source binary version, config values and pinned tool versions in each row; record only the expected path/listener differences.

| Runtime | Required live proof | Status |
| --- | --- | --- |
| macOS local, amd64 and arm64 | Installer uses the matching locked assets; mode 0400/0600 secrets load; loopback UI, preview, submit and shutdown work. | Unverified |
| macOS Podman, Apple Silicon and Intel | Build in the Podman Linux VM, install matching Linux tools, run non-root with read-only root/private tmpfs, verify loopback Host/CSP and complete preview/publication. See the Podman steps in the deployment guide. | Unverified |
| Linux container, amd64 and arm64 | Image runs as non-root with read-only root and private writable `/tmp`; init copy gives service-owned mode 0600 secrets; one pod can preview and submit. | Unverified |
| RHEL 9 systemd | Hardened unit starts as dedicated account; private temp works; SELinux remains enforcing; journald, restart and local proxy path work. | Unverified |

Do not call exporter coverage complete, release-ready, or production-safe until every supported family case has a recorded result, all environment behavior is verified, and the deployment rows have been run on their actual hosts.

## Recorded offline deployment evidence

On 2026-11-11 the final Containerfile built and its Linux amd64 image started with all pinned tools, including Ping CLI. It served the new-request page with strict CSP and rejected a hostile Host, using UID 10001, a read-only root/config/tools, a 2 GiB executable tmpfs, a 2 GiB memory limit and a service-owned mode0600 fixture secrets file. This rerun verified the clean-home PingCLI version fix. Pod YAML parsed with a client-side parser. These observations do not prove Podman on macOS, Kubernetes scheduling, RHEL SELinux/systemd operation, or tenant/GitLab publication.

## Repeatable operator run

1. Record the tested Git commit, operating system, architecture, tool hashes and provider version. Follow the matching deployment guide. Use a disposable source environment and a non-production GitLab configuration project with separate state backends and root tfvars for every rollout environment.
2. Give the source worker only read permissions. Verify a harmless configuration read succeeds and a configuration write is denied using an independent administrative test; do not grant administrator write access to bypass an exporter permission error. Restrict the GitLab credential through project policy or a constrained gateway, then independently verify it cannot write the default branch or branches outside the configured request prefix. Do not create a tool CI job or add pipeline credentials to this server.
3. Install the pinned tools, load the owner-only server secrets file, and open the loopback page. Confirm a hostile Host returns 400 and an ordinary page carries the documented CSP. For Podman, use the local smoke steps first with dummy credentials and `.invalid` service URLs, then replace only the sensitive file for the authorized integration run.
4. Create one known notification template content in the source. Choose that source, selected resources, and branch-only delivery. Search its exact name or ID; select just that result and save. Preview without upstream dependencies, then start a second draft with upstream dependencies enabled. Compare the declared dependency sets with the source item's actual references.
5. In preview, enter each missing nonsecret value as JSON for every named rollout environment. Supply secret names only; store their values separately at the declared Vault paths. Save, preview again, and confirm every environment is ready. Verify no readable source credential, client secret, literal import block or auto tfvars file appears in the review.
6. Submit the first draft as a branch. Inspect all committed files: shared HCL, root environment tfvars, conditional static-address imports and customer_secrets.yml. Check Requested-by includes `(unverified)` and the Request-id trailer matches the displayed request. Repeat using merge-request delivery and inspect its branch base and ordinary enterprise plan results.
7. Repeat an unchanged request and confirm no new commit. For a changed source item already different in Git, confirm the default keeps Git, then explicitly choose Update and inspect only that block's update. In a separate draft, move the default branch after preview; submit must require a fresh preview. Open Requests and compare each environment's status with actual GitLab deployment/job records; absence of evidence must display unknown.
8. Run the family, cross-service, defaults, image and missing-secret cases above separately. Have the ordinary enterprise pipeline review and deploy the merged commit through its configured rollout; the hosted process must never plan or apply. Inspect provider-injected defaults and DaVinci published/current-version behavior in those enterprise plans. Retain only redacted evidence, and use a reviewed roll-forward correction rather than an unreviewed revert if recovery is needed.
