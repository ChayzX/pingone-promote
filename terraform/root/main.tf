# Reference root stack. One stack is applied per environment with that
# environment's tfvars file:
#
#   terraform plan -var-file=environments/<env>.tfvars
#
# Only the module source line changes when the module moves to the
# enterprise GitLab registry or repository (see README.md). tfvars files never
# contain paths or sources.

module "davinci" {
  source = "../modules/pingone-davinci"

  # Enterprise GitLab Terraform module registry:
  # source  = "gitlab.example.com/platform-group/pingone-davinci/pingone"
  # version = "~> 0.1"
  #
  # Or a Git tag in the enterprise GitLab repository:
  # source = "git::https://gitlab.example.com/platform-group/pingone-promote.git//terraform/modules/pingone-davinci?ref=pingone-davinci/v0.1.0"

  tfvars_schema_version  = var.tfvars_schema_version
  pingone_environment_id = var.pingone_environment_id
  pingone_secrets        = var.pingone_secrets
  parameters             = var.parameters
  davinci_connections    = var.davinci_connections
  davinci_variables      = var.davinci_variables
  davinci_flows          = var.davinci_flows
  davinci_applications   = var.davinci_applications
  import_ids             = var.import_ids
}

output "flow_ids" {
  description = "Flow IDs by logical key."
  value       = module.davinci.flow_ids
}

output "required_secret_keys" {
  description = "pingone_secrets key names the tfvars reference."
  value       = module.davinci.required_secret_keys
}
