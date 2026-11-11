# Live acceptance: hosted server (`pingone-promote serve`)

Fixture tests (`go test ./internal/server/...`) cover the HTTP surface against a fake GitLab and a fake terraformer. They do not prove real GitLab API behavior, real tenant exports, enterprise pipeline naming, or behavior on macOS/RHEL. Run these cases against a real GitLab project and a non-production PingOne tenant. Send back status words, counts and timings only: no tokens, client secrets, environment IDs or flow contents.

**Stop rule.** If a **[STOP]** case fails, stop and report.

## Unverified GitLab API assumptions

The Requests page and submit depend on these. Each one has a case below.

| # | Assumption | Code | Case |
| --- | --- | --- | --- |
| A1 | `GET /projects/:id/repository/branches?search=^PREFIX` returns only branches starting with PREFIX (the `^` anchor works on your GitLab version). The server also re-filters by prefix. | `gitlab.ListRequestBranches` | R1 |
| A2 | `GET /projects/:id/merge_requests?state=all&labels=L&order_by=updated_at&sort=desc` returns merged MRs with `merge_commit_sha` (or `squash_commit_sha` when squash is forced by project settings). | `gitlab.ListRequestMRs` | R2 |
| A3 | `GET /projects/:id/pipelines?sha=SHA&ref=DEFAULT&order_by=id&sort=desc` returns the default-branch pipeline of the merge commit. With merged-results or merge-train pipelines the merge commit SHA can differ from the pipeline SHA; then the Requests page shows "No pipeline has run". | `gitlab.CommitPipeline` | R3 |
| A4 | `GET /projects/:id/pipelines/:pid/jobs` lists the latest attempt of each job by `name`, and the enterprise apply jobs are named by `applyJobPattern` (default `apply:{env}`). Jobs in child/downstream pipelines (bridges) are NOT read. | `gitlab.PipelineJobs` | R3 |
| A5 | Job `status` values: `success`, `failed`, `canceled`, `manual`, `running`, `pending`, `preparing`, `waiting_for_resource`, `created`, `scheduled`, `skipped`. They map to passed / failed / manual / running / not started. | `server.rolloutState` | R3 |
| A6 | The project access token (Developer, `api`) can create `PREFIX<id>` branches, commit, and open MRs, and cannot push to the protected default branch. | `gitlab.HostedCommitter` | S1, S4 |
| A7 | A browser posting a same-origin form sends `Sec-Fetch-Site: same-origin` (or a matching Origin/Referer). Under `Referrer-Policy: no-referrer` the Origin is `null`, so a browser without Fetch Metadata (Safari before 16.4) is refused. | `server.sameOrigin` | H3 |

## 0. Setup

1. Build on the host: `CGO_ENABLED=0 go build ./cmd/pingone-promote`.
2. Write the configuration (JSON, no secrets) with `sources`, `rollout`, `gitlab.baseUrl/projectId/mrLabels`, `pingone.regionCode/workerEnvironmentId`, pinned `tools`, `branchPrefix`, `applyJobPattern` matching your pipeline.
3. Write the secrets file (`PINGONE_PROMOTE_WORKER_CLIENT_ID`, `..._CLIENT_SECRET`, `PINGONE_PROMOTE_GITLAB_TOKEN`), `chmod 600`, owned by the service user.
4. **[STOP] C1.** `pingone-promote serve --config FILE --check-config` prints "usable" and exits 0. Change the secrets file to mode 0644: it is refused and no value is printed.
5. Start `pingone-promote serve --config FILE`. `curl -s http://127.0.0.1:8080/healthz` returns `ok`.

## 1. HTTP hardening

- **H1.** `curl -sI -H 'Host: evil.example' http://127.0.0.1:8080/` returns 421. With a name from `allowedHosts` it returns 200.
- **H2.** Every page (`/`, a draft, `/requests`, `/static/style.css`) has the CSP, `nosniff`, `no-referrer`, `no-store` and `same-origin` opener headers (browser devtools, Network tab).
- **H3.** In Chrome, Firefox and Safari (16.4+) the forms work. A form on another origin that posts to `/drafts` is refused (403).
- **H4.** With TLS configured, the cookie is `__Host-pingone-promote`, `Secure`, `HttpOnly`, `SameSite=Strict`.

## 2. Search and preview

- **P1 [STOP].** New request, type your name, source `dev`. Search a known flow by name, then by its ID: the results show type, name and ID. Add it.
- **P2.** Preview. Expect: summary by type, the change table with statuses and actions, readiness per environment, required secret names and Vault paths (no values), the diff, and the warning about reverts and `prevent_destroy`.
- **P3.** Set a dependency to "exclude" and apply. The preview runs again; if Git does not manage it, every rollout environment needs its ID and shows a form.
- **P4.** Type a private-key header as a value: it is refused with "looks like a secret" and is not shown again. Fill the real values: the preview has no blockers.
- **P5.** Whole environment without the confirmation checkbox is refused; with it, the preview lists counts by type.

## 3. Submit

- **S1 [STOP].** Submit as merge request. Expect links to the MR, branch `PREFIX<YYYYMMDD-HHMMSS-xxxxxx>` and commit. The commit trailer reads `Requested-by: NAME (unverified)`. The MR carries the configured label. Only Terraform files, tfvars and `customer_secrets.yml` changed; no secret value is in any file.
- **S2.** Submit another request as branch only: no MR is opened.
- **S3.** A request whose items already match Git: "Nothing was committed", no branch.
- **S4.** Preview, then merge something else to the default branch, then submit: "The default branch changed after the preview. Preview again." No request branch is left. Preview again and submit: it succeeds.
- **S5.** During a submit, cut the network to GitLab: the page reports that the result is uncertain and the branch is kept for checking.
- **S6.** The exporter runs during preview only (check the service log timing or tenant audit log): submit reuses the previewed export.

## 4. Requests and rollout

- **R1.** `/requests` lists the request branches and MRs (newest first).
- **R2.** Merge S1's MR. The row shows `merged`.
- **R3.** As the pipeline progresses, each rollout column moves through not started / running / manual / passed / failed, in rollout order, linking to the job.

## 5. Process

- **O1.** `kill -TERM` the server during a preview: it exits within 15 seconds and leaves no snapshot directory under the temporary directory.
- **O2.** The service log contains method, path, status and timing only; grep it for the client secret and token: no match.

## Results form

| Case | Result (pass/fail/skip) | Notes (sanitized) |
| --- | --- | --- |
| C1 | | |
| H1–H4 | | |
| P1–P5 | | |
| S1–S6 | | |
| R1–R3 | | |
| O1–O2 | | |
| A1–A7 confirmed? | | |
