import {
  for_each = { for k, v in var.import_ids : k => v if k == "module.davinci.pingone_davinci_application.pingcli__Web" }
  to       = module.davinci.pingone_davinci_application.pingcli__Web
  id       = "${var.pingone_environment_id}/${each.value}"
}

import {
  for_each = { for k, v in var.import_ids : k => v if k == "module.davinci.pingone_davinci_application_flow_policy.pingcli__Web-0020-Policy" }
  to       = module.davinci.pingone_davinci_application_flow_policy.pingcli__Web-0020-Policy
  id       = "${var.pingone_environment_id}/${each.value}"
}

import {
  for_each = { for k, v in var.import_ids : k => v if k == "module.davinci.pingone_davinci_connector_instance.pingcli__Http" }
  to       = module.davinci.pingone_davinci_connector_instance.pingcli__Http
  id       = "${var.pingone_environment_id}/${each.value}"
}

import {
  for_each = { for k, v in var.import_ids : k => v if k == "module.davinci.pingone_davinci_connector_instance.pingcli__PingOne" }
  to       = module.davinci.pingone_davinci_connector_instance.pingcli__PingOne
  id       = "${var.pingone_environment_id}/${each.value}"
}

import {
  for_each = { for k, v in var.import_ids : k => v if k == "module.davinci.pingone_davinci_flow.pingcli__Flow-0020-A" }
  to       = module.davinci.pingone_davinci_flow.pingcli__Flow-0020-A
  id       = "${var.pingone_environment_id}/${each.value}"
}

import {
  for_each = { for k, v in var.import_ids : k => v if k == "module.davinci.pingone_davinci_flow.pingcli__Flow-0020-B" }
  to       = module.davinci.pingone_davinci_flow.pingcli__Flow-0020-B
  id       = "${var.pingone_environment_id}/${each.value}"
}

import {
  for_each = { for k, v in var.import_ids : k => v if k == "module.davinci.pingone_davinci_flow.pingcli__Flow-0020-C" }
  to       = module.davinci.pingone_davinci_flow.pingcli__Flow-0020-C
  id       = "${var.pingone_environment_id}/${each.value}"
}

import {
  for_each = { for k, v in var.import_ids : k => v if k == "module.davinci.pingone_davinci_flow_deploy.pingcli__Flow-0020-A" }
  to       = module.davinci.pingone_davinci_flow_deploy.pingcli__Flow-0020-A
  id       = "${var.pingone_environment_id}/${each.value}"
}

import {
  for_each = { for k, v in var.import_ids : k => v if k == "module.davinci.pingone_davinci_flow_deploy.pingcli__Flow-0020-B" }
  to       = module.davinci.pingone_davinci_flow_deploy.pingcli__Flow-0020-B
  id       = "${var.pingone_environment_id}/${each.value}"
}

import {
  for_each = { for k, v in var.import_ids : k => v if k == "module.davinci.pingone_davinci_flow_deploy.pingcli__Flow-0020-C" }
  to       = module.davinci.pingone_davinci_flow_deploy.pingcli__Flow-0020-C
  id       = "${var.pingone_environment_id}/${each.value}"
}

import {
  for_each = { for k, v in var.import_ids : k => v if k == "module.davinci.pingone_davinci_flow_enable.pingcli__Flow-0020-A" }
  to       = module.davinci.pingone_davinci_flow_enable.pingcli__Flow-0020-A
  id       = "${var.pingone_environment_id}/${each.value}"
}

import {
  for_each = { for k, v in var.import_ids : k => v if k == "module.davinci.pingone_davinci_flow_enable.pingcli__Flow-0020-B" }
  to       = module.davinci.pingone_davinci_flow_enable.pingcli__Flow-0020-B
  id       = "${var.pingone_environment_id}/${each.value}"
}

import {
  for_each = { for k, v in var.import_ids : k => v if k == "module.davinci.pingone_davinci_flow_enable.pingcli__Flow-0020-C" }
  to       = module.davinci.pingone_davinci_flow_enable.pingcli__Flow-0020-C
  id       = "${var.pingone_environment_id}/${each.value}"
}

import {
  for_each = { for k, v in var.import_ids : k => v if k == "module.davinci.pingone_davinci_variable.pingcli__apiKey_company" }
  to       = module.davinci.pingone_davinci_variable.pingcli__apiKey_company
  id       = "${var.pingone_environment_id}/${each.value}"
}

import {
  for_each = { for k, v in var.import_ids : k => v if k == "module.davinci.pingone_davinci_variable.pingcli__attempts_flowInstance" }
  to       = module.davinci.pingone_davinci_variable.pingcli__attempts_flowInstance
  id       = "${var.pingone_environment_id}/${each.value}"
}

import {
  for_each = { for k, v in var.import_ids : k => v if k == "module.davinci.pingone_davinci_variable.pingcli__companyName_company" }
  to       = module.davinci.pingone_davinci_variable.pingcli__companyName_company
  id       = "${var.pingone_environment_id}/${each.value}"
}
