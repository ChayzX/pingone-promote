# ADR 0010: Module versioning and the module source switch

- Status: Accepted
- Date: 2026-11-03
- Issues: #59, #60, #63

## Decision

- The module lives at `terraform/modules/pingone-davinci`. Releases are tagged
  `pingone-davinci/vX.Y.Z` and recorded in its `CHANGELOG.md`.
- tfvars carry `tfvars_schema_version = 1`, which the module validates. tfvars
  contain no paths and no `source`.
- `required_version >= 1.15.0` (the verified minimum for child-module
  imports, ADR 0008). `required_providers pingone ~> 1.23`.
- `terraform/root/` is the reference root stack. Moving to the enterprise
  GitLab registry or a Git ref changes only the `source` line (and `version`)
  in `main.tf`.
- The root stack commits a multi-platform `.terraform.lock.hcl`.

## Alternatives

- Vendoring the module into the pipeline repository: rejected. Module and
  generator versions would drift apart.

## Consequences

- A breaking tfvars change bumps both the module major version and
  `tfvars_schema_version`.
