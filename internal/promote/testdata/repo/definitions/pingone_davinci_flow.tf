# Flows are managed by pingone-promote; review in merge requests.
resource "pingone_davinci_flow" "pingcli__Flow-0020-A" {
  environment_id = var.pingone_environment_id
  name           = "Flow A"
  description    = "Flow A for the fixture"
  color          = "#AFD5FF"

  settings = {
    csp               = "worker-src 'self' blob:;"
    log_level         = 4
    use_custom_css    = false
    use_custom_script = false
    css_links         = []
  }

  graph_data = {
    data = jsonencode({})
    renderer = jsonencode({
      "name" : "null"
    })
    elements = {
      nodes = {
        "n1aaaaaaaa" = {
          data = {
            id              = "n1aaaaaaaa"
            node_type       = "CONNECTION"
            connection_id   = pingone_davinci_connector_instance.pingcli__Http.id
            connector_id    = "httpConnector"
            name            = "Http"
            label           = "Http"
            capability_name = "createRandomString"
            type            = "action"
            status          = "configured"
            properties = jsonencode({
              "nodeTitle" : {
                "value" : "Flow A start"
              },
              "message" : {
                "value" : "Welcome to {{global.variables.companyName}}"
              }
            })
          }
          position = {
            x = 100
            y = 200
          }
          group      = "nodes"
          removed    = false
          selected   = false
          selectable = true
          locked     = false
          grabbable  = true
          pannable   = false
          classes    = ""
        }
      }
    }
  }

  depends_on = [
    pingone_davinci_variable.pingcli__companyName_company,
  ]
}

resource "pingone_davinci_flow" "pingcli__Flow-0020-B" {
  environment_id = var.pingone_environment_id
  name           = "Flow B"
  description    = "Flow B for the fixture"
  color          = "#E3F0FF"

  settings = {
    csp               = "worker-src 'self' blob:;"
    log_level         = 4
    use_custom_css    = false
    use_custom_script = false
    css_links         = []
  }

  graph_data = {
    data = jsonencode({})
    renderer = jsonencode({
      "name" : "null"
    })
    elements = {
      nodes = {
        "n1aaaaaaaa" = {
          data = {
            id              = "n1aaaaaaaa"
            node_type       = "CONNECTION"
            connection_id   = pingone_davinci_connector_instance.pingcli__PingOne.id
            connector_id    = "pingOneSSOConnector"
            name            = "PingOne"
            label           = "PingOne"
            capability_name = "userLookup"
            type            = "action"
            status          = "configured"
            properties = jsonencode({
              "nodeTitle" : {
                "value" : "Look up the user"
              },
              "message" : {
                "value" : "Welcome to {{global.variables.companyName}}"
              }
            })
          }
          position = {
            x = 100
            y = 200
          }
          group      = "nodes"
          removed    = false
          selected   = false
          selectable = true
          locked     = false
          grabbable  = true
          pannable   = false
          classes    = ""
        }
      }
    }
  }

  depends_on = [
    pingone_davinci_variable.pingcli__apiKey_company,
    pingone_davinci_variable.pingcli__attempts_flowInstance,
    pingone_davinci_variable.pingcli__companyName_company,
  ]
}

resource "pingone_davinci_flow" "pingcli__Flow-0020-C" {
  environment_id = var.pingone_environment_id
  name           = "Flow C"
  description    = "Flow C for the fixture"
  color          = "#FFC8C1"

  settings = {
    csp               = "worker-src 'self' blob:;"
    log_level         = 4
    use_custom_css    = false
    use_custom_script = false
    css_links         = []
  }

  graph_data = {
    data = jsonencode({})
    renderer = jsonencode({
      "name" : "null"
    })
    elements = {
      nodes = {
        "n1aaaaaaaa" = {
          data = {
            id              = "n1aaaaaaaa"
            node_type       = "CONNECTION"
            connection_id   = pingone_davinci_connector_instance.pingcli__PingOne.id
            connector_id    = "pingOneSSOConnector"
            name            = "PingOne"
            label           = "PingOne"
            capability_name = "userLookup"
            type            = "action"
            status          = "configured"
            properties = jsonencode({
              "nodeTitle" : {
                "value" : "Flow C lookup"
              },
              "message" : {
                "value" : "Welcome to {{global.variables.companyName}}"
              }
            })
          }
          position = {
            x = 100
            y = 200
          }
          group      = "nodes"
          removed    = false
          selected   = false
          selectable = true
          locked     = false
          grabbable  = true
          pannable   = false
          classes    = ""
        }
      }
    }
  }

  depends_on = [
    pingone_davinci_variable.pingcli__companyName_company,
  ]
}
