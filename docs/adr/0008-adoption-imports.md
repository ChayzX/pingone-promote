# ADR 0008: Adoption with for_each import blocks driven by import_ids

- Status: Accepted
- Date: 2026-11-03
- Issues: #60, #63

## Decision

The tfvars `import_ids` map holds existing PingOne IDs by logical key:

```hcl
import_ids = {
  davinci_connections   = { pingone_sso = "<id>" }
  davinci_flows         = { login = "<id>" }
  davinci_variables     = { brand_company = "<id>" }
  davinci_applications  = { web_login = "<id>" }
  davinci_flow_policies = { "web_login/default" = "<app_id>/<policy_id>" }
}
```

The module turns these into `for_each` import blocks, one per resource kind
and one per flow tier, using the provider's `<environment_id>/<id>` ID form.
Imported flows also import `pingone_davinci_flow_enable`. `flow_deploy` is not
imported, because whether an imported deploy resource triggers a redeploy is
an open Phase 0 question. `terraform/root/imports.tf.example` shows root-level
blocks addressed as `module.davinci.*`.

**Verified minimum Terraform version: 1.15.0.** Terraform 1.7.0 through
1.14.0 reject import blocks inside a child module at `init` ("Import blocks
are only allowed in the root module"). 1.15.0, 1.16.0, 1.17.0 and 1.17.1
accept them. The module and root stack declare `required_version >= 1.15.0`.

## Alternatives

- `terraform import` CLI calls in the pipeline: rejected. They are not
  reviewable in a merge request.
- Root-level import blocks only: kept as the documented fallback
  (`imports.tf.example`). It still needs a module copy without `imports.tf` on
  runtimes older than 1.15, because the module fails to initialize there.

## Consequences

- Stale import IDs are ignored once the address is in state, so `import_ids`
  can stay in the file after adoption.
- `terraform_data.contract_imports` rejects import IDs without a matching
  definition. Its `input` records which tier each imported flow targets, so
  plans show the routing.
- `mock_provider` cannot import flows, flow-scoped variables or flow policies:
  their refresh has unknown computed values. Tests therefore exercise real
  child-module imports for connections, global variables and applications,
  and assert the routing for the rest.
