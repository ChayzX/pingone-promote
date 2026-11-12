# Operating the hosted server

`pingone-promote serve --config FILE` is one process that keeps no data of its
own (ADR 0018). Drafts live in memory and are lost on restart; everything
durable is in GitLab. It writes only private temporary files (export staging,
Git snapshots) under `$TMPDIR`.

**Phase 1 has no sign-in.** Anyone who can reach the port can use it, and a
typed name is recorded as `NAME (unverified)`. Keep it on loopback, or behind a
reverse proxy that authenticates people and terminates TLS, or on a private
network.

Run exactly one instance.

Files in this repository: `deploy/config.example.json`,
`deploy/secrets.example.env`, `deploy/systemd/pingone-promote.service`,
`deploy/container/Containerfile`, `deploy/kubernetes/*.yaml`.
`deploy/deploy_test.go` fails CI if these drift from the server's config
format or lose their hardening.

## Configuration fields

JSON has no comments, so the example is described here. Unknown fields are
rejected. Secrets never go in this file.

| Field | Meaning |
| --- | --- |
| `listen` | `host:port`. Default `127.0.0.1:8080` (this machine only). Any non-loopback address (including `0.0.0.0` and `:8080`) is refused unless `exposure` is set. |
| `exposure` | Empty (loopback only), `tls` (the server terminates TLS; requires `tlsCertFile`/`tlsKeyFile`) or `behind-proxy` (a TLS-terminating proxy in front; requires `allowedHosts`). Either value logs a startup warning: there is no sign-in, so anyone who can reach the port can file requests under any name. With `behind-proxy` the server expects browsers to use `https://` (form origins are checked against it). |
| `tlsCertFile`, `tlsKeyFile` | Both or neither. Leave empty behind a TLS-terminating proxy or on localhost. |
| `allowedHosts` | Exact `Host` header names (no port) the server answers, besides localhost and loopback. Required when reached under any other name (proxy, Service, Ingress). Forwarded-host headers are not trusted. |
| `signIn` | `none` (the only working value). |
| `secretsFile` | Path of the secrets file (below). |
| `gitlab.baseUrl`, `gitlab.projectId`, `gitlab.mrLabels` | GitLab origin, numeric project ID, labels for merge requests. |
| `pingone.regionCode` | One of `NA`, `EU`, `CA`, `AP`, `AU`, `SG`. |
| `pingone.workerEnvironmentId` | Environment holding the **read-only** source worker app. |
| `sources` | Environments a request may export from (`development` means `development.tfvars`). |
| `sourceEnvironmentIds` | Optional: `{"development": "<PingOne environment ID>"}`. Needed when a source's `.tfvars` does not set `pingone_environment_id` (for example when it comes from a GitLab CI variable). |
| `rollout` | Environments the pipeline applies afterwards, in order. Must not overlap `sources`. |
| `overlayPattern` | Where each environment's tfvars live, default `{env}.tfvars`. |
| `repository` | Optional overrides: `moduleName`, `moduleDir`, `moduleFile`, `variablesFile`, `secretsFile`, `importsFile`, `customerSecretsFile`; `ciVariables` (root variables the pipeline supplies, never asked for or written to tfvars); `vaultPathPrefix`, `vaultMount` (Vault location of new secrets). |
| `branchPrefix` | Request branch prefix, e.g. `CIAM-` (ends in `-` or `/`). |
| `branchPattern`, `branchExample` | Optional. People type the branch name (for example a Jira key) and it must match `branchPattern` whole, such as `CIAM-[0-9]+`; nothing is added to it, and an existing branch is refused. Without it, branches are `branchPrefix` + request ID. |
| `applyJobPattern` | Apply job name in the default-branch pipeline; must contain `{env}`. |
| `layout` | `default` or `terraformer`. |
| `tools.terraform`, `terraformVersion`, `terraformSha256` | Terraform path, version (must be `1.16.7`), and the SHA-256 of the **extracted binary**. |
| `tools.terraformer`, `terraformerVersion`, `terraformerSha256` | `pingcli-terraformer` path, version (`0.6.0`), SHA-256. |
| `tools.pingcli*` | Optional; not used by the server. |

