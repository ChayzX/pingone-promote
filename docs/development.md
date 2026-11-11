# Development

Start with [the current handoff](HANDOFF.md), [ADR 0018](adr/0018-hosted-source-only-server.md), and ADRs 0014–0016. The hosted server is the development focus. The DaVinci app and CI request command are compatibility features; keep their checks passing.

Build with Go 1.27.3. Engine tests require Terraform 1.16.7 on PATH; otherwise some tests skip their formatter gate and do not constitute full verification. `go build ./...`, `go vet ./...`, `go test ./...`, `gofmt`, and `terraform fmt -check -recursive terraform/` are required. The handoff lists the legacy app/release checks too.

Use `internal/exporter/exportertest` and `internal/gitlab/gitlabtest` instead of a real tenant for development. The hosted service reuses `request.Run`, `neutralize.Neutralize`, `promote.Merge`, overlay, pipeline-secret and formatter packages. Keep HTTP handlers free of resource-specific transformation logic. Do not run Terraform plans or applies in the server. Store no real credentials or captured tenant data in fixtures, source, test output or commits.

Run slow work with bounded background contexts; serialize draft mutations, invalidate stale previews and bind published content to the previewed export and Git SHA. Tests must cover concurrent/stale operations, uncertain GitLab outcomes, all POST security checks, and secrets in blocked previews as well as successful submissions. Add live-only proof requirements to `docs/acceptance/` and label assumptions explicitly.

Use small independently reviewed changes. A checkpoint on `feat/server` may be incomplete; PR #105 and this handoff must describe its current limits. Keep this implementation on `feat/server` and out of `main`, as the user requested for the competition. Leave Claude’s `release/hosted-v1` untouched. Require green checks for the exact reviewed commit before claiming readiness.

The default hosted exporter is `exporter.Composite`. Core inventory uses explicit REST recipes and the read-only provider importer from ADR 0019; DaVinci keeps the pinned terraformer. The graph and adoption metadata must survive adapter boundaries. `Export.EnvLocalVariables` prevents generated ID inputs from being inlined into connector JSON, even when temporary source tfvars contain their values. `Export.AdoptOnly` blocks recreation of default objects. Both are copied through composition and passed into neutralization.

An optional real-provider loopback test runs when `PINGONE_PROMOTE_PROVIDER_ZIP` identifies the official pinned archive and Terraform 1.16.7 is on PATH. It uses synthetic UUID credentials and a local HTTPS proxy/fake API, not a tenant. Provision the archive in CI to exercise it; a skipped optional test is not proof of provider interoperability. The enterprise config repo should pin provider 1.24.0 to match the export schema; generated legacy broad constraints alone do not guarantee that version.
