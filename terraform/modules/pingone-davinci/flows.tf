# DaVinci flows in fixed tiers t0..t3, then deploy and enable.
#
# Each tier is its own resource so that tier N can reference the IDs of flows
# in tiers below N (see refs.tf). The tier blocks below are identical except
# for the tier number and the refs map they use. Raising a flow's tier moves
# it to another resource address, which Terraform treats as destroy and
# create; see README.md for the moved/state mv runbook.

locals {
  flows_by_tier = {
    for t in [0, 1, 2, 3] : t => { for k, f in var.davinci_flows : k => f if f.tier == t }
  }

  # graph_data without node properties; connection keys become connection IDs.
  graph_canvas = {
    for k, f in var.davinci_flows : k => {
      box_selection_enabled = f.graph.box_selection_enabled
      data                  = f.graph.data
      max_zoom              = f.graph.max_zoom
      min_zoom              = f.graph.min_zoom
      pan                   = f.graph.pan
      panning_enabled       = f.graph.panning_enabled
      renderer              = f.graph.renderer
      user_panning_enabled  = f.graph.user_panning_enabled
      user_zooming_enabled  = f.graph.user_zooming_enabled
      zoom                  = f.graph.zoom
      zooming_enabled       = f.graph.zooming_enabled
    }
  }

  graph_nodes = {
    for k, f in var.davinci_flows : k => {
      for nid, n in f.graph.nodes : nid => {
        node_type        = n.node_type
        connection_id    = n.connection == null ? null : lookup(local.connection_ids, n.connection, "")
        connector_id     = n.connector_id
        capability_name  = n.capability_name
        capability_class = n.capability_class
        label            = n.label
        status           = n.status
        type             = n.type
        outcomes         = n.outcomes
      }
    }
  }

  graph_edges = {
    for k, f in var.davinci_flows : k => length(f.graph.edges) == 0 ? null : {
      for eid, e in f.graph.edges : eid => {
        data = {
          source                = e.source
          target                = e.target
          multi_value_source_id = e.multi_value_source_id
        }
        position   = e.position
        classes    = e.classes
        group      = e.group
        grabbable  = e.grabbable
        locked     = e.locked
        pannable   = e.pannable
        removed    = e.removed
        selectable = e.selectable
        selected   = e.selected
      }
    }
  }

  # output_schema.output.properties may use env and param references.
  output_schemas = {
    for k, f in var.davinci_flows : k => f.output_schema == null ? null : {
      output = {
        type                  = f.output_schema.output.type
        additional_properties = f.output_schema.output.additional_properties
        properties = f.output_schema.output.properties == null ? null : join("", [
          for i, p in split("@@", f.output_schema.output.properties) : i % 2 == 0 ? p : lookup(local.refs_static, p, "")
        ])
      }
    }
  }

  props_t0 = {
    for k, f in local.flows_by_tier[0] : k => {
      for nid, n in f.graph.nodes : nid => n.properties == null ? null : join("", [
        for i, p in split("@@", n.properties) : i % 2 == 0 ? p : lookup(local.refs_t0, p, "")
      ])
    }
  }

  props_t1 = {
    for k, f in local.flows_by_tier[1] : k => {
      for nid, n in f.graph.nodes : nid => n.properties == null ? null : join("", [
        for i, p in split("@@", n.properties) : i % 2 == 0 ? p : lookup(local.refs_t1, p, "")
      ])
    }
  }

  props_t2 = {
    for k, f in local.flows_by_tier[2] : k => {
      for nid, n in f.graph.nodes : nid => n.properties == null ? null : join("", [
        for i, p in split("@@", n.properties) : i % 2 == 0 ? p : lookup(local.refs_t2, p, "")
      ])
    }
  }

  props_t3 = {
    for k, f in local.flows_by_tier[3] : k => {
      for nid, n in f.graph.nodes : nid => n.properties == null ? null : join("", [
        for i, p in split("@@", n.properties) : i % 2 == 0 ? p : lookup(local.refs_t3, p, "")
      ])
    }
  }
}

resource "pingone_davinci_flow" "t0" {
  for_each   = local.flows_by_tier[0]
  depends_on = [terraform_data.contract]

  environment_id = var.pingone_environment_id
  name           = each.value.name
  description    = each.value.description
  color          = each.value.color
  settings       = each.value.settings
  trigger        = each.value.trigger
  input_schema   = each.value.input_schema
  output_schema  = local.output_schemas[each.key]
  graph_data = merge(local.graph_canvas[each.key], {
    elements = {
      nodes = length(each.value.graph.nodes) == 0 ? null : {
        for nid, n in each.value.graph.nodes : nid => {
          data       = merge(local.graph_nodes[each.key][nid], { properties = local.props_t0[each.key][nid] })
          position   = n.position
          classes    = n.classes
          group      = n.group
          grabbable  = n.grabbable
          locked     = n.locked
          pannable   = n.pannable
          removed    = n.removed
          selectable = n.selectable
          selected   = n.selected
        }
      }
      edges = local.graph_edges[each.key]
    }
  })
}