`terraformSha256` is a placeholder in the examples
(`REPLACE_WITH_sha256sum_OF_tools/terraform`); the server refuses to start with
it. `fetch-tools.sh` verifies the *downloaded zip*, and prints the
terraformer digest but not the terraform binary digest, so compute it:

    sha256sum /opt/pingone-promote/tools/terraform        # Linux
    shasum -a 256 tools/terraform                         # macOS

The example holds the `linux_amd64` terraformer digest
(`be0bea0f...ae4a98`). Other platforms (the script prints the right one):
linux_arm64 `e934912a...a156d7`, darwin_arm64 `80a18625...63942b`,
darwin_amd64 `306c7064...0fab9f`. Check against `scripts/fetch-tools.sh`.

### Secrets file

`NAME=value` lines (see `deploy/secrets.example.env`):
`PINGONE_PROMOTE_WORKER_CLIENT_ID`, `PINGONE_PROMOTE_WORKER_CLIENT_SECRET`,
`PINGONE_PROMOTE_GITLAB_TOKEN`. The server refuses a file that is not a regular
file with mode exactly `0600` or `0400` (so `0440`/`0640` are rejected). The
worker must be read-only; the GitLab token needs only the scopes to write
branches, commits and merge requests on the one project.

### Network

Outbound HTTPS (443) only to the PingOne API and auth hosts for the region,
and to the GitLab host. Inbound only on the listen port. Hostnames, **to
verify** against PingOne documentation for your tenant:

| Region code | tld | Hosts |
| --- | --- | --- |
| NA | `com` | `api.pingone.com`, `auth.pingone.com` |
| EU | `eu` | `api.pingone.eu`, `auth.pingone.eu` |
| CA | `ca` | `api.pingone.ca`, `auth.pingone.ca` |
| AP | `asia` | `api.pingone.asia`, `auth.pingone.asia` |
| AU | `com.au` | `api.pingone.com.au`, `auth.pingone.com.au` |
| SG | `sg` | `api.pingone.sg`, `auth.pingone.sg` |

The server must be able to resolve and reach those plus the `fetch-tools.sh`
download hosts only while installing (`releases.hashicorp.com`, `github.com`).

## RHEL 9 install (systemd)

Run as root.

    # 1. Service account and directories
    useradd --system --home-dir /nonexistent --shell /sbin/nologin pingone-promote
    install -d -m 0755 -o root -g root /opt/pingone-promote /opt/pingone-promote/tools
    install -d -m 0750 -o root -g pingone-promote /etc/pingone-promote

    # 2. Pinned tools (checksum verified) from a checkout of this repository
    bash scripts/fetch-tools.sh --dest /opt/pingone-promote/tools
    sha256sum /opt/pingone-promote/tools/terraform

    # 3. The binary: build (Go 1.27.3, e.g. fetch-tools.sh --with-go) or copy a release build
    CGO_ENABLED=0 go build -trimpath -o /opt/pingone-promote/pingone-promote ./cmd/pingone-promote
    chmod 0755 /opt/pingone-promote/pingone-promote

    # 4. Config and secrets (edit both; put the terraform sha256 into the config)
    install -m 0640 -o root -g pingone-promote deploy/config.example.json /etc/pingone-promote/config.json
    install -m 0400 -o pingone-promote -g pingone-promote deploy/secrets.example.env /etc/pingone-promote/secrets.env
    $EDITOR /etc/pingone-promote/config.json /etc/pingone-promote/secrets.env

    # 5. Service
    install -m 0644 deploy/systemd/pingone-promote.service /etc/systemd/system/
    systemctl daemon-reload
    systemctl enable --now pingone-promote
    systemctl status pingone-promote
    journalctl -u pingone-promote -f

