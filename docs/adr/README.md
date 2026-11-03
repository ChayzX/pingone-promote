# Architecture decision records

These records cover the move from PingOne's native Configuration Management
API to Terraform-driven tfvars authoring (epic #58). Each record uses the
format decision / alternatives / consequences.

| ADR | Title |
| --- | --- |
| [0001](0001-terraform-provider.md) | Use the pingidentity/pingone Terraform provider |
| [0002](0002-languages.md) | Go for the app, generator and checker; Terraform for the modules |
| [0003](0003-tfvars-shape.md) | tfvars shape, references and logical keys |
| [0004](0004-flow-graphs.md) | Flow graphs as typed HCL plus canonical JSON heredocs |
| [0005](0005-subflow-tiers.md) | Fixed subflow tiers t0..t3 |
| [0006](0006-hcl-io.md) | HCL I/O with hashicorp/hcl/v2, app-canonical files, terraform fmt gate |
| [0007](0007-inventory.md) | Inventory from the PingOne Platform DaVinci admin APIs |
| [0008](0008-adoption-imports.md) | Adoption with for_each import blocks driven by import_ids |
| [0009](0009-secrets.md) | Secrets come from pipeline-injected variables, never from tfvars |
| [0010](0010-versioning-source-switch.md) | Module versioning and the module source switch |
| [0011](0011-drift-and-diffs.md) | Drift and diffs |
| [0012](0012-package-fate.md) | Fate of existing packages |
| [0013](0013-submission-and-drafts.md) | Single-commit GitLab submission and the server-side Draft API |

Code in this phase:

- `internal/tfvars`: schema v1, parser, writer, validator, diff and fmt gate
- `internal/keys`: logical key encoding
- `terraform/modules/pingone-davinci`: the module
- `terraform/root`: the reference root stack
- `pingone-promote check`: offline validation
