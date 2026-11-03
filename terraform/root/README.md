# Reference root stack

This is the root configuration the enterprise GitLab Terraform pipeline runs
once per environment:

```sh
terraform init
terraform plan  -var-file=environments/<env>.tfvars
terraform apply -var-file=environments/<env>.tfvars
```

This repository ships no deployment pipeline. Plans and applies run in the
enterprise pipeline.

## Files

| File | Purpose |
| --- | --- |
| `main.tf` | calls the `pingone-davinci` module; the only place its `source` appears |
| `providers.tf` | `required_version >= 1.15.0` and the `pingone` provider, configured from the environment |
| `variables.tf` | pass-through variables for the tfvars; the module owns the types |
| `backend.tf.example` | GitLab-managed state over the `http` backend |
| `imports.tf.example` | root-level import blocks addressed as `module.davinci.*` |
| `.terraform.lock.hcl` | provider checksums for linux, darwin and windows |

## Pipeline inputs

- The environment's tfvars file, `environments/<env>.tfvars`, which
  pingone-promote writes.
- `TF_VAR_pingone_secrets`: a JSON object mapping secret keys
  (`^[a-z0-9_]{1,128}$`) to values. `pingone-promote check --required-secrets`
  lists the keys a file needs.
- `PINGONE_CLIENT_ID`, `PINGONE_CLIENT_SECRET`, `PINGONE_ENVIRONMENT_ID` (the
  worker's authentication environment) and `PINGONE_REGION_CODE`.
- Backend settings (`TF_HTTP_*`) when `backend.tf.example` is used.

State contains secrets (the provider has no write-only attributes). Use an
encrypted, access-controlled backend.

## Switching the module source

During development `main.tf` uses the in-repo module:

```hcl
source = "../modules/pingone-davinci"
```

To consume the released module, change only that line (and add `version`
for a registry source).

Enterprise GitLab Terraform module registry:

```hcl
source  = "gitlab.example.com/<group>/pingone-davinci/pingone"
version = "~> 0.1"
```

Git tag in the enterprise GitLab repository:

```hcl
source = "git::https://gitlab.example.com/<group>/pingone-promote.git//terraform/modules/pingone-davinci?ref=pingone-davinci/v0.1.0"
```

tfvars files never contain module paths or sources, so no environment file
changes when the source moves. Run `terraform init -upgrade` after changing
it.

## Verification

```sh
terraform init -backend=false
terraform validate
terraform test     # mock provider; needs no credentials
```
