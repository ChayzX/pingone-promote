# Reference root (v2)

This is the shape of a config repository that pingone-promote maintains
(ADR 0014, 0015, 0016). It is generated: it equals
`internal/promote/testdata/repo` after the Flow B request from the fixture
source environment, and `TestReferenceRootIsCurrent` keeps it in step.

The enterprise pipeline runs ONE commit through the environments in order,
with that environment's overlay:

```sh
terraform init
terraform plan  -var-file=<env>.tfvars
terraform apply -var-file=<env>.tfvars   # manual job; never run by pingone-promote
```

## Files

| File | Purpose |
| --- | --- |
| `definitions/` | shared definitions: a neutralized pingcli-terraformer child module, one `<type>.tf` per resource type. Only pingone-promote edits it, block by block. |
| `main.tf` | `module "davinci" { source = "./definitions" ... }`, passing every variable |
| `variables.tf` | environment-local root variables (no defaults: every overlay must set them) and `import_ids` |
| `secrets.tf` | one sensitive, non-nullable variable per secret; the pipeline sets `TF_VAR_<name>` from `customer_secrets.yml` |
| `imports.tf` | one conditional import per address, driven by `var.import_ids` (spike S1, ADR 0016) |
| `versions.tf` | Terraform `>= 1.16.0` and the `pingone` provider, configured from the environment |
| `<env>.tfvars` | one flat overlay per environment: `pingone_environment_id`, environment values, `import_ids` |
| `customer_secrets.yml` | the pipeline's Vault declarations (ADR 0009) |
| `backend.tf.example` | GitLab-managed state over the `http` backend |
| `tests/root.tftest.hcl` | mock-provider plan, run once per overlay |

No `*.auto.tfvars` file may exist: Terraform loads it automatically, and an
empty secret there would override the pipeline's `TF_VAR_` value.

## Checks

```sh
pingone-promote check --root . --customer-secrets customer_secrets.yml \
  --terraform-fmt --terraform-validate --envs dev,qa,stage,prod
for env in dev qa stage prod; do terraform test -var-file=$env.tfvars; done
```

State contains secret values (the provider has no write-only attributes).
Use an encrypted, access-controlled backend.
