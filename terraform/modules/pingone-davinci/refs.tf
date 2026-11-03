# Placeholder resolution.
#
# JSON heredocs carry cross-references as @@kind:key@@. Resolution splits on
# "@@": even segments are literal text and odd segments are reference tokens
# looked up in a refs map. "@@@@" yields the empty token, which maps to a
# literal "@@".
#
#   join("", [for i, p in split("@@", s) : i % 2 == 0 ? p : lookup(refs, p, "")])
#
# Values substituted inside JSON strings are JSON-escaped with
# substr(jsonencode(v), 1, length(jsonencode(v)) - 2). trim() is not used
# because it would also strip an escaped trailing quote.
#
# lookup() falls back to "" so that an unknown token never raises a raw
# "Invalid index" error; contract.tf reports every unknown token by name and
# every resource depends on the contract, so nothing is applied unresolved.
#
# A for_each resource cannot refer to its own instances, so flows are split
# into fixed tiers t0..t3 and each tier sees only the flows below it:
#   refs_t0 = static + secrets + connections + global variables
#   refs_t1 = refs_t0 + t0 flows, refs_t2 = refs_t1 + t1 flows, ...

locals {
  refs_static = merge(
    { for k, v in var.parameters : "param:${k}" => substr(jsonencode(v), 1, length(jsonencode(v)) - 2) },
    { "env:id" = var.pingone_environment_id, "" = "@@" },
  )

  refs_secrets = { for k, v in var.pingone_secrets : "secret:${k}" => substr(jsonencode(v), 1, length(jsonencode(v)) - 2) if v != null }

  # Connector instance properties may use env, param and secret references.
  refs_connection = merge(local.refs_static, local.refs_secrets)

  connection_ids      = { for k, c in pingone_davinci_connector_instance.this : k => c.id }
  global_variable_ids = { for k, v in pingone_davinci_variable.global : k => v.id }

  refs_t0 = merge(
    local.refs_connection,
    { for k, id in local.connection_ids : "connection:${k}" => id },
    { for k, id in local.global_variable_ids : "variable:${k}" => id },
  )
  refs_t1 = merge(local.refs_t0, { for k, f in pingone_davinci_flow.t0 : "flow:${k}" => f.id })
  refs_t2 = merge(local.refs_t1, { for k, f in pingone_davinci_flow.t1 : "flow:${k}" => f.id })
  refs_t3 = merge(local.refs_t2, { for k, f in pingone_davinci_flow.t2 : "flow:${k}" => f.id })

  flow_ids = merge(
    { for k, f in pingone_davinci_flow.t0 : k => f.id },
    { for k, f in pingone_davinci_flow.t1 : k => f.id },
    { for k, f in pingone_davinci_flow.t2 : k => f.id },
    { for k, f in pingone_davinci_flow.t3 : k => f.id },
  )
}