resource "pingone_davinci_flow" "t1" {
  for_each   = local.flows_by_tier[1]
  depends_on = [terraform_data.contract]

  environment_id = var.pingone_environment_id
  name           = each.value.name
  description    = each.value.description
  color          = each.value.color
  settings       = each.value.settings
  trigger        = each.value.trigger
  input_schema   = each.value.input_schema
  output_schema  = local.output_schemas[each.key]
  graph_data = merge(local.graph_canvas[each.key], {
    elements = {
      nodes = length(each.value.graph.nodes) == 0 ? null : {
        for nid, n in each.value.graph.nodes : nid => {
          data       = merge(local.graph_nodes[each.key][nid], { properties = local.props_t1[each.key][nid] })
          position   = n.position
          classes    = n.classes
          group      = n.group
          grabbable  = n.grabbable
          locked     = n.locked
          pannable   = n.pannable
          removed    = n.removed
          selectable = n.selectable
          selected   = n.selected
        }
      }
      edges = local.graph_edges[each.key]
    }
  })
}

resource "pingone_davinci_flow" "t2" {
  for_each   = local.flows_by_tier[2]
  depends_on = [terraform_data.contract]

  environment_id = var.pingone_environment_id
  name           = each.value.name
  description    = each.value.description
  color          = each.value.color
  settings       = each.value.settings
  trigger        = each.value.trigger
  input_schema   = each.value.input_schema
  output_schema  = local.output_schemas[each.key]
  graph_data = merge(local.graph_canvas[each.key], {
    elements = {
      nodes = length(each.value.graph.nodes) == 0 ? null : {
        for nid, n in each.value.graph.nodes : nid => {
          data       = merge(local.graph_nodes[each.key][nid], { properties = local.props_t2[each.key][nid] })
          position   = n.position
          classes    = n.classes
          group      = n.group
          grabbable  = n.grabbable
          locked     = n.locked
          pannable   = n.pannable
          removed    = n.removed
          selectable = n.selectable
          selected   = n.selected
        }
      }
      edges = local.graph_edges[each.key]
    }
  })
}

resource "pingone_davinci_flow" "t3" {
  for_each   = local.flows_by_tier[3]
  depends_on = [terraform_data.contract]

  environment_id = var.pingone_environment_id
  name           = each.value.name
  description    = each.value.description
  color          = each.value.color
  settings       = each.value.settings
  trigger        = each.value.trigger
  input_schema   = each.value.input_schema
  output_schema  = local.output_schemas[each.key]
  graph_data = merge(local.graph_canvas[each.key], {
    elements = {
      nodes = length(each.value.graph.nodes) == 0 ? null : {
        for nid, n in each.value.graph.nodes : nid => {
          data       = merge(local.graph_nodes[each.key][nid], { properties = local.props_t3[each.key][nid] })
          position   = n.position
          classes    = n.classes
          group      = n.group
          grabbable  = n.grabbable
          locked     = n.locked
          pannable   = n.pannable
          removed    = n.removed
          selectable = n.selectable
          selected   = n.selected
        }
      }
      edges = local.graph_edges[each.key]
    }
  })
}

locals {
  props_all = merge(local.props_t0, local.props_t1, local.props_t2, local.props_t3)

  # Redeploy when the authored definition or anything it resolves to changes:
  # the tfvars entry, resolved node properties (subflow, variable and
  # connection IDs, secrets) and node connection IDs. The digest is marked
  # nonsensitive so plans show when a redeploy happens; it is a SHA-256 over
  # the whole rendered definition, not over any single secret.
  deploy_digests = {
    for k, f in var.davinci_flows : k => nonsensitive(sha256(jsonencode({
      definition  = f
      properties  = local.props_all[k]
      connections = { for nid, n in local.graph_nodes[k] : nid => n.connection_id }
    })))
  }
}

resource "pingone_davinci_flow_deploy" "this" {
  for_each = { for k, f in var.davinci_flows : k => f if f.deploy }

  environment_id = var.pingone_environment_id
  flow_id        = local.flow_ids[each.key]
  deploy_trigger_values = {
    definition = local.deploy_digests[each.key]
  }
}

resource "pingone_davinci_flow_enable" "this" {
  for_each   = var.davinci_flows
  depends_on = [pingone_davinci_flow_deploy.this]

  environment_id = var.pingone_environment_id
  flow_id        = local.flow_ids[each.key]
  enabled        = each.value.enabled
}
