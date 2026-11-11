# PingOne Promote

PingOne Promote is a single Go service for reviewing PingOne configuration changes and publishing Terraform configuration to a GitLab request branch. The enterprise pipeline remains responsible for Terraform plan and apply. The hosted service never runs either command.

## Quick setup

1. Build the single binary with Go 1.27.3: `go build -o pingone-promote ./cmd/pingone-promote`.
2. Install the pinned local tools for this machine with `python3 scripts/install-tools.py --bin-dir ./local-tools --cache-dir ./local-cache > tools-config.json`. The installer supports macOS and Linux on amd64 and arm64. Keep the generated config private to the host; it contains paths and checksums, not credentials.
3. Create a JSON server config from the example in [deploy/README.md](deploy/README.md). Point it at the pinned tools and set `secretsFile` to a separate file.
4. Put the PingOne worker client ID, worker client secret and GitLab token in the secrets file. Make the service owner its only reader and set mode `0600` or `0400`.
5. Start with `./pingone-promote serve --config /path/to/config.json`. It listens on `127.0.0.1:8080` by default. Open that address on the host and complete a preview before submitting.
6. For a persistent deployment, follow the [deployment guide](deploy/README.md) for systemd on RHEL 9 or a single-replica Kubernetes pod.

## Trust and operation

Phase 1 has no sign-in. A name typed into the UI is always shown as unverified and grants no authorization. Keep the service on loopback unless it is behind a deliberate TLS and access boundary. The Host header is checked against exact configured hostnames; forwarded headers are not trusted. POST actions require CSRF protection, and the pages use a strict Content Security Policy.

The server stores unfinished drafts in bounded memory. Restarting it loses those drafts. Published requests and durable history live in GitLab. Run one replica. Use only a source environment; the configured rollout supplies all later environments. Missing values for any rollout environment block submission.

Secrets are never stored in configuration, pages, logs, or Git. The secrets file must be a regular file owned by the service account with mode `0400` or `0600`; group-readable modes such as `0440` are rejected. The worker is used read-only. The service has no PingOne write credential and never runs Terraform plan or apply.

The exporter coverage and live tenant behavior are still being established. See the [acceptance cases](docs/acceptance/hosted-live-cases.md) for required live proof and current limits. A fixture pass does not establish tenant or production pipeline compatibility.

## Troubleshooting

If startup reports a secrets-file mode error, set ownership to the service user and run `chmod 600 /path/to/secrets.env`. If startup rejects a tool, rerun the pinned installer and keep its executable path and checksum together. If the UI is unreachable, confirm the process is listening on `127.0.0.1:8080`; a remote browser cannot reach that loopback address directly. Put a TLS reverse proxy on the host and set the exact allowed hostname before making it reachable through a proxy. If a preview is blocked, resolve every reported missing rollout value in the source or target configuration before submitting. If GitLab's default branch moved after preview, create a fresh preview. Unfinished drafts disappear on restart.

See [development.md](docs/development.md) for build and test commands and [deploy/README.md](deploy/README.md) for service operations.
