# Contract checks. Each category is its own terraform_data so a failing plan
# names the category, and tests can expect one specific failure. Messages list
# key NAMES only, never values. Every managed resource depends on
# terraform_data.contract, so nothing is planned past a failed contract.
#
# These checks mirror internal/tfvars Validate (pingone-promote check); the
# module keeps them so that a hand-edited or stale tfvars file still fails
# safely in the pipeline.

locals {
  ref_token_pattern = "^(|env:id|(secret|param):[a-z0-9_]{1,128}|(connection|flow|variable):[A-Za-z0-9_-]{1,128})$"

  # Placeholder kinds allowed per JSON field scope.
  ref_scopes = {
    static     = ["", "env", "param"]
    connection = ["", "env", "param", "secret"]
    node       = ["", "env", "param", "secret", "connection", "variable", "flow"]
  }

  # Every JSON heredoc the module resolves.
  json_fields = concat(
    [for k, c in var.davinci_connections : { where = "davinci_connections[\"${k}\"].properties", text = c.properties, scope = "connection", tier = -1 } if c.properties != null],
    [for k, v in var.davinci_variables : { where = "davinci_variables[\"${k}\"].value.json_object", text = v.value.json_object, scope = "static", tier = -1 } if try(v.value.json_object, null) != null],
    [for k, f in var.davinci_flows : { where = "davinci_flows[\"${k}\"].output_schema.output.properties", text = f.output_schema.output.properties, scope = "static", tier = -1 } if try(f.output_schema.output.properties, null) != null],
    flatten([for k, f in var.davinci_flows : [
      for nid, n in f.graph.nodes : { where = "davinci_flows[\"${k}\"].graph.nodes[\"${nid}\"].properties", text = n.properties, scope = "node", tier = f.tier } if n.properties != null
    ]]),
  )

  unbalanced_fields = [for jf in local.json_fields : jf.where if length(split("@@", jf.text)) % 2 == 0]

  json_refs = flatten([for jf in local.json_fields : [
    for i, tok in split("@@", jf.text) : {
      where = jf.where
      scope = jf.scope
      tier  = jf.tier
      token = tok
      valid = can(regex(local.ref_token_pattern, tok))
      kind  = tok == "" ? "" : split(":", tok)[0]
      key   = tok == "" ? "" : join(":", slice(split(":", tok), 1, length(split(":", tok))))
    } if i % 2 == 1
  ]])

  valid_refs = [for r in local.json_refs : r if r.valid]

  supplied_secret_keys = nonsensitive(keys(var.pingone_secrets))

  referenced_secrets = distinct(concat(
    [for r in local.valid_refs : r.key if r.kind == "secret"],
    [for k, v in var.davinci_variables : v.secret if v.data_type == "secret" && v.secret != null],
  ))
  missing_secrets    = sort([for s in local.referenced_secrets : s if !contains(local.supplied_secret_keys, s)])
  missing_parameters = sort(distinct([for r in local.valid_refs : r.key if r.kind == "param" && !contains(keys(var.parameters), r.key)]))

  reference_problems = concat(
    [for w in local.unbalanced_fields : "${w}: unpaired @@ (write a literal @@ as @@@@)"],
    [for r in local.json_refs : "${r.where}: malformed placeholder" if !r.valid],
    [for r in local.valid_refs : "${r.where}: placeholder kind \"${r.kind}\" is not allowed here" if !contains(local.ref_scopes[r.scope], r.kind)],
    [for r in local.valid_refs : "${r.where}: unknown connection \"${r.key}\"" if r.kind == "connection" && !contains(keys(var.davinci_connections), r.key)],
    [for r in local.valid_refs : "${r.where}: unknown flow \"${r.key}\"" if r.kind == "flow" && !contains(keys(var.davinci_flows), r.key)],
    [for r in local.valid_refs : "${r.where}: unknown or flow-scoped variable \"${r.key}\"" if r.kind == "variable" && try(var.davinci_variables[r.key].context, "flow") == "flow"],
    flatten([for k, f in var.davinci_flows : [
      for nid, n in f.graph.nodes : "davinci_flows[\"${k}\"].graph.nodes[\"${nid}\"].connection: unknown connection \"${n.connection}\""
      if n.connection != null && !contains(keys(var.davinci_connections), coalesce(n.connection, "-"))
    ]]),
    flatten([for k, f in var.davinci_flows : [
      for eid, e in f.graph.edges : "davinci_flows[\"${k}\"].graph.edges[\"${eid}\"]: source or target is not a node of this flow"
      if !contains(keys(f.graph.nodes), e.source) || !contains(keys(f.graph.nodes), e.target)
    ]]),
    flatten([for ak, a in var.davinci_applications : [
      for pk, p in a.flow_policies : [
        for i, d in p.distributions : "davinci_applications[\"${ak}\"].flow_policies[\"${pk}\"].distributions[${i}].flow: unknown flow \"${d.flow}\""
        if !contains(keys(var.davinci_flows), d.flow)
      ]
    ]]),
  )

  tier_problems = [
    for r in local.valid_refs : "${r.where}: flow \"${r.key}\" is in tier ${var.davinci_flows[r.key].tier}; a tier ${r.tier} flow may reference only lower tiers"
    if r.kind == "flow" && r.scope == "node" && contains(keys(var.davinci_flows), r.key) && try(var.davinci_flows[r.key].tier >= r.tier, false)
  ]

  variable_identities = { for k, v in var.davinci_variables : k => jsonencode([v.name, v.context, v.flow]) }
  variable_problems = concat(
    [for k, v in var.davinci_variables : "davinci_variables[\"${k}\"].flow: required exactly when context is \"flow\"" if(v.context == "flow") != (v.flow != null)],
    [for k, v in var.davinci_variables : "davinci_variables[\"${k}\"].flow: unknown flow \"${v.flow}\"" if v.flow != null && !contains(keys(var.davinci_flows), coalesce(v.flow, "-"))],
    [for k, v in var.davinci_variables : "davinci_variables[\"${k}\"]: data_type \"secret\" needs secret and no value" if v.data_type == "secret" && (v.secret == null || v.value != null)],
    [for k, v in var.davinci_variables : "davinci_variables[\"${k}\"].secret: allowed only when data_type is \"secret\"" if v.data_type != "secret" && v.secret != null],
    [for k, v in var.davinci_variables : "davinci_variables[\"${k}\"].value: set at most one of string, bool, float32, json_object" if v.value != null && length([for x in [try(v.value.string, null), try(v.value.bool, null), try(v.value.float32, null), try(v.value.json_object, null)] : x if x != null]) > 1],
    [for k, id in local.variable_identities : "davinci_variables[\"${k}\"]: another entry has the same name, context and flow" if length([for k2, id2 in local.variable_identities : k2 if id2 == id]) > 1],
  )

  import_problems = concat(
    [for k in keys(var.import_ids.davinci_connections) : "import_ids.davinci_connections[\"${k}\"]: no such connection" if !contains(keys(var.davinci_connections), k)],
    [for k in keys(var.import_ids.davinci_flows) : "import_ids.davinci_flows[\"${k}\"]: no such flow" if !contains(keys(var.davinci_flows), k)],
    [for k in keys(var.import_ids.davinci_variables) : "import_ids.davinci_variables[\"${k}\"]: no such variable" if !contains(keys(var.davinci_variables), k)],
    [for k in keys(var.import_ids.davinci_applications) : "import_ids.davinci_applications[\"${k}\"]: no such application" if !contains(keys(var.davinci_applications), k)],
    [for k in keys(var.import_ids.davinci_flow_policies) : "import_ids.davinci_flow_policies[\"${k}\"]: no such application flow policy" if !contains(keys(local.flow_policies), k)],
    [for k, id in var.import_ids.davinci_flow_policies : "import_ids.davinci_flow_policies[\"${k}\"]: ID must be \"<application_id>/<policy_id>\"" if length(split("/", id)) != 2],
  )
}

