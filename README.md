# PingOne Promote

## What it does

PingOne Promote lets you pick a SOURCE PingOne environment and export any of its configuration (one notification template, one application, one flow, or the whole environment) as Terraform into your configuration repository. It then opens a merge request in GitLab with the generated files and the values each later environment still needs. Your existing pipeline rolls the merged change out; PingOne Promote never changes PingOne and never runs Terraform plans or applies.

Phase 1 has no sign-in: anyone who can reach the web page can use it, and a typed name is recorded as `NAME (unverified)`. Keep it on your own machine or behind an authenticating proxy (see [docs/operations.md](docs/operations.md)).

## Before you start

You need:

1. **A read-only PingOne worker.** A worker application in the source environment whose roles can only read configuration. Its client ID and secret go in the secrets file. Never give it a write role.
2. **A GitLab token** that can only create branches, commits and merge requests on the one config project. Protect the project's default branch.
3. **The config repository layout** the pipeline already uses: `definitions/`, one `<env>.tfvars` per environment at the root (for example `development.tfvars`, `testing.tfvars`), and `customer_secrets.yml`.
4. **Your environment names**: the source environment(s) people may export from, and the rollout list (the environments the pipeline applies afterwards, in order). They must not overlap.
5. **Pinned tools**, downloaded and checksum-verified by `scripts/fetch-tools.sh` (Terraform 1.16.7 and pingcli-terraformer 0.6.0).

The hosted server needs no CI job, CI variable or special branch in the config repository.

## Checklist: macOS (local test)

**Recommended: test with Podman** so the Mac runs the same container a server does. Follow [docs/podman-mac.md](docs/podman-mac.md) step by step (about 20 minutes). The steps below run the binary directly instead.

