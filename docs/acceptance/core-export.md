# Live acceptance: core PingOne export

Everything in this file can only be proven on a real PingOne tenant. The
fixture tests (`internal/coreexport`, `internal/multiexport`) prove the
logic against a fake API whose shapes were written from documentation, not
recorded from a tenant. Until a case below passes, treat the matching rows
of `docs/coverage.md` as unverified (`Live: no`).

## Setup (once)

1. Two disposable environments in one organization: `dev` (source) and
   `test` (rollout). Never use production for these cases.
2. A worker application in `dev` with **read-only** roles. Start with
   Environment Admin scoped to `dev` only to find gaps, then reduce to the
   smallest read roles that pass; record the final role set in
   `docs/operations.md`. The server must never get a role that can write.
3. A scratch GitLab project with the enterprise layout (`definitions/`,
   root `<env>.tfvars`, `customer_secrets.yml`), a protected default branch,
   and a token that can create branches and merge requests only.
4. A workstation (not the server) with Terraform 1.16.7, provider
   `pingidentity/pingone` 1.24.0, and a **separate** write-capable worker
   for `test`, used only to run plans in step "Plan check".

## Cases

| ID | Case | Steps | Pass when |
| --- | --- | --- | --- |
| C-01 | Read-only client | Run any export with the worker. In PingOne audit, filter by the worker client ID. | Only `GET` requests and one token request per hour appear; no `POST/PUT/PATCH/DELETE`. |
| C-02 | Region hosts | Repeat C-01 for each region you have (NA, EU, CA, AP, AU, SG). | The token and API hosts in `internal/pingone` are right; record the AU and SG hosts here. |
| C-03 | Every type is read | Create one object of each covered type in `dev` (use `docs/coverage.md`). Export the whole environment with a dry run. | Every object appears in the preview; no type is reported as "was not read" except unlicensed services; note each 403 with the role it needs. |
| C-04 | Plan check | Submit C-03 as a branch. On the workstation, check out the branch, `terraform init`, then `terraform plan -var-file=dev.tfvars` against **dev** with the import IDs from `dev.tfvars`. | The plan imports every object and shows **no changes** after import. Each attribute the plan wants to change is a mapping defect: record type, attribute, API field. |
| C-05 | One notification template | Change one template content (for example recovery code, English, email) in `dev`. Request only that item, without upstream. | The diff touches one block of `pingone_notification_template_content.tf` (and its import); nothing else. |
| C-06 | Upstream across DaVinci and core | Request one application whose flow policy assignment points to a DaVinci flow policy, with upstream on. | The DaVinci flow policy, its DaVinci application and flows, and the application's sign-on policy are in the change; `flow_policy_id` is a reference. |
| C-07 | DaVinci to core | Use a flow with a PingOne connector node that names a population by ID. Request the flow with upstream. | The population is added and the node references `pingone_population.<label>.id`, not the ID. |
| C-08 | Built-in adoption | Request the default population and the default sign-on policy. | The preview blocks with `missing_import` for `test`; after you enter `test`'s IDs, plan against `test` imports them and creates nothing. |
| C-09 | Singletons | Request branding settings and MFA settings. Plan against `test`. | They are imported by environment ID; no create. |
| C-10 | Secrets | Request an identity provider with a client secret, an LDAP gateway and SMTP settings. | The preview lists their secrets by name and Vault path; no value appears in Git, tfvars, the page or the server log. |
| C-11 | Environment values | Request an application with redirect URIs and a custom domain. | Redirect URIs and the domain are variables; the preview blocks until `test` has values; the source values are in `dev.tfvars` only. |
| C-12 | Whole environment with exclusions | Export the whole environment excluding one application and one flow. | Neither, nor the application's mappings and assignments, is in the change; references to them are ID variables or Git addresses. |
| C-13 | Unlicensed service | Use an environment without Verify or Credentials. | Those types are reported as not read; the export succeeds. |
| C-14 | Determinism | Export the same item twice without changes in `dev`. | The second request reports no changes. |
| C-15 | Rollout | Merge C-05's merge request and let the enterprise pipeline apply to `test`. | The Requests page shows `test` as passed, then the next environments in order. |

Record results with date, tenant region, provider version and the commit of
pingone-promote. When a case passes for a type, set `Verified: true` for it
in `internal/catalog/catalog.go` and regenerate `docs/coverage.md`.

## Known upstream issues to watch

- terraform-provider-pingone: provider-injected defaults can show a change
  when nothing changed; DaVinci flows whose current and published versions
  differ show a deploy action; secrets are not readable through the API.
  #1360 (missing flow settings fields) was fixed in provider 1.22.0.
- pingcli-plugin-terraformer: #166 and #16 (same-named DaVinci variables),
  #142 (`_2` references when an application and a flow share a name; fix in
  #146, release not confirmed), #163 (one 403 aborts a whole export), #156
  (unmarked sensitive outputs; not used here), #120 (no merge mode; this
  tool merges itself).
