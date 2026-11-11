# Hosted live acceptance cases

Status on `feat/server`: **unverified**. No tenant or GitLab live acceptance run is claimed. Fixture tests cannot establish tenant API permissions, exporter completeness, provider behavior, GitLab policies, or host deployment behavior. Run these cases only in a disposable test tenant and a non-production configuration project. Record the app/provider versions, permissions, results, and redacted evidence without committing credentials or tenant data.

## Export and selection

The provider inventory is pinned to `pingidentity/terraform-provider-pingone` 1.24.0. The read-only provider importer is a proposed implementation and is not claimed built by this document. These cases remain blocked until that implementation and its family inventory exist.

- For every supported provider resource family, create exactly one representative item in the source, including one notification template. Search, select that item alone, preview and confirm that its address, configuration, dependencies and secrets are handled by the declared policy. Record each family separately; no family may be inferred from another family's fixture.
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
| Linux container, amd64 and arm64 | Image runs as non-root with read-only root and private writable `/tmp`; init copy gives service-owned mode 0600 secrets; one pod can preview and submit. | Unverified |
| RHEL 9 systemd | Hardened unit starts as dedicated account; private temp works; SELinux remains enforcing; journald, restart and local proxy path work. | Unverified |

Do not call exporter coverage complete, release-ready, or production-safe until the provider importer exists, every supported family case has a recorded result, all environment behavior is verified, and the deployment rows have been run on their actual hosts.