resource "terraform_data" "contract_secrets" {
  lifecycle {
    precondition {
      condition     = length(local.missing_secrets) == 0
      error_message = "pingone_secrets is missing ${length(local.missing_secrets)} key(s) referenced by the tfvars: ${join(", ", local.missing_secrets)}. Supply them through TF_VAR_pingone_secrets."
    }
    precondition {
      condition     = length(local.missing_parameters) == 0
      error_message = "parameters is missing key(s) referenced as @@param:<key>@@: ${join(", ", local.missing_parameters)}."
    }
  }
}

resource "terraform_data" "contract_references" {
  lifecycle {
    precondition {
      condition     = length(local.reference_problems) == 0
      error_message = "Unresolved references:\n${join("\n", local.reference_problems)}"
    }
  }
}

resource "terraform_data" "contract_tiers" {
  lifecycle {
    precondition {
      condition     = length(local.tier_problems) == 0
      error_message = "Subflow tier ordering violated:\n${join("\n", local.tier_problems)}"
    }
  }
}

resource "terraform_data" "contract_variables" {
  lifecycle {
    precondition {
      condition     = length(local.variable_problems) == 0
      error_message = "Invalid davinci_variables:\n${join("\n", local.variable_problems)}"
    }
  }
}

resource "terraform_data" "contract_imports" {
  # Records which tier resource each flow import targets (logical keys and
  # PingOne IDs only), so plans show the adoption routing.
  input = { for t, m in local.import_flows_by_tier : "t${t}" => keys(m) }

  lifecycle {
    precondition {
      condition     = length(local.import_problems) == 0
      error_message = "Invalid import_ids:\n${join("\n", local.import_problems)}"
    }
  }
}

# Single dependency anchor for every managed resource.
resource "terraform_data" "contract" {
  depends_on = [
    terraform_data.contract_secrets,
    terraform_data.contract_references,
    terraform_data.contract_tiers,
    terraform_data.contract_variables,
    terraform_data.contract_imports,
  ]
}
