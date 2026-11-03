# Adoption of existing resources. import_ids maps logical keys to existing
# PingOne IDs; each entry becomes an import block for the matching instance.
# Import IDs follow the provider's "<environment_id>/<id>" form. Once an
# address is in state Terraform ignores its import block, so stale IDs are
# harmless. Entries without a matching definition are reported by
# terraform_data.contract_imports.
#
# flow_deploy is deliberately not imported: whether an imported deploy
# resource triggers a redeploy is an open Phase 0 question (#61, #62).

locals {
  import_flows = { for k, id in var.import_ids.davinci_flows : k => id if contains(keys(var.davinci_flows), k) }
  import_flows_by_tier = {
    for t in [0, 1, 2, 3] : t => { for k, id in local.import_flows : k => id if var.davinci_flows[k].tier == t }
  }
}

import {
  for_each = { for k, id in var.import_ids.davinci_connections : k => id if contains(keys(var.davinci_connections), k) }
  to       = pingone_davinci_connector_instance.this[each.key]
  id       = "${var.pingone_environment_id}/${each.value}"
}

import {
  for_each = local.import_flows_by_tier[0]
  to       = pingone_davinci_flow.t0[each.key]
  id       = "${var.pingone_environment_id}/${each.value}"
}

import {
  for_each = local.import_flows_by_tier[1]
  to       = pingone_davinci_flow.t1[each.key]
  id       = "${var.pingone_environment_id}/${each.value}"
}

import {
  for_each = local.import_flows_by_tier[2]
  to       = pingone_davinci_flow.t2[each.key]
  id       = "${var.pingone_environment_id}/${each.value}"
}

import {
  for_each = local.import_flows_by_tier[3]
  to       = pingone_davinci_flow.t3[each.key]
  id       = "${var.pingone_environment_id}/${each.value}"
}

import {
  for_each = local.import_flows
  to       = pingone_davinci_flow_enable.this[each.key]
  id       = "${var.pingone_environment_id}/${each.value}"
}

import {
  for_each = { for k, id in var.import_ids.davinci_variables : k => id if try(var.davinci_variables[k].context, "flow") != "flow" }
  to       = pingone_davinci_variable.global[each.key]
  id       = "${var.pingone_environment_id}/${each.value}"
}

import {
  for_each = { for k, id in var.import_ids.davinci_variables : k => id if try(var.davinci_variables[k].context, "") == "flow" }
  to       = pingone_davinci_variable.flow_scoped[each.key]
  id       = "${var.pingone_environment_id}/${each.value}"
}

import {
  for_each = { for k, id in var.import_ids.davinci_applications : k => id if contains(keys(var.davinci_applications), k) }
  to       = pingone_davinci_application.this[each.key]
  id       = "${var.pingone_environment_id}/${each.value}"
}

import {
  for_each = { for k, id in var.import_ids.davinci_flow_policies : k => id if contains(keys(local.flow_policies), k) }
  to       = pingone_davinci_application_flow_policy.this[each.key]
  id       = "${var.pingone_environment_id}/${each.value}"
}
