# Hosted deployment

The same `pingone-promote serve --config FILE` binary runs on a developer Mac, in a Linux container, and as a RHEL 9 systemd service. Configuration and pinned tool versions stay the same. Only paths and the listener address change for the container filesystem. The server holds unfinished drafts in memory, so deploy exactly one replica. A restart drops unfinished drafts; GitLab retains submitted requests.

## Install and configure

Build the binary with Go 1.27.3 (`go build -o pingone-promote ./cmd/pingone-promote`) or build the supplied [Containerfile](../Containerfile). Install tools for each target OS and architecture with `python3 scripts/install-tools.py --bin-dir /opt/pingone-promote/bin --cache-dir /opt/pingone-promote/cache`. The installer supports Darwin and Linux on amd64 and arm64. It verifies each SHA-256 from the checked-in lockfile before extracting an executable. The provider ZIP remains intact in the cache for the read-only importer. The generated JSON fragment reports tool paths and executable hashes; copy those fields into `tools` in the server config. Do not edit or infer the checksums by hand.

Keep the ordinary JSON config and sensitive secrets file separate. The config contains GitLab/PingOne metadata, paths, pins, source and rollout names, and exact allowed hostnames. It contains no tokens. A minimal shape is:

```json
{
  "listen": "127.0.0.1:8080",
  "allowedHosts": ["promote.example.com"],
  "signIn": "none",
  "secretsFile": "/etc/pingone-promote/secrets.env",
  "gitlab": {
    "baseUrl": "https://gitlab.example.com",
    "projectId": "group/configuration"
  },
  "pingone": {
    "regionCode": "NA",
    "workerEnvironmentId": "00000000-0000-0000-0000-000000000000"
  },
  "sources": ["development"],
  "rollout": ["testing", "production"],
  "tools": {
    "terraform": "/opt/pingone-promote/bin/terraform",
    "terraformVersion": "1.16.7",
    "terraformSha256": "<installer output>",
    "terraformer": "/opt/pingone-promote/bin/pingcli-terraformer",
    "terraformerVersion": "0.6.0",
    "terraformerSha256": "<installer output>",
    "pingcli": "/opt/pingone-promote/bin/pingcli",
    "pingcliVersion": "0.8.4",
    "pingcliSha256": "<installer output>",
    "providerZip": "/opt/pingone-promote/cache/terraform-provider-pingone_1.24.0_linux_amd64.zip",
    "providerSha256": "<installer output>",
    "providerVersion": "1.24.0"
  }
}
```

The hosted exporter uses the verified provider ZIP for private read-only imports. See [coverage](../docs/coverage.md) for resource-specific limits and fixture evidence. By default enabled services are detected from the source environment bill of materials; if its API is unavailable, set `pingone.services` explicitly to the licensed families, such as `["base", "sso", "mfa", "davinci"]`. Omitted families are deliberately excluded; do not omit a licensed family when requesting a whole environment.

The secrets file has exactly the three required values, one per line:

```text
PINGONE_PROMOTE_WORKER_CLIENT_ID=...
PINGONE_PROMOTE_WORKER_CLIENT_SECRET=...
PINGONE_PROMOTE_GITLAB_TOKEN=...
```

Use the minimum read-only PingOne worker permissions required by the exporter and a GitLab token restricted by project policy. The service enforces its request namespace and path allow-list in code. Protect the default branch in GitLab as a separate control. No rollout credentials or PingOne write credential belong on this host.

## RHEL 9 with systemd

Create a dedicated service account and install the binary and executable tools as root-owned, non-writable files:

```sh
sudo useradd --system --home-dir /var/lib/pingone-promote --shell /sbin/nologin pingone-promote
sudo install -d -o root -g pingone-promote -m 0750 /etc/pingone-promote
sudo install -d -o root -g root -m 0755 /opt/pingone-promote/bin /opt/pingone-promote/cache
sudo install -o root -g root -m 0755 ./pingone-promote /usr/local/bin/pingone-promote
sudo install -o root -g root -m 0555 ./local-tools/terraform /opt/pingone-promote/bin/terraform
sudo install -o root -g root -m 0555 ./local-tools/pingcli-terraformer /opt/pingone-promote/bin/pingcli-terraformer
sudo install -o root -g root -m 0555 ./local-tools/pingcli /opt/pingone-promote/bin/pingcli
sudo install -o root -g root -m 0444 ./local-cache/terraform-provider-pingone_1.24.0_linux_amd64.zip /opt/pingone-promote/cache/terraform-provider-pingone_1.24.0_linux_amd64.zip
sudo install -o root -g pingone-promote -m 0640 ./config.json /etc/pingone-promote/config.json
sudo install -o pingone-promote -g pingone-promote -m 0600 ./secrets.env /etc/pingone-promote/secrets.env
sudo install -o root -g root -m 0644 deploy/pingone-promote.service /etc/systemd/system/pingone-promote.service
sudo systemctl daemon-reload
sudo systemctl enable --now pingone-promote
sudo systemctl status pingone-promote
```

The supplied unit runs as the dedicated account with a private `/tmp`, read-only system files, no capabilities, no privilege escalation, and bounded file descriptors/tasks. `PrivateTmp` provides the writable scratch space used for verified tool copies and request snapshots. It does not make persistent state; drafts remain in memory. Do not add a writable shared group to the secrets file: the server accepts only exact mode `0400` or `0600`.

