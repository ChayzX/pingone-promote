# ADR 0001: Use the pingidentity/pingone Terraform provider

- Status: Accepted
- Date: 2026-11-03
- Issues: #58, #60

## Decision

Terraform is the source of truth for PingOne configuration. All DaVinci
resources are managed with `pingidentity/pingone` `~> 1.23` (1.23.0 was
verified, 2026-10-07). It authenticates with worker client credentials.

The provider reads its credentials from the pipeline environment:
`PINGONE_CLIENT_ID`, `PINGONE_CLIENT_SECRET`, `PINGONE_ENVIRONMENT_ID` (the
worker's authentication environment) and `PINGONE_REGION_CODE` (`AP`, `AU`,
`CA`, `EU`, `NA` or `SG`). These names were verified against the provider
schema. Credentials never appear in tfvars.

Resource types in use: `pingone_davinci_connector_instance`,
`pingone_davinci_variable`, `pingone_davinci_flow`, `pingone_davinci_flow_deploy`,
`pingone_davinci_flow_enable`, `pingone_davinci_application` and
`pingone_davinci_application_flow_policy`.

## Alternatives

- `pingidentity/davinci` 0.6.0: rejected. It needs an interactive username
  and password, and it is superseded by the DaVinci resources in
  `pingidentity/pingone`.
- PingOne's native Configuration Management API (what the app uses today):
  rejected. It is not reviewable as code, and it cannot be applied by the
  enterprise pipeline.

## Consequences

- The provider has no write-only attributes. Connector secrets and secret
  variable values are stored in Terraform state, so the enterprise backend
  must be encrypted and access-controlled (`terraform/root/backend.tf.example`).
- The provider's own docs have errors. Its flow policy example uses
  `application_id` and an `oauth {}` block, but the 1.23.0 schema has
  `davinci_application_id`, and `oauth` is a nested attribute. Module code is
  checked against `terraform providers schema -json`, not against the docs.
