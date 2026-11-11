# ADR 0019: A native read-only exporter for core PingOne

- Status: Accepted
- Date: 2026-11-11
- Extends: ADR 0015 (pingcli-terraformer stays the DaVinci exporter), ADR 0018 (hosted server)

## Context

The hosted server must bring any PingOne configuration from a source
environment into the Terraform config repository: a whole environment, or one
small item (one notification template, one application, one sign-on policy,
one population, one DaVinci flow), optionally with everything it references,
transitively, across DaVinci and core PingOne. pingcli-terraformer v0.6.0
exports DaVinci only. The server must never run `terraform plan` or `apply`
and holds only a read-only worker for the source environment.

## Options evaluated, in the order required

**(a) A newer pingcli-terraformer.** v0.6.0 (2026-10-16) is the newest
release. Its `definitions/` embeds `pingone/base/environment.yaml` (disabled)
and seven DaVinci definitions; the binary dispatches to seven hardcoded
handlers. No release adds core types. Upstream issues #27 to #118 request
them; PR #143 (open, unreleased) would add 25 SSO types. Rejected for now;
see "Upgrade path".

**(b) `pingcli platform export`.** pingcli v1.11.0 (2026-11-02) no longer has
the command: commit e806d35 removed the legacy CLI code (issue #175, open;
`pingcli export` is an unknown command since 1.0.0). The last version with
it, v0.8.4, writes **import blocks only** (`hcl_import_block.template`); its
own output tells the user to run `terraform plan -generate-config-out` to get
resource blocks. That needs a plan, with provider credentials, which this
server must never run. It also exports a whole service at a time. Rejected.

**(c) Contributing YAML definitions upstream.** Definitions are embedded at
build time (`//go:embed pingone/base/*.yaml pingone/davinci/*.yaml`) and need
matching Go handlers; there is no runtime definitions path. Upstream
contribution is the long-term path, but it cannot deliver coverage now and a
private fork would be a second binary to build, pin and audit. Deferred.

**(d) Chosen: a native, read-only exporter in this repository.**
`internal/pingone` is a client that can only obtain a worker token and send
GET requests to one environment; there is no code path that sends a body.
`internal/coreexport` reads every exportable type and renders the selected
objects as **terraformer-shaped HCL** (a child module with one `<type>.tf`
per type, `variables.tf`, root import blocks, an `.auto.tfvars` of values),
so the existing neutralizer and additive merge handle DaVinci and core the
same way. `internal/multiexport` combines it with pingcli-terraformer behind
the request engine's exporter interface.

## Decision

- `internal/catalog` is the single registry of resource types. It embeds a
  trimmed schema of the pinned provider (`pingidentity/pingone` 1.24.0,
  regenerated with `internal/catalog/schemagen`) and classifies all 101
  resources as covered, covered with documented limits, or not possible
  (users and user data; secrets generated per environment; per-environment
  actions such as domain verification). `docs/coverage.md` is generated from
  it and a test fails when it is out of date.
- **Attributes come from the provider schema.** For each configurable schema
  attribute the exporter reads the API field of the same name in camelCase,
  then the same name ignoring case and underscores, then `<x>.id` for an
  attribute `<x>_id`, then the catalog's renames, hoists (polymorphic
  objects such as an application's `oidc_options`, an identity provider's
  `google`, a sign-on action's `mfa`, a template content's `email`) and
  fallback sub-objects. Computed-only attributes are never written.
- **References.** Every string that equals the ID of a known object (core or
  DaVinci) becomes a reference when that object is in the export, and a
  per-environment ID variable (`<type>_<label>_id`, kind `excluded_ref`)
  when it is not. Git-managed objects keep their Git address (ADR 0015).
  DaVinci HCL is scanned for whole string literals equal to core IDs, which
  are rewritten the same way.
- **Upstream dependencies** are followed across both exporters until
  nothing new is added: core objects add what their API objects reference;
  terraformer's own `--include-upstream` closes DaVinci; DaVinci HCL adds
  the core objects it names. Owned children (an application's attribute
  mappings and policy assignments, a sign-on policy's actions, a flow's
  deploy and enable) always travel with their parent unless excluded.
- **Secrets** (schema `sensitive` or write-only) are never read or written;
  they become sensitive variables mapped to `customer_secrets.yml`. Secrets
  generated per environment (application, resource and DaVinci secrets,
  gateway credentials) are not exported at all: Terraform reads them in
  each environment.
- **Environment-specific values** listed per type in the catalog (redirect
  and logout URIs, domains, IdP endpoints, SMTP host, LDAP servers, webhook
  URLs, ...) become per-environment variables; the environment ID is
  replaced everywhere.
- **Built-in objects** (default population, default sign-on, MFA and
  password policies, predefined scopes and languages, system applications,
  default keys and predictors) are exported for adoption: the merge reports
  `missing_import` for every environment without an `import_ids` entry,
  and the operator enters the IDs on screen (`request.importIds`). Singleton
  settings (branding, notification and MFA settings, ...) are adopted with an
  import block whose ID is `var.pingone_environment_id`, the one
  unconditional import `check --root` accepts.
- A 403 on a type (an unlicensed service or a missing read role) skips it
  with a warning; any other error fails the export.

## Consequences

- `internal/promote` TestWholeEnvironmentCoreAndDaVinciValidates exports the
  whole fixture environment (every core family plus DaVinci), merges it and
  runs `terraform validate` against the pinned provider 1.24.0 in CI. It
  caught real rendering defects (polymorphic risk predictors, predefined
  languages, required empty lists) that are fixed; `pingone_risk_policy` is
  not exported until a live sample shows its API shape. Validation proves
  the HCL matches the provider schema, not that the API field mapping is
  right; that is the live plan check (C-04).

- Coverage is broad but **unverified against a live tenant**: API paths,
  field names and import ID formats were written from the provider and API
  documentation. Every type is `Live: no` in the coverage matrix until
  `docs/acceptance/core-export.md` passes. A mismatched field drops the
  attribute (it is not written) rather than writing a wrong one; the live
  case compares a plan of the exported definitions against the source.
- The worker needs read roles for each service it exports (Environment
  Admin read, or Identity Data Read Only plus Configuration Read Only; the
  exact minimal role set is a live acceptance item).
- Provider defaults that the API omits may still show as plan changes
  (terraform-provider-pingone issues on injected defaults); the preview
  warns that an enterprise plan must be reviewed.

## Upgrade path

When pingcli-terraformer releases core definitions, a type moves to it by
changing its `Exporter` in the catalog, after golden tests show the same
addresses and attributes. The labels already follow terraformer's
sanitization (`pingcli__<name>`, `-XXXX-` escapes), so addresses stay
stable across the switch.
