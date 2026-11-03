# DaVinci variables. Company, flowInstance and user variables are created
# before flows so flows can reference them; flow-scoped variables need their
# flow's ID and are created after flows, in a separate resource. Logical keys
# encode name, context and flow, so a company variable and a flow variable
# with the same name are distinct instances.

locals {
  # Provider value object for each variable. A secret variable takes its value
  # from pingone_secrets; json_object may use env and param references.
  variable_values = {
    for k, v in var.davinci_variables : k => (
      v.data_type == "secret" ? {
        string        = null
        bool          = null
        float32       = null
        json_object   = null
        secret_string = lookup(var.pingone_secrets, coalesce(v.secret, "-"), null)
        } : v.value == null ? null : {
        string  = v.value.string
        bool    = v.value.bool
        float32 = v.value.float32
        json_object = v.value.json_object == null ? null : join("", [
          for i, p in split("@@", v.value.json_object) : i % 2 == 0 ? p : lookup(local.refs_static, p, "")
        ])
        secret_string = null
      }
    )
  }
}

resource "pingone_davinci_variable" "global" {
  for_each   = { for k, v in var.davinci_variables : k => v if v.context != "flow" }
  depends_on = [terraform_data.contract]

  environment_id = var.pingone_environment_id
  name           = each.value.name
  context        = each.value.context
  data_type      = each.value.data_type
  mutable        = each.value.mutable
  display_name   = each.value.display_name
  min            = each.value.min
  max            = each.value.max
  value          = local.variable_values[each.key]
}

resource "pingone_davinci_variable" "flow_scoped" {
  for_each   = { for k, v in var.davinci_variables : k => v if v.context == "flow" }
  depends_on = [terraform_data.contract]

  environment_id = var.pingone_environment_id
  name           = each.value.name
  context        = each.value.context
  data_type      = each.value.data_type
  mutable        = each.value.mutable
  display_name   = each.value.display_name
  min            = each.value.min
  max            = each.value.max
  value          = local.variable_values[each.key]
  flow = {
    id = lookup(local.flow_ids, coalesce(each.value.flow, "-"), "")
  }
}
