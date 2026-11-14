# Updating to a newer build

These steps replace a running `pingone-promote` container on a Mac with
Podman ([podman-mac.md](podman-mac.md)) with a newer build. Run them in your
clone of the repository. Unfinished drafts are lost when the container
stops; submitted requests are in GitLab.

## 1. Get the new code

```bash
git fetch origin release/hosted-v1
git checkout release/hosted-v1
git pull origin release/hosted-v1
git log --oneline -1
```

The last line is the build you are about to run.

## 2. Build the image

```bash
podman build --format docker -f deploy/container/Containerfile -t pingone-promote:test --build-arg VERSION=$(git rev-parse --short HEAD) .
```

The build ends with `Successfully tagged localhost/pingone-promote:test`.
These lines in its output are expected and harmless:

| Line | Why |
| --- | --- |
| `rehash: warning: skipping ca-certificates.crt ...` | Debian's bundle file is not a single certificate; your own certificates are still added. |
| `debconf: unable to initialize frontend ...` | the build has no terminal; packages still install. |
| `HEALTHCHECK is not supported for OCI image format` | the build ran without `--format docker`; the image works, but `podman ps` shows no health status. |

## 3. Bring the configuration up to date

Compare `~/pingone-promote-test/config.json` with
`deploy/container/config.podman.json` and the settings table in
[podman-mac.md](podman-mac.md), step 5. The server refuses to start with a
setting it does not know, so a misspelled or misplaced name stops it.

Settings added by recent builds:

| Setting | What to put |
| --- | --- |
| `branchPattern`, `branchExample` | Your branch naming rule, for example `"CIAM-[0-9]+(-[0-9]+)?"` and `"CIAM-1234"` (accepts CIAM-1234-1 too). The example must match the pattern. People type the branch name on the request page. |
| `applyJobPattern` | Your apply job's name with `{env}`, for example `"terraform:apply-{env}"`. |
| `repository` | Only if your repository differs from the default layout (module name or folder, Vault path). See podman-mac.md, step 5. |

When the Terraform or pingcli-terraformer version in the Containerfile
changed, also read the new checksums (podman-mac.md, step 4) and copy both
into `tools`. The build prints the pingcli-terraformer one.

## 4. Check the configuration with the new image

```bash
podman run --rm --read-only --tmpfs /tmp:rw,exec,nosuid,nodev --secret pingone-promote-secrets,target=secrets.env,uid=10001,gid=10001,mode=0400 -v ~/pingone-promote-test/config.json:/etc/pingone-promote/config.json:ro pingone-promote:test --check-config
```

It must print `configuration, secrets file and tools are usable`. If it
prints anything else, fix that first (see the table below); the running
container keeps working meanwhile.

## 5. Replace the container

```bash
podman rm -f pingone-promote
```

```bash
podman run -d --name pingone-promote -p 127.0.0.1:8080:8080 --read-only --tmpfs /tmp:rw,exec,nosuid,nodev --secret pingone-promote-secrets,target=secrets.env,uid=10001,gid=10001,mode=0400 -v ~/pingone-promote-test/config.json:/etc/pingone-promote/config.json:ro pingone-promote:test
```

## 6. Confirm it started

```bash
podman ps -a --filter name=pingone-promote
```

`Up ...` means it is running: open <http://localhost:8080/>. To confirm
the image is the build from step 1:

```bash
podman run --rm --entrypoint /opt/pingone-promote/pingone-promote pingone-promote:test --version
```

`Exited ...` means it stopped; the reason is the last line of:

```bash
podman logs pingone-promote
```

Fix the configuration, then `podman start pingone-promote` (no need to
recreate it: the configuration file is read at every start).

## If it does not start

| Message | Fix |
| --- | --- |
| `configuration file contains the unknown setting "X"` | `X` is misspelled, no longer used, or in the wrong section. Compare with `deploy/container/config.podman.json`. |
| `configuration file is not valid JSON near line N` | Usually a missing comma at the end of the line before N, or a comma after the last item of a list or section. |
| `configuration setting "X" ... has the wrong kind of value` | For example a list written as text: `"sources": ["development"]`, not `"sources": "development"`. |
| "does not know the PingOne environment ID of the source" (on preview) | The source's `.tfvars` must set `pingone_environment_id` on the default branch, be named after the source exactly as in `sources`, and sit where `overlayPattern` says (default: the repository root). |
| `environmentIds: "X" is neither a source nor a rollout environment` | Use the exact names from `sources` and `rollout` as the keys. |
| `environmentIds.X must be a PingOne environment ID` | Paste the environment's ID (a UUID) from the PingOne admin console. |
| `sourceEnvironmentIds: "X" is not one of the sources` | Use the exact names from `sources` as the keys. |
| `sourceEnvironmentIds.X must be a PingOne environment ID` | Paste the environment's ID (a UUID) from the PingOne admin console. |
| `branchExample does not match branchPattern` | Make the example a branch name your pattern accepts. |
| `branchPattern is not a valid regular expression` | Fix the pattern; write only the name rule, for example `CIAM-[0-9]+`, without `^` or `$`. |
| `branchPrefix must ...` | Delete the `branchPrefix` line when you use `branchPattern`. |
| `applyJobPattern must contain {env}` | For example `"terraform:apply-{env}"`. |
| a checksum does not match | Repeat podman-mac.md, step 4, and copy both values into `tools`. |
| `the secrets file must ...` | Keep the `--secret ...` option exactly as in step 5. |
| `exposure` / `listen is not a loopback address` | Keep `"listen": "0.0.0.0:8080"` with `"exposure": "container"`. |

If none of these match, send the last lines of `podman logs pingone-promote`.
They contain no secrets.

## When a page says "the operator can find details in the service log"

The log line `export failed` holds the cause. When it says
`restart the server with --debug`, the detail can quote tenant content and is
only logged on request: recreate the container with `--debug` after the image
name, reproduce the failure, read the log, then recreate it without `--debug`.

```bash
podman rm -f pingone-promote
```

```bash
podman run -d --name pingone-promote -p 127.0.0.1:8080:8080 --read-only --tmpfs /tmp:rw,exec,nosuid,nodev --secret pingone-promote-secrets,target=secrets.env,uid=10001,gid=10001,mode=0400 -v ~/pingone-promote-test/config.json:/etc/pingone-promote/config.json:ro pingone-promote:test --debug
```

```bash
podman logs pingone-promote 2>&1 | grep "export failed"
```

Secret values are removed from the log, but the detail can name your
flows, connections or applications: check it before sending it on.

## Other installations

- **RHEL 9 (systemd):** install the new binary as in
  [operations.md](operations.md), run
  `pingone-promote serve --config /etc/pingone-promote/config.json --check-config`
  as the service user, then `systemctl restart pingone-promote` and
  `journalctl -u pingone-promote -n 50` if it does not stay up.
- **Kubernetes:** push the new image, update the image tag in
  `deploy/kubernetes/deployment.yaml` (both `image:` lines) and the
  `pingone-promote-config` ConfigMap, apply, and read
  `kubectl logs deploy/pingone-promote` if the pod restarts.
