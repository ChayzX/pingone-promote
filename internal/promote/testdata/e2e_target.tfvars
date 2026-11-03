# Managed by pingone-promote (tfvars schema v1). Comments outside this header are not preserved.

tfvars_schema_version  = 1
pingone_environment_id = "00000000-0000-4000-8000-0000000000b2"

parameters = {
  "form_mfa_20subflow_n2form"    = "00000000-0000-4000-8000-00000000e001"
  "target_only"                  = "kept"
  "themeid_mfa_20subflow_n2form" = "00000000-0000-4000-8000-00000000e002"
}

davinci_connections = {
  "Flow-20Conductor" = {
    name      = "Flow Conductor"
    connector = "flowConnector"
  }
  "Http" = {
    name      = "Http"
    connector = "httpConnector"
  }
  "PingOne" = {
    name       = "PingOne"
    connector  = "pingOneSSOConnector"
    properties = <<-JSON
      {
        "clientId": {
          "type": "string",
          "value": "fake-target-client-id"
        },
        "clientSecret": {
          "type": "string",
          "value": "@@secret:PingOne_DaVinci_connection_client@@"
        },
        "envId": {
          "type": "string",
          "value": "@@env:id@@"
        },
        "region": {
          "type": "string",
          "value": "NA"
        }
      }
    JSON
  }
}

davinci_variables = {
  "brand_company" = {
    name         = "brand"
    context      = "company"
    data_type    = "string"
    mutable      = true
    display_name = "Brand"
    value = {
      string = "target-brand"
    }
  }
}

davinci_flows = {}

davinci_applications = {
  "Web-20Login" = {
    name            = "Web Login"
    api_key_enabled = true
    oauth = {
      enforce_signed_request_openid = false
      grant_types                   = ["authorizationCode"]
      redirect_uris                 = ["https://app.test.example.com/cb"]
      scopes                        = ["openid", "profile"]
    }
    flow_policies = {}
  }
}

import_ids = {
  davinci_connections = {
    "PingOne" = "fake-target-conn-0003"
  }
  davinci_flows         = {}
  davinci_variables     = {}
  davinci_applications  = {}
  davinci_flow_policies = {}
}
