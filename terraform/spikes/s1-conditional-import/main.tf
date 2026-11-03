# Spike S1 (ADR 0016): a conditional for_each import block whose `to` is a
# static, non-for_each address inside a child module. The import fires only
# when var.import_ids has an entry for that address.

terraform {
  required_version = ">= 1.16.0"
}

variable "pingone_environment_id" {
  type = string
}

variable "import_ids" {
  type     = map(string)
  default  = {}
  nullable = false
}

module "davinci" {
  source = "./child"
}

import {
  for_each = { for k, v in var.import_ids : k => v if k == "module.davinci.terraform_data.pingcli__Login-0020-Flow" }
  to       = module.davinci.terraform_data.pingcli__Login-0020-Flow
  id       = "${var.pingone_environment_id}/${each.value}"
}

output "login_id" {
  value = module.davinci.login_id
}