For SELinux enforcing mode, keep files under `/etc`, `/usr/local/bin`, and `/opt` with their standard labels and check `restorecon -Rv /etc/pingone-promote /opt/pingone-promote /usr/local/bin/pingone-promote /etc/systemd/system/pingone-promote.service` after installation. Do not disable SELinux to work around a denial. Inspect `ausearch -m AVC -ts recent` and `journalctl -u pingone-promote` and adjust the narrow file label or policy for the actual denial. The unit starts on loopback; do not open a host firewall port for it. If a same-host reverse proxy is used, allow only that proxy to reach the service and terminate TLS there. A remote proxy needs an explicit firewall rule limited to its source network, TLS, and an access boundary.

For a trusted TLS-terminating proxy, bind to the host interface that only the proxy can reach, set the exact external hostname in `allowedHosts`, and set `publicOrigin` to the HTTPS origin such as `https://promote.example.com`. The proxy must not rely on forwarded host or scheme headers for trust; the service does not trust them. Serve it directly with the server's TLS certificate/key if no proxy is used. Do not expose plain HTTP to an untrusted network.

Useful operations:

```sh
sudo journalctl -u pingone-promote -f
sudo systemctl restart pingone-promote
sudo systemctl stop pingone-promote
```

## Container or Kubernetes pod

Build the image from the repository root. The image contains the one server binary and CA certificates; keep credentials and downloaded tools outside the image. Supply the pinned executable tools and provider ZIP through a read-only volume, with config paths matching the mount. The app needs only a private writable `/tmp`; run with a read-only root filesystem and a non-root UID. Bound CPU and memory. The included [hosted-pod.yaml](hosted-pod.yaml) is a single-pod deployment template and assumes a config map named `pingone-promote-config`, Secret named `pingone-promote-secrets` containing key `secrets.env`, and a read-only tools PVC mounted at `/opt/pingone-promote`.

Stage the tool volume before mounting it read-only. Run the installer on a machine with the pod's Linux architecture, copy its `bin/` and `cache/` onto the PVC, and set ownership to the pod UID. The installer's private default modes are intended for its own user; a root-owned installation is not readable by UID 10001. For example, with the PVC mounted at `/mnt/pingone-promote-tools` in a staging pod:

```sh
python3 scripts/install-tools.py --bin-dir /mnt/pingone-promote-tools/bin --cache-dir /mnt/pingone-promote-tools/cache --output /tmp/tools.json
chown -R 10001:10001 /mnt/pingone-promote-tools/bin /mnt/pingone-promote-tools/cache
chmod 0700 /mnt/pingone-promote-tools/bin /mnt/pingone-promote-tools/cache
```

Use paths beginning `/opt/pingone-promote/bin/` and `/opt/pingone-promote/cache/` in the server config, retain the generated hashes, then unmount the staging pod before starting the server. Only public tool artifacts belong on this PVC. The service account must not be able to alter the read-only volume.

The Pod init container copies the projected Kubernetes Secret into memory-backed `emptyDir`, then sets ownership to UID 10001 and mode `0600`. The service mounts that copy read-only. Do not use `fsGroup` or group-readable `0440`: the server deliberately rejects group access. Keep the source Secret's RBAC narrow. The main process has no capabilities, runs as UID 10001, has a read-only root filesystem, and receives bounded resources. `automountServiceAccountToken` is disabled. Use `kubectl port-forward pod/pingone-promote 8080:8080` for local access, or put it behind a controlled TLS ingress and service policy. If reachable in-cluster, configure an exact allowed host and HTTPS public origin; never trust forwarded headers as a substitute for a trusted proxy.

The manifest listens through pod networking. The default service config listens on loopback, so set `listen` to `0.0.0.0:8080` only when Kubernetes network policy or a trusted ingress controls who can connect. Add `publicOrigin` for the exact external HTTPS origin. Kubernetes Secrets are readable by principals allowed to get the Secret; enforce namespace RBAC and encryption at rest. The template is a starting point and must be rendered with the chosen cluster's image registry, storage class, network policy, and config.

## Mac and local service

Use the same binary, config format and locked tool installer. Keep `listen` at `127.0.0.1:8080`, put the secrets file in the service user's home or a protected directory with mode `0600`, and open `http://127.0.0.1:8080` locally. macOS does not use the Linux systemd or SELinux instructions above.

## Troubleshooting

- `secrets file must have mode 0600 or 0400`: set owner to the service account and chmod to one of those exact modes. Kubernetes must copy the Secret in an init container and chown it; `fsGroup` alone does not satisfy this rule.
- `tool failed its pinned checksum or version check`: reinstall from `tools.lock.json`, verify that config uses the generated executable SHA and pinned version, and avoid replacing a tool after startup.
- `cannot listen`: check for a second replica or another process on the port. This service keeps draft ownership in process memory and is intended to run as one replica.
- `Host not allowed`: add the exact hostname used by the browser to `allowedHosts`. Port and scheme are not part of the configured hostname; forwarded headers do not change the Host validation.
- unavailable through ingress: confirm the app is bound to the pod interface, ingress terminates TLS, the host is allowed, and the network policy permits only the intended proxy path.
- service unavailable after restart: unfinished drafts are intentionally in memory only. Start a new preview; submitted requests remain in GitLab.
- SELinux denial: inspect recent AVC records and correct the path label or narrow policy. Do not turn off enforcing mode.
