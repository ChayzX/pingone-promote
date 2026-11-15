# PingOne Promote

PingOne Promote copies configuration from one PingOne environment into the Terraform repository that your pipeline already deploys from. It is a small web server: you choose what to copy, review exactly what will change, and it opens a GitLab merge request. Promotion to each later environment stays where it is today, in the pipeline.

## What it does

- **Export.** Reads a source environment with a read-only worker and writes Terraform for what you chose: one application, one flow, one notification template, a whole section, or the whole environment, with or without what those items depend on.
- **Preview.** Shows every item that is added, updated or left alone, the diff against Git, and the values each rollout environment still needs (redirect URIs, domains, IDs of built-in objects). A request cannot be submitted while a value is missing.
- **Submit.** Commits the change to a branch and opens a merge request. Per-environment values go in that environment's `.tfvars`; secrets are declared as Vault references and never written to Git.
- **Track.** Lists each request with its merge request and the apply job of every environment.

PingOne Promote never changes PingOne and never runs `terraform plan` or `apply`.

This release has no sign-in: anyone who can reach the page can use it, and a typed name is recorded as `NAME (unverified)`. Keep it on your own machine or behind an authenticating proxy (see [docs/operations.md](docs/operations.md)).

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
2. `install -d -m 0755 /opt/pingone-promote /opt/pingone-promote/tools && install -d -m 0750 -g pingone-promote /etc/pingone-promote`
3. `bash scripts/fetch-tools.sh --dest /opt/pingone-promote/tools`, then `sha256sum /opt/pingone-promote/tools/terraform`
4. `CGO_ENABLED=0 go build -trimpath -o /opt/pingone-promote/pingone-promote ./cmd/pingone-promote`
5. Install `deploy/config.example.json` as `/etc/pingone-promote/config.json` (mode 0640, group `pingone-promote`) and `deploy/secrets.example.env` as `/etc/pingone-promote/secrets.env` (mode 0400, owned by `pingone-promote`); edit both.
6. Label the files: `semanage fcontext -a -t bin_t '/opt/pingone-promote(/.*)?' && restorecon -Rv /opt/pingone-promote`
7. Check everything as the service user: `runuser -u pingone-promote -- /opt/pingone-promote/pingone-promote serve --config /etc/pingone-promote/config.json --check-config`
8. `install -m 0644 deploy/systemd/pingone-promote.service /etc/systemd/system/`
9. `systemctl daemon-reload && systemctl enable --now pingone-promote`
10. `journalctl -u pingone-promote -f` and check for a clean start.
11. Keep `listen` on `127.0.0.1:8080` and publish through an authenticating reverse proxy on 443 with its name in `allowedHosts`. Only if the server itself must listen on another address, set `"exposure": "tls"` (with a certificate) or `"behind-proxy"`; see [operations](docs/operations.md).

## Daily use

1. **New request.** Open the page, type your name, choose the source environment and what to promote (one item, a group, or the whole environment, with or without what it depends on).
2. **Preview.** Wait for the preview. Read the summary, the item list, the diff and anything under "Things to check".
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
| "The default branch changed after the preview. Preview again." | Someone merged to the default branch after your preview. | Preview again, then submit. |
| Preview blocked: `missing_value` ("must set ...") | A rollout environment has no value for a per-environment variable. | Enter the value in the preview form for each environment. |
| Preview blocked: "already exists in every environment (a built-in object); enter its ID for ..." | A built-in object (default population, default policy) must be adopted, not created. | Enter that environment's existing object ID in the import ID field. Find the ID in the PingOne console of that environment. |
| "the worker cannot read it (HTTP 403: service not licensed or no read role)" | The type is unlicensed in the source, or the worker lacks a read role. | Add the read role, or ignore it for services you do not use; the export continues. |
| "pingone: the worker token request returned HTTP 401/400; check the worker client ID, secret, environment and region" | Wrong worker credentials, environment ID or `regionCode`. | Fix `secrets.env` and `pingone` in the config. |
| "GitLab token is required", or the GitLab host "could not be found (DNS)", "refused the connection" or "did not answer in time" | Empty token, wrong `baseUrl`, or blocked egress. | Fill `PINGONE_PROMOTE_GITLAB_TOKEN`; check the URL, VPN and firewall. |
| "GitLab default branch must be protected" or "GitLab project must be private, active, and have a default branch" | Project settings do not meet the server's checks. | Protect the default branch; use the right `projectId`. |
| Unfinished drafts disappeared | The server restarted; drafts are memory-only. | Start the request again. History is in GitLab. |

More failures: [docs/operations.md](docs/operations.md#troubleshooting).

## More

- Developers: [docs/development.md](docs/development.md).
- Operating details and every config field: [docs/operations.md](docs/operations.md).
- Rolling forward after a bad change: [docs/recovery.md](docs/recovery.md).
- What can be exported: [docs/coverage.md](docs/coverage.md). Design: [docs/adr/README.md](docs/adr/README.md).
- Updating to a newer build: [docs/updating.md](docs/updating.md).
- Status and known limitations: [docs/status.md](docs/status.md).
