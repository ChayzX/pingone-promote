# PingOne Promote

PingOne Promote copies configuration from one PingOne environment into the
Terraform repository your pipeline deploys from. You choose what to copy,
review what will change, and it opens a GitLab merge request. It never changes
PingOne and never runs `terraform plan` or `apply`.

This repository holds only what is needed to build and run the server.

## Layout

| Path | Contents |
| --- | --- |
| `cmd/`, `internal/`, `go.mod`, `go.sum` | The server source |
| `scripts/fetch-tools.sh` | Downloads and verifies Terraform 1.16.7 and pingcli-terraformer 0.6.0 |
| `deploy/container/` | `Containerfile`, an example configuration, and `extra-ca/` for company root certificates |
| `deploy/systemd/`, `deploy/*.example.*` | systemd unit and example files for a host install |
| `deploy/kubernetes/` | ConfigMap, Deployment, Service and NetworkPolicy |
| `docs/operations.md` | Every configuration setting, network needs and Kubernetes notes |
| `docs/recovery.md` | What to do after a bad change (fix forward, never revert) |

## Build

    TAG=$(git rev-parse --short HEAD)
    podman build --platform linux/amd64 --format docker \
      -f deploy/container/Containerfile --build-arg VERSION=$TAG \
      -t <registry-host>/<group>/pingone-promote:$TAG .

Read the two tool checksums the configuration needs from the image:

    podman run --rm --entrypoint sha256sum <image> \
      /opt/pingone-promote/tools/terraform \
      /opt/pingone-promote/tools/pingcli-terraformer

## Deploy

1. Edit the ConfigMap in `deploy/kubernetes/deployment.yaml` (GitLab, PingOne,
   environments, `allowedHosts`, both checksums) and both `image:` lines.
2. Create the secret from a file with the three names
   `PINGONE_PROMOTE_WORKER_CLIENT_ID`, `PINGONE_PROMOTE_WORKER_CLIENT_SECRET`
   and `PINGONE_PROMOTE_GITLAB_TOKEN`:

       kubectl create namespace pingone-promote
       kubectl -n pingone-promote create secret generic pingone-promote-secrets \
         --from-file=secrets.env=./secrets.env

3. `kubectl apply -f deploy/kubernetes/deployment.yaml`, then edit and apply
   `deploy/kubernetes/networkpolicy.yaml`.
4. Put an authenticating proxy in front. The server has no sign-in of its own,
   and it must run as a single replica.

Details are in [docs/operations.md](docs/operations.md).
