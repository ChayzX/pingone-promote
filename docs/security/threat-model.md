# Threat model: hosted server (`pingone-promote serve`)

Scope: the phase-1 hosted server (ADR 0018), its GitLab and PingOne access, and its deployment artefacts in `deploy/`. Phase 1 has no sign-in. This document records what the server protects, what it does not, and which controls are elsewhere.

## Assets

| Asset | Where it lives | Why it matters |
| --- | --- | --- |
| PingOne worker credentials (read-only source worker) | Secrets file only (0600/0400, owner = service user), process memory, the exporter child's environment | Read access to the source tenant's configuration, which can include sensitive settings |
| GitLab project token (Developer, `api`) | Secrets file, process memory | Can create branches, commits and merge requests in the configuration repository |
| Configuration repository (HCL, tfvars, `customer_secrets.yml`) | GitLab | The source of truth that the enterprise pipeline applies to every environment |
| Source environment configuration | Read on demand; drafts and previews in memory | Can include tenant names, URLs and IDs |
| Drafts and previews | Server memory, bound to a browser session, expiring | Work in progress of one person |

The server never holds PingOne write credentials or rollout-environment credentials, and never plans or applies.

## Attackers

1. **A website the user visits** (CSRF, cross-site subresource or frame requests, session flooding).
2. **Someone who can reach the port**: on the network when exposed, or any local user of the host even on loopback.
3. **A requester** who types a misleading name or description, or picks content to hide something from reviewers.
4. **Tenant content**: names, descriptions and values read from the source environment, which reach commit messages, MR text, pages and logs.
5. **A compromised or buggy exporter tool**, whose stderr can quote credentials or tenant data.
6. **A local attacker on the host** who can swap files the server reads (secrets file, tools).

## Trust boundaries

- **Browser to server.** Every response has a strict CSP (no script), `nosniff`, `Referrer-Policy: same-origin`, `no-store`, COOP and CORP `same-origin`, and `frame-ancestors 'none'`. The Host header must be in an exact allow-list; forwarded host headers are ignored.
- **Server to GitLab.** HTTPS only, no redirects, responses bounded. Writes are limited in code to `<branchPrefix><requestId>` branches and the Terraform and secret-declaration path allow-list. The server refuses an existing protected branch under the prefix.
- **Server to exporter.** The pinned binary is checksum-verified before every exec, gets only an allow-listed environment, and its output is bounded.
- **Server to pipeline.** Through Git only. The merge request is the review point.

## Mitigations

| Threat | Mitigation |
| --- | --- |
| CSRF | Random per-session token on every POST, constant-time compare. Plus same-origin evidence: `Sec-Fetch-Site: same-origin`, or, without Fetch Metadata, an `Origin` exactly equal to this server's origin. `Origin: null` and a missing Origin are refused. |
| Cross-site subresources, frames, fetches | Any request with `Sec-Fetch-Site: cross-site`/`same-site` is refused unless it is a top-level GET navigation. A cross-site navigation without a session cookie gets a page that reloads from this site; it never creates a session. |
| Session flooding or draft eviction | Sessions are created only on same-origin requests, on direct navigation (a typed URL or bookmark, `Sec-Fetch-Site: none`), or on requests without fetch metadata. A full session table evicts expired or draft-less sessions only; when every session owns drafts, new sessions get 503. An expired session's drafts are dropped. |
| GitLab request flooding through /requests | Cross-site subresource requests are refused before any work, and GitLab reads are cached for 30 seconds. |
| Spoofed MR description | The generated summary comes first. The requester's text follows under "Requester's note" in a fenced `text` block, with backticks and `~~~` neutralized so it cannot close the fence. The real trailers end the description. |
| Tenant names injecting trailers or lines | Control characters and line separators in names are replaced with spaces in commit messages and MR titles. |
| Secrets in pages, logs or results | Exporter errors become fixed plain-language messages; the raw text is logged at debug level only, after heuristic redaction (private keys, JWTs, bearer and basic credentials, secret-named literals; heuristics can miss an unusual secret format) and exact removal of the loaded worker secret, client ID and GitLab token. Every result, page and log line also passes the exact-value filter. The exporter replaces the client secret in stderr before cutting it to 64 KiB. Values that look like secrets are refused on the form and never shown again. |
| Panics | Handler panics release the drafts lock and return a plain 500 page; job panics become a generic job error. Neither the panic value nor a stack trace is logged or shown. |
| Exposed listen address | A non-loopback `listen` is refused unless `exposure` is `tls` (with certificate and key) or `behind-proxy` (with `allowedHosts`). Either value logs a no-sign-in warning at startup. |
| Secrets file swap or weak permissions | Lstat, then open with `O_NOFOLLOW|O_NONBLOCK`, then `SameFile`. The file must be regular, mode 0600/0400 and owned by the service user. Symbolic links, including Kubernetes Secret volume entries, are refused; the manifest uses an init-container copy. |
| Tool swap | Tool digests are pinned in the configuration and checked at `--check-config` and before every export. |
| Reviewed content differs from committed content | Submit replays the export captured at preview, roots the branch at the previewed default-branch SHA, and refuses if the inputs or the default branch changed. |

## Accepted phase-1 risks

- **No sign-in.** Anyone who can reach the port can file requests under any typed name. Names are recorded as "(unverified)" and confer no authority.
- **Loopback is not isolation.** Binding to `127.0.0.1` keeps out the network, not other local users or processes on the same host. Run the server on a host or pod dedicated to it.
- **Merge request review is the control.** The server can only propose changes. Protect the default branch, require approvals, and inspect the enterprise plan before any apply. A Git revert can delete resources in Terraform: fix forward instead.
- **Old browsers.** A browser that sends neither Fetch Metadata nor a correct `Origin` on form posts cannot submit forms.
- **In-memory drafts.** A restart loses unfinished drafts; one replica only.

## Out of scope

The enterprise pipeline, its state backends and Vault policies; GitLab's own access control; and PingOne tenant administration.