Secrets delivery. `secretsFile` is `/etc/pingone-promote/secrets.env`, mode
`0400`, owned by `pingone-promote`; the server refuses any other mode or
owner. `ProtectSystem=strict` keeps it read-only for the service. To edit it:
`sudo chmod 0600 /etc/pingone-promote/secrets.env`, edit,
`sudo chmod 0400 /etc/pingone-promote/secrets.env`, `sudo systemctl restart pingone-promote`.

On systemd 254 or newer you may use systemd credentials instead: add
`LoadCredential=secrets:/etc/pingone-promote/secrets.env` to the unit, keep that
file `0600 root:root`, and set `secretsFile` to
`/run/credentials/pingone-promote.service/secrets`. systemd then gives the service
a root-owned `0440` copy with an access-control entry for the service user, which
the server accepts only inside `/run/credentials/`. Verified on Ubuntu 24.04
(systemd 255). On RHEL 9 (systemd 252) in a container the service user could not
read the credential at all, so it is not the default there.

Tested 2026-11-11: the shipped unit's hardening as a transient unit on Ubuntu 24.04
(systemd 255), and the install steps above in a RHEL 9.9 (UBI 9, systemd 252)
container with systemd as init. The service starts, `/healthz` answers, a foreign
Host gets 421, a POST without a CSRF token gets 403, the process has no
capabilities, `NoNewPrivs` and a seccomp filter, no secret reaches the journal,
and `systemd-analyze security` rates it 1.6 to 1.7 (OK). SELinux on a real RHEL 9 host is
still untested; see `docs/acceptance/hosted-server.md`.

`MemoryDenyWriteExecute=yes` is safe for this binary: it is a static Go
program (`CGO_ENABLED=0`) and terraform and pingcli-terraformer are Go too. If
you add a tool that JITs, relax it.

Check the configuration, secrets file and tools before starting the service
(run it as the service user, who owns the secrets file):

    runuser -u pingone-promote -- /opt/pingone-promote/pingone-promote serve --config /etc/pingone-promote/config.json --check-config

### SELinux

Enforcing is the RHEL default; do not disable it.

- Binary and tools in `/opt` should be `bin_t` so the unit's process can execute them:

      semanage fcontext -a -t bin_t '/opt/pingone-promote(/.*)?'
      restorecon -Rv /opt/pingone-promote

- Config in `/etc` gets `etc_t` by default and is readable. Verify with
  `ls -Z`.
- Port 8080 is `http_cache_port_t`, which services may bind. Another port
  needs `semanage port -a -t http_port_t -p tcp 9443` (choose the type your
  policy permits) before a non-default `listen` works.
- Outbound HTTPS from a service run by systemd is allowed for the default
  `unconfined_service_t` domain. If you confine it with a custom domain, allow
  `name_connect` to `http_port_t`. A custom policy is optional.
- If denied: `ausearch -m avc -ts recent`; use `audit2allow` only to read the
  denial, do not paste its output into policy blindly.

### Firewall

Loopback needs nothing. Only if you expose the port beyond the host:

    firewall-cmd --permanent --add-port=8080/tcp && firewall-cmd --reload

Prefer a reverse proxy (nginx/httpd/HAProxy) on 443 that authenticates users and
terminates TLS, proxying to `127.0.0.1:8080`, with that public name in
`allowedHosts`. The proxy must pass the original `Host` header (the server
ignores `X-Forwarded-Host`).

## macOS local test

For trying it on your own laptop. Requires Go 1.27.3 (or `--with-go`).

    bash scripts/fetch-tools.sh --dest ./tools
    shasum -a 256 tools/terraform
    go build -o pingone-promote ./cmd/pingone-promote
    cp deploy/config.example.json config.json
    cp deploy/secrets.example.env secrets.env && chmod 600 secrets.env