Full detail: [docs/operations.md](docs/operations.md#macos-local-test).

1. `bash scripts/fetch-tools.sh --dest ./tools`
2. `shasum -a 256 tools/terraform` (copy the result into `terraformSha256`).
3. `go build -o pingone-promote ./cmd/pingone-promote`
4. `cp deploy/config.example.json config.json` and edit it (GitLab, PingOne region and worker environment, `sources`, `rollout`, absolute tool paths, both digests).
5. `cp deploy/secrets.example.env secrets.env && chmod 600 secrets.env`, then fill in the three values.
6. `./pingone-promote serve --config config.json --check-config`
7. `./pingone-promote serve --config config.json`, then open http://127.0.0.1:8080/

## Checklist: container or pod

Full detail: [docs/operations.md](docs/operations.md#container-and-kubernetes).

1. Pin both base images by digest in `deploy/container/Containerfile`.
2. `podman build --format docker -f deploy/container/Containerfile -t pingone-promote:dev .`
3. Get the Terraform digest: `podman run --rm --entrypoint sha256sum pingone-promote:dev /opt/pingone-promote/tools/terraform`
4. Edit the ConfigMap in `deploy/kubernetes/deployment.yaml`: `gitlab`, `pingone`, `sources`, `rollout`, `terraformSha256`, and `allowedHosts` (your Ingress host name).
5. `kubectl create namespace pingone-promote`
6. `kubectl -n pingone-promote create secret generic pingone-promote-secrets --from-file=secrets.env=./secrets.env`
7. `kubectl apply -f deploy/kubernetes/deployment.yaml`, then replace the CIDRs in `deploy/kubernetes/networkpolicy.yaml` and apply it.
8. Put an authenticating proxy or private network in front and list its host name in `allowedHosts`. Inside a pod the server listens on `0.0.0.0:8080` with `"exposure": "behind-proxy"` (the example ConfigMap sets both); without `exposure` the server refuses any address other than loopback. Run exactly one replica.

## Checklist: RHEL 9 (systemd)

Full detail: [docs/operations.md](docs/operations.md#rhel-9-install-systemd). Run as root.

1. `useradd --system --home-dir /nonexistent --shell /sbin/nologin pingone-promote`
2. `bash scripts/fetch-tools.sh --dest /opt/pingone-promote/tools`, then `sha256sum /opt/pingone-promote/tools/terraform`
3. `CGO_ENABLED=0 go build -trimpath -o /opt/pingone-promote/pingone-promote ./cmd/pingone-promote`
4. Install `deploy/config.example.json` as `/etc/pingone-promote/config.json` (mode 0640) and `deploy/secrets.example.env` as `/etc/pingone-promote/secrets.env` (mode 0600); edit both.
5. Label the files: `semanage fcontext -a -t bin_t '/opt/pingone-promote(/.*)?' && restorecon -Rv /opt/pingone-promote`
6. `install -m 0644 deploy/systemd/pingone-promote.service /etc/systemd/system/`
7. `systemctl daemon-reload && systemctl enable --now pingone-promote`
8. `journalctl -u pingone-promote -f` and check for a clean start.
9. Keep `listen` on `127.0.0.1:8080` and publish through an authenticating reverse proxy on 443 with its name in `allowedHosts`. Only if the server itself must listen on another address, set `"exposure": "tls"` (with a certificate) or `"behind-proxy"`; see [operations](docs/operations.md).

## Daily use

1. **New request.** Open the page, type your name, choose the source environment and what to promote (one item, a group, or the whole environment, with or without what it depends on).
2. **Preview.** Wait for the search and preview. Read the list of changes, the diff and the warnings.
3. **Fill in missing values.** For every rollout environment, enter the values the preview asks for (redirect URIs, domains, IDs of built-in objects). Submission stays blocked until none are missing.
4. **Submit.** Submit the preview you reviewed. A merge request opens in GitLab and the Requests page tracks it.
5. **Watch the rollout.** Review and merge the request; the Requests page shows each environment's apply job as the pipeline runs.
6. **Never revert; fix forward.** If something is wrong after merging, make a new request that corrects it. Do not use Git revert. See [docs/recovery.md](docs/recovery.md).

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| Start fails: "the secrets file must be readable by its owner only (mode 0600 or 0400...)" | The secrets file has group or other access (0440 and 0640 are refused too). | `chmod 600` the file. In Kubernetes the init container does this; see operations. |
| Start fails: "the secrets file must be owned by the user the server runs as" or "must not be a symbolic link" | Wrong owner, or the path is a symlink. | `chown` it to the service user; point `secretsFile` at the real file. |
| Start fails: "secrets file line N: unknown name" | A line is not one of the three known names, or has no `=`. | Use only `PINGONE_PROMOTE_WORKER_CLIENT_ID`, `..._WORKER_CLIENT_SECRET`, `..._GITLAB_TOKEN`. |
| Start fails: "tools.terraform does not match tools.terraformSha256" | Digest still the placeholder, or the tool changed. | Recompute with `sha256sum` and update the config. |
| Start fails: "terraformer binary does not report the pinned version" (or checksum does not match) | Wrong pingcli-terraformer build. | Re-run `scripts/fetch-tools.sh`; keep `terraformerVersion` 0.6.0 and its digest. |
| Browser: "This server does not answer for that host name." (421) | The URL's name is not in `allowedHosts`. | Add it to `allowedHosts`, restart; the proxy must pass the original `Host` header. |
| "This form did not come from this site's own page", "session expired or cookies are blocked" or "The form is out of date" | CSRF or same-origin refusal: stale page, blocked cookies, or a proxy rewriting origin. | Reload the page and try again; allow cookies for the site. |
| "the default branch changed after this request was previewed; preview again" | Someone merged to the default branch after your preview. | Preview again, then submit. |
| Preview blocked: `missing_value` ("must set ...") | A rollout environment has no value for a per-environment variable. | Enter the value in the preview form for each environment. |
| Preview blocked: "already exists in every environment (a built-in object); enter its ID for ..." | A built-in object (default population, default policy) must be adopted, not created. | Enter that environment's existing object ID in the import ID field. If the form refuses IDs, see open items in [docs/HANDOFF.md](docs/HANDOFF.md). |
| "the worker cannot read it (HTTP 403: service not licensed or no read role)" | The type is unlicensed in the source, or the worker lacks a read role. | Add the read role, or ignore it for services you do not use; the export continues. |
| "pingone: the worker token request returned HTTP 401/400; check the worker client ID, secret, environment and region" | Wrong worker credentials, environment ID or `regionCode`. | Fix `secrets.env` and `pingone` in the config. |
| "GitLab token is required", or GitLab "...request failed; verify the server URL and network" | Empty token, wrong `baseUrl`, or blocked egress. | Fill `PINGONE_PROMOTE_GITLAB_TOKEN`; check the URL and firewall. |
| "GitLab default branch must be protected" or "GitLab project must be private, active, and have a default branch" | Project settings do not meet the server's checks. | Protect the default branch; use the right `projectId`. |
| Unfinished drafts disappeared | The server restarted; drafts are memory-only. | Start the request again. History is in GitLab. |

More failures: [docs/operations.md](docs/operations.md#troubleshooting).

## More

- Developers: [docs/development.md](docs/development.md).
- Operating details and every config field: [docs/operations.md](docs/operations.md).
- Rolling forward after a bad change: [docs/recovery.md](docs/recovery.md).
- What can be exported: [docs/coverage.md](docs/coverage.md). Design: [docs/adr/README.md](docs/adr/README.md).
- Updating to a newer build: [docs/updating.md](docs/updating.md).
- Current state and open items: [docs/HANDOFF.md](docs/HANDOFF.md).
