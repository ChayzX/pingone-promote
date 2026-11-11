# Development

Start with [the current handoff](HANDOFF.md), [ADR 0018](adr/0018-hosted-source-only-server.md), and ADRs 0014–0016. The hosted server is the development focus. The DaVinci app and CI request command are compatibility features; keep their checks passing.

Build with Go 1.27.3. Engine tests require Terraform 1.16.7 on PATH; otherwise some tests skip their formatter gate and do not constitute full verification. `go build ./...`, `go vet ./...`, `go test ./...`, `gofmt`, and `terraform fmt -check -recursive terraform/` are required. The handoff lists the legacy app/release checks too.

Use `internal/exporter/exportertest` and `internal/gitlab/gitlabtest` instead of a real tenant for development. The hosted service reuses `request.Run`, `neutralize.Neutralize`, `promote.Merge`, overlay, pipeline-secret and formatter packages. Keep HTTP handlers free of resource-specific transformation logic. Do not run Terraform plans or applies in the server. Store no real credentials or captured tenant data in fixtures, source, test output or commits.

Run slow work with bounded background contexts; serialize draft mutations, invalidate stale previews and bind published content to the previewed export and Git SHA. Tests must cover concurrent/stale operations, uncertain GitLab outcomes, all POST security checks, and secrets in blocked previews as well as successful submissions. Add live-only proof requirements to `docs/acceptance/` and label assumptions explicitly.

Use small independently reviewed changes. A checkpoint on `feat/server` may be incomplete; PR #105 and this handoff must describe its current limits. Merge only when required CI is green for the exact reviewed commit.