Edit `config.json`: `listen` `127.0.0.1:8080`; `secretsFile` the absolute path
of `secrets.env`; `tools.terraform`/`tools.terraformer` the absolute paths
under `./tools`; `terraformSha256` and the darwin `terraformerSha256` (the
script prints it); your GitLab and PingOne values. Fill `secrets.env`. Then:

    ./pingone-promote serve --config config.json
    open http://127.0.0.1:8080/

## Container and Kubernetes

Pin both base images by digest before shared use (instructions at the top of
`deploy/container/Containerfile`).

    podman build --format docker -f deploy/container/Containerfile -t pingone-promote:dev .
    podman run --rm --entrypoint /opt/pingone-promote/pingone-promote pingone-promote:dev --version
    podman run --rm --entrypoint sha256sum pingone-promote:dev /opt/pingone-promote/tools/terraform

`/tmp` must allow executing files: the exporter runs its checksum-verified
copy of the tool from a private directory under `$TMPDIR`, so a `noexec`
tmpfs (the Docker and Podman `--tmpfs` default) fails with "permission
denied". Pass `--tmpfs /tmp:rw,exec,nosuid,nodev`; a Kubernetes `emptyDir`
allows it.

Run locally (config must say `"listen": "0.0.0.0:8080"` with
`"exposure": "behind-proxy"` and your host name in `allowedHosts` inside the
container, and `secretsFile` `/run/secrets/secrets.env`; the secrets file on the host must
be `chmod 600` and owned by uid 10001 or be mapped, e.g. with
`--userns=keep-id:uid=10001,gid=10001`):

    podman run --rm -p 127.0.0.1:8080:8080 --read-only --tmpfs /tmp:rw,exec,nosuid,nodev \
      --userns=keep-id:uid=10001,gid=10001 \
      -v "$PWD/config.json:/etc/pingone-promote/config.json:ro" \
      -v "$PWD/secrets.env:/run/secrets/secrets.env:ro" pingone-promote:dev

Kubernetes:

    kubectl create namespace pingone-promote
    kubectl -n pingone-promote create secret generic pingone-promote-secrets --from-file=secrets.env=./secrets.env
    # edit image, ConfigMap (gitlab, pingone, allowedHosts, terraformSha256)
    kubectl apply -f deploy/kubernetes/deployment.yaml
    kubectl apply -f deploy/kubernetes/networkpolicy.yaml   # after replacing the CIDRs

Points to know:

- **Listening, `exposure` and `allowedHosts`.** Inside a pod the server must listen on
  `0.0.0.0:8080` for the Service to reach it, so the ConfigMap sets
  `"exposure": "behind-proxy"` (the server refuses a non-loopback address
  without it and logs a no-sign-in warning with it). That is only safe because the
  `Host` header allow-list (`allowedHosts`, e.g. the Ingress hostname) is
  exact and the pod sits behind an authenticating proxy or private network.
  Probes send `Host: localhost`, which is always allowed. Without an
  authenticating proxy anyone in the cluster network could submit requests as
  an unverified name.
- **Secret file ownership conflict.** A Secret volume is root-owned. With
  `defaultMode: 0400` and no `fsGroup` the non-root server cannot read it.
  With `fsGroup: 10001` the kubelet makes the group `10001` and adds group read,
  so the file is effectively `0440 root:10001`, which the server's exact
  `0600`/`0400` check rejects. `runAsUser` does not change file ownership. The
  manifest therefore mounts the Secret only into an init container (uid
  10001, all capabilities dropped), which copies it with `umask 077` and
  `chmod 0400` into a memory-backed `emptyDir` that the server mounts
  read-only. The server sees an owner-only file owned by its own uid. Verify
  on your cluster: `kubectl exec ... -- ls -ln /run/pingone-promote`.
  The secret is then in tmpfs only, not on node disk beyond the Secret object
  itself; restrict RBAC on that Secret.
