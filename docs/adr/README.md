# Architecture decision records

These records cover the move from PingOne's native Configuration Management
API to Terraform (epic #58). Each record uses the format decision /
alternatives / consequences. Since 2026-11-03 the design is v2: a
source-only export through pingcli-terraformer, neutralized into shared
definitions plus one overlay per environment, triggered from a DaVinci
front end as a GitLab CI job.

| ADR | Title | Status |
| --- | --- | --- |
| [0001](0001-terraform-provider.md) | Use the pingidentity/pingone Terraform provider | Accepted |
| [0002](0002-languages.md) | Go for the app, generator and checker; Terraform for the modules | Accepted |
| [0003](0003-tfvars-shape.md) | tfvars shape, references and logical keys | Superseded by 0014/0015 |
| [0004](0004-flow-graphs.md) | Flow graphs as typed HCL plus canonical JSON heredocs | Superseded by 0014/0015 |
| [0005](0005-subflow-tiers.md) | Fixed subflow tiers t0..t3 | Superseded by 0014/0015 |
| [0006](0006-hcl-io.md) | HCL I/O and the terraform fmt gate | Accepted, revised for v2 |
| [0007](0007-inventory.md) | Inventory from the PingOne Platform DaVinci admin APIs | Superseded by 0014/0015 |
| [0008](0008-adoption-imports.md) | Adoption with for_each import blocks driven by import_ids | Superseded by 0014/0015 and 0016 |
| [0009](0009-secrets.md) | Secrets come from pipeline-injected variables, never from tfvars | Accepted, revised for v2 |
| [0010](0010-versioning-source-switch.md) | Module versioning and the module source switch | Superseded by 0014/0015 |
| [0011](0011-drift-and-diffs.md) | Drift and diffs | Superseded by 0014/0015 |
| [0012](0012-package-fate.md) | Fate of existing packages | Accepted, revised for v2 |
| [0013](0013-submission-and-drafts.md) | Single-commit GitLab submission and the server-side Draft API | Accepted, revised for v2 |
| [0014](0014-source-only-rollout.md) | Source-only rollout | Accepted |
| [0015](0015-pingcli-terraformer-exporter.md) | pingcli-terraformer as the exporter, plus a neutralizer (scoped export and additive merge) | Accepted |
| [0016](0016-import-mechanism.md) | Conditional imports on static addresses (spike S1) | Accepted |
| [0017](0017-davinci-frontend-ci-backend.md) | DaVinci front end, GitLab CI job backend | Accepted |

v2 code:

- `internal/exporter`: the pinned pingcli-terraformer runner and search
- `internal/neutralize`: export to shared definitions plus source values
- `internal/hclblocks`: byte-range block edits
- `internal/promote`: additive merge, overlays, `check --root`
- `internal/request`: the CI request job (`pingone-promote request`)
- `terraform/root`: the reference root; `terraform/spikes/s1-conditional-import`
- `examples/gitlab-ci/pingone-promote.yml`, `docs/gitlab-ci-integration.md`
