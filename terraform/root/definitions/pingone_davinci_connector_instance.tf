resource "pingone_davinci_connector_instance" "pingcli__Http" {
  environment_id = var.pingone_environment_id
  name           = "Http"

  connector = {
    id = "httpConnector"
  }
}

resource "pingone_davinci_connector_instance" "pingcli__PingOne" {
  environment_id = var.pingone_environment_id
  name           = "PingOne"

  connector = {
    id = "pingOneSSOConnector"
  }
  properties = jsonencode({
    "clientId" : {
      "type" : "string",
      "value" : "fixture-client-id"
    },
    "clientSecret" : {
      "type" : "string",
      "value" : var.PingOne_DaVinci_connection_PingOne_clientSecret
    },
    "envId" : {
      "type" : "string",
      "value" : var.pingone_environment_id
    },
    "region" : {
      "type" : "string",
      "value" : "NA"
    }
  })
}

# Hand-maintained trailing note: the Http connector is shared by Flow A and the subflows.
