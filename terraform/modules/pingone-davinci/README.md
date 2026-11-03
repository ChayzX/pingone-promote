# pingone-davinci Terraform module

This module manages PingOne DaVinci connector instances, variables, flows
(with deploy and enable), applications and application flow policies with
`pingidentity/pingone` `~> 1.23`. It is fully driven by one environment's
tfvars file (schema v1), which `pingone-promote` writes. The module itself
is fixed: promotion changes only tfvars.

Decisions behind it are in [docs/adr](../../../docs/adr/README.md).

## Requirements

- Terraform `>= 1.15.0`. This is the first release that accepts import blocks
  inside a child module; 1.7.0 through 1.14.0 fail at `init`.
- Provider `pingidentity/pingone` `~> 1.23`, configured by the caller (see
  `terraform/root/providers.tf`).

## Inputs

| Variable | Type | Notes |
| --- | --- | --- |
| `tfvars_schema_version` | number | must be `1` |
| `pingone_environment_id` | string | lowercase UUID |
| `pingone_secrets` | map(string), sensitive | built by the root `secrets.tf` from one `TF_VAR_<key>` per secret; keys `^[A-Za-z][A-Za-z0-9_]{0,127}$`; null counts as missing |
| `parameters` | map(string) | environment-local, non-secret values |
| `davinci_connections` | map(object) | `name`, `connector`, optional `properties` JSON |
| `davinci_variables` | map(object) | keys are `<name>_<context>[_<flowKey>]` |
| `davinci_flows` | map(object) | `tier` 0..3, `graph.nodes` / `graph.edges`, optional settings, trigger and schemas |
| `davinci_applications` | map(object) | optional `oauth`, `api_key_enabled` and `flow_policies` |
| `import_ids` | object of maps | existing IDs for adoption |

`internal/tfvars/schema.go` is the authoritative definition. `variables.tf`
mirrors it.

## References

JSON heredocs (connector `properties`, node `properties`, variable
`json_object` and output schema `properties`) can contain placeholders:

| Placeholder | Resolves to | Allowed in |
| --- | --- | --- |
| `@@env:id@@` | `pingone_environment_id` | all JSON fields |
| `@@param:<key>@@` | `parameters[key]`, JSON-escaped | all JSON fields |
| `@@secret:<key>@@` | `pingone_secrets[key]`, JSON-escaped | connector and node properties |
| `@@connection:<key>@@` | connector instance ID | node properties |
| `@@variable:<key>@@` | global variable ID | node properties |
| `@@flow:<key>@@` | flow ID, lower tier only | node properties |
| `@@@@` | a literal `@@` | all JSON fields |

A node's `connection = "<key>"` resolves to `connection_id`, and a policy
distribution's `flow = "<key>"` resolves to the flow ID.

## Contract

Each category below is a separate `terraform_data` resource with
preconditions. Every managed resource depends on them, so a broken file fails
the plan before anything is changed. Messages list key names, never values.

- `contract_secrets`: lists missing `pingone_secrets` and `parameters` keys.
- `contract_references`: unpaired `@@`, malformed placeholders, disallowed
  kinds, unknown connections, flows and variables, broken edges, unknown policy
  flows.
- `contract_tiers`: a flow references a flow in the same or a higher tier.
- `contract_variables`: context and flow rules, secret and value rules,
  duplicate (name, context, flow) identity.
- `contract_imports`: import IDs without a definition, or a malformed policy
  ID. Its `input` shows which tier each imported flow targets.

## Adoption

`import_ids` entries become `for_each` import blocks (`imports.tf`) with IDs
of the form `<environment_id>/<id>`. Flow policies use
`"<app_key>/<policy_key>" = "<application_id>/<policy_id>"`. Once an address
is in state its import block is ignored.

## Raising a flow's tier

A flow's tier picks its resource (`pingone_davinci_flow.tN`), so changing the
tier changes the address. Move the state in the same change instead of
letting Terraform destroy and recreate the flow:

```hcl
moved {
  from = module.davinci.pingone_davinci_flow.t2["mfa_subflow"]
  to   = module.davinci.pingone_davinci_flow.t3["mfa_subflow"]
}
```

or run `terraform state mv` with the same addresses before applying.
pingone-promote blocks tier raises in the UI and points to this runbook.

## Outputs

`connection_ids`, `flow_ids`, `variable_ids`, `application_ids`,
`flow_policy_ids`, `flow_deploy_digests` and `required_secret_keys` (names
only).

## Tests

```sh
terraform init -backend=false
terraform test
```

`tests/e2e/` holds the end-to-end case. `internal/promote`'s `TestEndToEnd`
writes `tests/e2e/promoted.auto.tfvars` from the fixture inventory (translate,
then merge into a target environment), and the module resolves it:

```sh
terraform test -test-directory=tests/e2e
```

The tests use `mock_provider "pingone"`, so they need no credentials and make
no PingOne calls. `tests/golden_full.auto.tfvars` is written by
`go test ./internal/tfvars -update` and checked byte for byte against the Go
golden file, so Go and Terraform test the same document.

## Known limitations

- `flow_deploy` is not imported; whether that would redeploy is a Phase 0
  question (#61, #62).
- Under `mock_provider`, imported flows, flow-scoped variables and flow
  policies refresh with unknown computed values, so real imports are tested
  only for connections, global variables and applications.
- `pingone_davinci_application_key` and `_secret` (rotation resources) are not
  managed yet.