- **Never point `secretsFile` at the Secret volume.** Files in a Secret (or
  projected) volume are symbolic links into a `..data` directory, and the
  server refuses a symbolic link (it opens the file with `O_NOFOLLOW`). Keep
  `secretsFile` at the init container's copy, `/run/pingone-promote/secrets.env`,
  as the shipped manifest does. A `subPath` mount also yields a regular file,
  but it is never updated when the Secret changes and still has the 0440
  ownership problem above, so prefer the init-container copy.
- **Egress.** Core NetworkPolicy matches IPs, not names. Use CIDRs you have
  reviewed (they can change), an egress gateway/proxy with a fixed address, or
  a CNI with FQDN policy (Cilium `toFQDNs`, Calico). Allow DNS, the PingOne
  API/auth hosts and the GitLab host on 443.
- One replica, `Recreate` strategy. Drafts are in memory.
- `/healthz` returns 200 for an allowed Host and needs no session. Probes
  must send `Host: localhost` (or another allowed name); CI runs the image
  with `--check-config` and checks `/healthz` that way.

## Upgrade, tool pin changes

1. Read the release notes. Back up nothing (no data), but note that pending
   drafts are lost when the process restarts; announce it.
2. Build or copy the new binary to `/opt/pingone-promote/pingone-promote`
   (RHEL: `restorecon -v` the file) and `systemctl restart pingone-promote`;
   for Kubernetes set a new image digest and `kubectl apply`.
3. A tool version change is a commit to `scripts/fetch-tools.sh` (new
   digests) plus the matching `tools.*Version` and `tools.*Sha256` in the
   configuration. Re-run `fetch-tools.sh`, recompute the terraform binary
   digest, update the config, restart. A mismatched digest stops the server
   at start.
4. Rolling back is the reverse of the above; nothing to migrate.

## Logs

Logs go to stdout/stderr: `journalctl -u pingone-promote` or
`kubectl logs`. Secret values, tokens and raw upstream bodies are not logged by
design. Before sharing logs, still skim them; report any secret you find as a
bug. No log files are written to disk.

## Backup

None needed: the server has no database. Back up your configuration and the
secrets file (in your secret manager). All history is in GitLab.

## Troubleshooting

| Symptom | Likely cause and fix |
| --- | --- |
| Exits: secrets file must have mode 0600 or 0400 | Wrong mode (0440/0640 included) or not a regular file. `chmod 600`; under Kubernetes see the init-container note. |
| Exits: cannot open the secrets file | Wrong path, owner cannot read it, or `LoadCredential` source missing. `ls -ln`; for systemd check `secretsFile` equals `/run/credentials/pingone-promote.service/secrets`. |
| Exits: pinned SHA-256 digests | `terraformSha256` still the placeholder, or the tools changed. Recompute with `sha256sum`. |
| Exits: unknown field / malformed | Typo in the JSON config. The message deliberately omits values. |
| Exits: listen / address in use | Port taken; or SELinux denies a non-standard port (`semanage port`). |
| Browser: 403/bad host | The name in the URL is not in `allowedHosts`. Add it and restart; the proxy must keep the Host header. |
| Status 203/EXEC, `permission denied` | Binary not executable or wrong SELinux label (`restorecon -Rv /opt/pingone-promote`). |
| `status=31/SYS` (SIGSYS) | A tool made a blocked system call; check `journalctl`, loosen `SystemCallFilter` only if needed. |
| Cannot reach PingOne or GitLab | DNS or egress blocked (firewall, NetworkPolicy, proxy). Test `curl -sS https://api.pingone.<tld>` from the host. |
| Terraform step fails writing files | `$TMPDIR` not writable: with `--read-only` mount a tmpfs/emptyDir on `/tmp`. |
| Pod `CreateContainerConfigError` | Secret `pingone-promote-secrets` missing in the namespace. |
| Drafts vanished | The process restarted; drafts are memory-only by design. |
