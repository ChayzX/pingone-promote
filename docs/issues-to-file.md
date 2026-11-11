# GitHub issues to rewrite and file

The hosted build could not edit GitHub issues (no GitHub API access in the
session that built it). These are the texts, ready to paste. Delete this file
once they are on GitHub.

## Rewrite #82: Directory family (populations, groups, schema attributes, password policies)

Replace the body with:

> Delivered for fixtures in the hosted build (ADR 0018, ADR 0019). Exported by
> the native read-only exporter (`internal/coreexport`) into terraformer-shaped
> HCL, neutralized and merged additively like DaVinci.
>
> - Types: `pingone_population`, `pingone_population_default` (adopted),
>   `pingone_population_default_identity_provider`, `pingone_group`,
>   `pingone_group_nesting`, `pingone_group_role_assignment`,
>   `pingone_schema_attribute` (custom only; standard attributes adopted),
>   `pingone_password_policy` (the default adopted). Status per type:
>   `docs/coverage.md`.
> - References: a population's password policy, a group's population, a
>   sign-on action's registration population become references or
>   per-environment ID variables.
> - Tests: `internal/multiexport` TestSingleItemOfEveryFamilyAloneAndWithUpstream
>   (directory cases), `internal/coreexport` TestRenderedBlocksHaveRequiredAttributes.
>
> Remaining (needs a tenant): live cases C-03, C-04, C-08 in
> `docs/acceptance/core-export.md`; confirm the memberOfGroups shape for
> group nesting and the import format of `pingone_population_default`.

## Rewrite #83: Applications, resources, scopes, grants, sign-on policies and actions

> Delivered for fixtures (ADR 0019). Applications are read from the flat API
> object into `oidc_options` / `saml_options` / `wsfed_options` by protocol;
> redirect, logout, login and home URLs become per-environment variables.
> Application secrets are never exported (generated per environment, read by
> Terraform). Owned children travel with the application: attribute mappings
> (CORE mappings adopted), sign-on and flow policy assignments, resource
> grants, role assignments, push credentials. Sign-on policy actions are
> rendered per type (login, MFA, IdP, identifier first, agreement,
> progressive profiling, PingID); actions of the default policy are adopted.
> A flow policy assignment references the DaVinci flow policy (cross-family
> upstream, test TestUpstreamCrossesFromCoreToDaVinci).
>
> Remaining (needs a tenant): C-04 plan check for applications of each
> protocol; C-06; confirm `social_provider_ids` mapping (not exported today).

## Rewrite #84: Branding, notifications, forms, agreements, languages

> Delivered for fixtures (ADR 0019). One notification template content can
> be exported alone (TestOneNotificationTemplateExportsNothingElse and the
> server end-to-end test). Template content is read per delivery method into
> `email`, `sms`, `voice` or `push`; the email sender and reply-to addresses
> are per-environment values. Branding settings, notification settings, MFA
> settings and other singletons are imported by environment ID. Themes,
> forms, agreements with localizations and revisions, languages (predefined
> ones adopted, `language_update` for enabled/default) are covered.
> Images cannot be exported (bytes, not configuration).
>
> Remaining (needs a tenant): C-05, C-09, C-11; SMTP and phone provider
> secrets mapping on a real tenant.

## Rewrite #85: Coverage matrix for every pingone provider resource

> Done: `docs/coverage.md` is generated from `internal/catalog` (provider
> 1.24.0 schema embedded) and lists all 101 resources as covered, covered
> with documented limits, or not possible with the reason; a test fails when
> it is stale or when a provider resource is unclassified. Every core type is
> `Live: no` until `docs/acceptance/core-export.md` passes; set
> `Verified: true` per type as cases pass. Close when the live cases are done.

## New issues

1. **Live acceptance of the core exporter on a sandbox tenant.** Run
   `docs/acceptance/core-export.md` C-01 to C-15; record field-name and
   import-format corrections in `internal/catalog`; set `Verified` per type.
   Labels: needs-live.
2. **Live acceptance of the hosted server with a real GitLab.** Run
   `docs/acceptance/hosted-server.md`, including the Requests page with the
   enterprise pipeline's apply job names, child pipelines and MR approvals.
3. **Minimal read-only worker roles.** Find and document the smallest role
   set that lets the exporter read every covered type (C-01, C-03).
4. **PingOne sign-in for the server (phase 2).** Implement OIDC behind the
   existing `server.Identity` seam; recorded names become verified.
5. **Move core types to pingcli-terraformer when it gains definitions.**
   Track upstream PR #143 and later releases; switch a type's `Exporter` in
   the catalog after golden tests (ADR 0019 upgrade path).
6. **Per-client RADIUS secrets and webhook/phone-provider headers.** These
   are documented limits today (shared variable or set by hand).
7. **Digest-pin the container base images** (`golang`, `ubi-minimal`) in
   `deploy/container/Containerfile`.
8. **HTTP/2 under TLS** (`tls.NewListener` without `h2`), functional only.

Close #92 (source-only export tracker) as superseded by ADR 0018 and ADR 0019,
and close #94 (DaVinci-hosted front end) as superseded by the hosted server,
with a link to the README.
