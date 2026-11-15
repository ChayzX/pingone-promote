# Status and known limitations

This page describes the hosted server on the `release/hosted-v1` line: what
is in place, how far it has been verified, and what is still open. Operators
start at the [README](../README.md); developers at
[development.md](development.md). The design is recorded in
[ADR 0018](adr/0018-hosted-source-only-server.md) (hosted server) and
[ADR 0019](adr/0019-core-pingone-exporter.md) (PingOne exporter).

## What is in place

- **Server.** `pingone-promote serve --config FILE [--check-config]` serves
  embedded pages without JavaScript under a strict Content-Security-Policy.
  It answers only for allowed host names (421 otherwise), checks a CSRF token
  and the request origin on every POST, and exposes `/healthz`. Drafts,
  previews and submissions live in memory and run as background jobs that can
  be cancelled.
- **Start-up checks.** The secrets file must be a regular file, not a
  symbolic link, readable by its owner only and owned by the service user.
  The configuration is validated as a whole, and both tool binaries must
  match their pinned digests.
- **GitLab.** A preview works on a private snapshot of one commit. A
  submission is committed on top of the previewed commit and is refused when
  the default branch has moved since. When the outcome of a commit is
  uncertain the branch is kept and the page says what to check. The Requests
  page shows each merge request and the apply job of every environment.
- **Export.** DaVinci is exported through the pinned pingcli-terraformer
  0.6.0. Other PingOne configuration is read by `internal/coreexport`, a
  client that only issues GET requests. `internal/catalog` classifies all 101
  provider resources: 54 covered, 32 covered with limits, 15 not possible.
  See [coverage.md](coverage.md).
- **Merge.** Changes are additive. Values that differ per environment go to
  that environment's `.tfvars`, secrets are declared as Vault references, and
  the result must pass `terraform fmt` before it can be submitted. Every
  status message is redacted before it is shown or logged.
- **Deployment files.** The systemd unit, Containerfile and Kubernetes
  manifests in `deploy/` are checked against the code by
  `deploy/deploy_test.go`.

## Verification

The automated suite runs against recorded fixtures and a stand-in GitLab.
It includes the race detector on Linux and a real `terraform validate` of
generated output against the pinned provider.

Behaviour against live systems is verified separately with the cases in
[acceptance/core-export.md](acceptance/core-export.md) and
[acceptance/hosted-server.md](acceptance/hosted-server.md). Each resource
type in [coverage.md](coverage.md) carries a `Live` column; a type marked
`no` has been tested with fixtures only. Run the acceptance cases against
your own tenant, GitLab and host before relying on a type in production, and
again after changing the worker's roles, the pipeline or the host platform.

## Known limitations

- **No sign-in.** A typed name is recorded as `NAME (unverified)`. OIDC is
  reserved in the configuration but not implemented. Keep the server on
  loopback or behind an authenticating proxy.
- **One instance.** State is in memory, so run a single replica. Unfinished
  drafts are lost on restart; submitted requests are in GitLab.
- **GitLab races.** GitLab has no conditional commit, so a small window
  remains between the default-branch check and the commit, and between the
  head check and the deletion of an abandoned branch. Branch protection and
  merge request review are the control.
- **Secret detection is heuristic.** `internal/secretscan` refuses values
  that look like credentials. Treat it as a second line of defence, not as
  proof that an export holds no secret; reviewers still read the diff.
- **Terraform digest.** `scripts/fetch-tools.sh` verifies the downloaded
  archive but does not print the digest of the extracted binary; operators
  compute it (see [operations.md](operations.md#configuration-fields)).
- **Egress host names.** The per-region host names listed in
  [operations.md](operations.md#network) should be confirmed against your
  tenant before they are written into firewall rules.
- **Upstream.** pingcli-terraformer exports DaVinci only, and Ping CLI 1.x
  has no export command, which is why the server carries its own exporter
  for the rest of PingOne.

## Security properties to preserve

- Exporters hold only the read-only worker of the source environment. The
  server has no write credential for PingOne and none for the rollout
  environments.
- Secrets come only from the owner-only secrets file. They never appear in
  the configuration file, pages, logs, Git or `.tfvars`.
- The server never runs `terraform plan` or `apply`. Changes roll forward
  through the pipeline and are never reverted
  ([recovery.md](recovery.md)).

The threat model is in [security/threat-model.md](security/threat-model.md).

## Checking a build

With Go 1.27.4 and Terraform 1.16.7 on `PATH`
(`scripts/fetch-tools.sh --with-go` installs both):

    gofmt -l .
    go build ./...
    go vet ./...
    go test ./...
    terraform fmt -check -recursive terraform/
    python3 -m unittest discover -s scripts -p test_release.py
