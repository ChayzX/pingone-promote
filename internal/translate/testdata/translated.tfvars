# Managed by pingone-promote (tfvars schema v1). Comments outside this header are not preserved.

tfvars_schema_version  = 1
pingone_environment_id = "00000000-0000-4000-8000-0000000000a1"

parameters = {
  "form_mfa_20subflow_n2form"    = "00000000-0000-4000-8000-00000000f001"
  "themeid_mfa_20subflow_n2form" = "00000000-0000-4000-8000-00000000f002"
}

davinci_connections = {
  "Custom-20API" = {
    name       = "Custom API"
    connector  = "genericConnector"
    properties = <<-JSON
      {
        "apiKey": {
          "type": "string",
          "value": "@@secret:PingOne_DaVinci_connection_Custom_API_apiKey@@"
        },
        "baseUrl": {
          "type": "string",
          "value": "https://api.example.com"
        },
        "loginHint": {
          "secure": true,
          "type": "string",
          "value": "@@secret:PingOne_DaVinci_connection_Custom_API_loginHint@@"
        }
      }
    JSON
  }
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
          "value": "fake-client-id"
        },
        "clientSecret": {
          "type": "string",
          "value": "@@secret:PingOne_DaVinci_connection_PingOne_clientSecret@@"
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
  "PingFedAuthNCreds_company" = {
    name      = "PingFedAuthNCreds"
    context   = "company"
    data_type = "secret"
    mutable   = false
    secret    = "davinci_variable_PingFedAuthNCreds_value"
  }
  "attempts_company" = {
    name      = "attempts"
    context   = "company"
    data_type = "number"
    mutable   = true
    min       = 0
    max       = 10
    value = {
      float32 = 3
    }
  }
  "attempts_flow_Login" = {
    name      = "attempts"
    context   = "flow"
    data_type = "number"
    mutable   = true
    flow      = "Login"
    value = {
      float32 = 0
    }
  }
  "brand_company" = {
    name         = "brand"
    context      = "company"
    data_type    = "string"
    mutable      = true
    display_name = "Brand"
    value = {
      string = "acme"
    }
  }
  "greeting_flowInstance" = {
    name      = "greeting"
    context   = "flowInstance"
    data_type = "string"
    mutable   = true
    value = {
      string = "Hello"
    }
  }
  "theme_company" = {
    name      = "theme"
    context   = "company"
    data_type = "object"
    mutable   = true
    value = {
      json_object = <<-JSON
        {
          "logo": "https://auth.pingone.example/@@env:id@@/logo.png"
        }
      JSON
    }
  }
}

davinci_flows = {
  "Login" = {
    name    = "Login"
    color   = "#AFD5FF"
    tier    = 3
    enabled = true
    settings = {
      csp                          = "script-src 'self'"
      css_links                    = ["https://cdn.example.com/a.css"]
      flow_http_timeout_in_seconds = 300
      log_level                    = 4
      use_csp                      = true
    }
    trigger = {
      type = "AUTHENTICATION"
    }
    graph = {
      box_selection_enabled = true
      max_zoom              = 1e+50
      min_zoom              = 1e-50
      pan = {
        x = 0
        y = 0
      }
      panning_enabled      = true
      renderer             = "{\"name\":\"null\"}"
      user_panning_enabled = true
      user_zooming_enabled = true
      zoom                 = 1
      zooming_enabled      = true
      nodes = {
        "n3eval" = {
          node_type = "EVAL"
          label     = "Evaluator"
          status    = "configured"
          type      = "action"
          position = {
            x = 100
            y = 100
          }
          classes    = ""
          group      = "nodes"
          grabbable  = true
          locked     = false
          pannable   = false
          removed    = false
          selectable = true
          selected   = false
        }
        "n3sso" = {
          node_type       = "CONNECTION"
          connection      = "PingOne"
          connector_id    = "pingOneSSOConnector"
          capability_name = "userLookup"
          label           = "Lookup"
          status          = "configured"
          type            = "action"
          properties      = <<-JSON
            {
              "helper": {
                "value": {
                  "connectionId": "@@connection:Http@@"
                }
              },
              "hmacKey": {
                "value": "@@secret:PingOne_DaVinci_flow_Login_n3sso_hmacKey@@"
              },
              "issuer": {
                "value": "https://auth.pingone.example/@@env:id@@/as"
              },
              "note": {
                "value": "literal @@@@ in text"
              }
            }
          JSON
          position = {
            x = 0
            y = 0
          }
          classes    = ""
          group      = "nodes"
          grabbable  = true
          locked     = false
          pannable   = false
          removed    = false
          selectable = true
          selected   = false
        }
        "n3sub" = {
          node_type       = "CONNECTION"
          connection      = "Flow-20Conductor"
          connector_id    = "flowConnector"
          capability_name = "startUiSubFlow"
          label           = "MFA"
          status          = "configured"
          type            = "action"
          properties      = <<-JSON
            {
              "mf-configObject": {
                "value": "[\n  {\n    \"children\": [\n      {\n        \"text\": \"\"\n      },\n      {\n        \"text\": \"\"\n      },\n      {\n        \"type\": \"link\",\n        \"src\": \"variable.svg\",\n        \"url\": \"mf-configObject\",\n        \"data\": \"{{global.variables.mf-configObject}}\",\n        \"tooltip\": \"{{global.variables.mf-configObject}}\",\n        \"children\": [\n          {\n            \"text\": \"mf-configObject\"\n          }\n        ]\n      },\n      {\n        \"text\": \"\"\n      }\n    ]\n  }\n]"
              },
              "nodeTitle": {
                "value": "MFA subflow"
              },
              "subFlowId": {
                "value": {
                  "label": "MFA subflow",
                  "value": "@@flow:MFA-20subflow@@"
                }
              },
              "subFlowVersionId": {
                "value": -1
              },
              "username": {
                "value": "[\n  {\n    \"children\": [\n      {\n        \"text\": \"\"\n      },\n      {\n        \"text\": \"\"\n      },\n      {\n        \"type\": \"link\",\n        \"src\": \"http.svg\",\n        \"url\": \"username\",\n        \"data\": \"{{local.wzzj7jakza.payload.output.username}}\",\n        \"tooltip\": \"{{local.wzzj7jakza.payload.output.username}}\",\n        \"children\": [\n          {\n            \"text\": \"username\"\n          }\n        ]\n      },\n      {\n        \"text\": \"\"\n      }\n    ]\n  }\n]"
              }
            }
          JSON
          position = {
            x = 200
            y = 0
          }
          classes    = ""
          group      = "nodes"
          grabbable  = true
          locked     = false
          pannable   = false
          removed    = false
          selectable = true
          selected   = false
        }
      }
      edges = {
        "e3a" = {
          source = "n3sso"
          target = "n3sub"
          position = {
            x = 0
            y = 0
          }
          group      = "edges"
          grabbable  = true
          locked     = false
          pannable   = false
          removed    = false
          selectable = true
          selected   = false
        }
        "e3b" = {
          source = "n3sso"
          target = "n3eval"
          position = {
            x = 0
            y = 0
          }
          group      = "edges"
          grabbable  = true
          locked     = false
          pannable   = false
          removed    = false
          selectable = true
          selected   = false
        }
      }
    }
  }
  "MFA-20subflow" = {
    name        = "MFA subflow"
    description = "Second factor"
    tier        = 2
    enabled     = false
    settings = {
      log_level = 4
    }
    input_schema = [
      {
        preferred_control_type = "textField"
        preferred_data_type    = "string"
        property_name          = "username"
        required               = true
      },
    ]
    output_schema = {
      output = {
        type                  = "object"
        properties            = <<-JSON
          {
            "result": {
              "type": "string"
            }
          }
        JSON
        additional_properties = false
      }
    }
    graph = {
      box_selection_enabled = true
      max_zoom              = 1e+50
      min_zoom              = 1e-50
      pan = {
        x = 0
        y = 0
      }
      panning_enabled      = true
      renderer             = "{\"name\":\"null\"}"
      user_panning_enabled = true
      user_zooming_enabled = true
      zoom                 = 1
      zooming_enabled      = true
      nodes = {
        "n2form" = {
          node_type       = "CONNECTION"
          connection      = "Http"
          connector_id    = "httpConnector"
          capability_name = "customHTMLTemplate"
          label           = "Form"
          status          = "configured"
          type            = "action"
          properties      = <<-JSON
            {
              "form": {
                "value": "@@param:form_mfa_20subflow_n2form@@"
              },
              "message": {
                "value": "Hi {{global.variables.brand}}, 100%%{done} $${x}"
              },
              "themeId": {
                "value": "@@param:themeid_mfa_20subflow_n2form@@"
              }
            }
          JSON
          position = {
            x = 400
            y = 0
          }
          classes    = ""
          group      = "nodes"
          grabbable  = true
          locked     = false
          pannable   = false
          removed    = false
          selectable = true
          selected   = false
        }
        "n2save" = {
          node_type       = "CONNECTION"
          connection      = "Http"
          connector_id    = "variablesConnector"
          capability_name = "saveValue"
          label           = "Save"
          status          = "configured"
          type            = "action"
          properties      = <<-JSON
            {
              "nodeTitle": {
                "value": "Start"
              },
              "saveVariables": {
                "value": [
                  {
                    "key": 0.7604670177003245,
                    "label": "mf-configObject (object - flowInstance)",
                    "name": "mf-configObject",
                    "nameDefault": "mf-configObject",
                    "type": "object",
                    "value": "[\n  {\n    \"children\": [\n      {\n        \"text\": \"\"\n      },\n      {\n        \"text\": \"\"\n      },\n      {\n        \"type\": \"link\",\n        \"src\": \"auth.svg\",\n        \"url\": \"mf-configObject\",\n        \"data\": \"{{global.parameters.mf-configObject}}\",\n        \"tooltip\": \"{{global.parameters.mf-configObject}}\",\n        \"children\": [\n          {\n            \"text\": \"mf-configObject\"\n          }\n        ]\n      },\n      {\n        \"text\": \"\"\n      }\n    ]\n  }\n]"
                  }
                ]
              }
            }
          JSON
          position = {
            x = 0
            y = 0
          }
          classes    = ""
          group      = "nodes"
          grabbable  = true
          locked     = false
          pannable   = false
          removed    = false
          selectable = true
          selected   = false
        }
        "n2sub" = {
          node_type       = "CONNECTION"
          connection      = "Flow-20Conductor"
          connector_id    = "flowConnector"
          capability_name = "startUiSubFlow"
          label           = "Forgot password"
          status          = "configured"
          type            = "action"
          properties      = <<-JSON
            {
              "mf-configObject": {
                "value": "[\n  {\n    \"children\": [\n      {\n        \"text\": \"\"\n      },\n      {\n        \"text\": \"\"\n      },\n      {\n        \"type\": \"link\",\n        \"src\": \"variable.svg\",\n        \"url\": \"mf-configObject\",\n        \"data\": \"{{global.variables.mf-configObject}}\",\n        \"tooltip\": \"{{global.variables.mf-configObject}}\",\n        \"children\": [\n          {\n            \"text\": \"mf-configObject\"\n          }\n        ]\n      },\n      {\n        \"text\": \"\"\n      }\n    ]\n  }\n]"
              },
              "nodeTitle": {
                "value": "PingOne SSO Forgot Password subflow"
              },
              "subFlowId": {
                "value": {
                  "label": "PingOne SSO Forgot Password subflow",
                  "value": "@@flow:PingOne-20SSO-20Forgot-20Password-20subflow@@"
                }
              },
              "subFlowVersionId": {
                "value": -1
              },
              "username": {
                "value": "[\n  {\n    \"children\": [\n      {\n        \"text\": \"\"\n      },\n      {\n        \"text\": \"\"\n      },\n      {\n        \"type\": \"link\",\n        \"src\": \"http.svg\",\n        \"url\": \"username\",\n        \"data\": \"{{local.wzzj7jakza.payload.output.username}}\",\n        \"tooltip\": \"{{local.wzzj7jakza.payload.output.username}}\",\n        \"children\": [\n          {\n            \"text\": \"username\"\n          }\n        ]\n      },\n      {\n        \"text\": \"\"\n      }\n    ]\n  }\n]"
              }
            }
          JSON
          position = {
            x = 200
            y = 0
          }
          classes    = ""
          group      = "nodes"
          grabbable  = true
          locked     = false
          pannable   = false
          removed    = false
          selectable = true
          selected   = false
        }
      }
      edges = {
        "e2a" = {
          source = "n2save"
          target = "n2sub"
          position = {
            x = 0
            y = 0
          }
          group      = "edges"
          grabbable  = true
          locked     = false
          pannable   = false
          removed    = false
          selectable = true
          selected   = false
        }
        "e2b" = {
          source = "n2sub"
          target = "n2form"
          position = {
            x = 0
            y = 0
          }
          group      = "edges"
          grabbable  = true
          locked     = false
          pannable   = false
          removed    = false
          selectable = true
          selected   = false
        }
      }
    }
  }
  "PingOne-20SSO-20Forgot-20Password-20subflow" = {
    name    = "PingOne SSO Forgot Password subflow"
    tier    = 1
    enabled = true
    settings = {
      log_level      = 2
      use_custom_css = false
    }
    graph = {
      box_selection_enabled = true
      max_zoom              = 1e+50
      min_zoom              = 1e-50
      pan = {
        x = 0
        y = 0
      }
      panning_enabled      = true
      renderer             = "{\"name\":\"null\"}"
      user_panning_enabled = true
      user_zooming_enabled = true
      zoom                 = 1
      zooming_enabled      = true
      nodes = {
        "n1eq" = {
          node_type       = "CONNECTION"
          connection      = "Http"
          connector_id    = "functionsConnector"
          capability_name = "AEqualsB"
          label           = "A equals B"
          status          = "configured"
          type            = "action"
          properties      = <<-JSON
            {
              "caseSensitive": {
                "value": false
              },
              "leftValueA": {
                "value": "[\n  {\n    \"children\": [\n      {\n        \"text\": \"\"\n      },\n      {\n        \"text\": \"\"\n      },\n      {\n        \"type\": \"link\",\n        \"src\": \"variable.svg\",\n        \"url\": \"gv-autoEnrollEmail\",\n        \"data\": \"{{global.company.variables.gv-autoEnrollEmail}}\",\n        \"tooltip\": \"{{global.company.variables.gv-autoEnrollEmail}}\",\n        \"children\": [\n          {\n            \"text\": \"gv-autoEnrollEmail\"\n          }\n        ]\n      },\n      {\n        \"text\": \"\"\n      }\n    ]\n  }\n]"
              },
              "nodeTitle": {
                "value": "Check if we should autoenroll Email -gv-autoEnrollEmail"
              },
              "rightValueB": {
                "value": "[\n  {\n    \"children\": [\n      {\n        \"text\": \"true\"\n      }\n    ]\n  }\n]"
              }
            }
          JSON
          position = {
            x = 300
            y = 100
          }
          classes    = ""
          group      = "nodes"
          grabbable  = true
          locked     = false
          pannable   = false
          removed    = false
          selectable = true
          selected   = false
        }
        "n1err" = {
          node_type       = "CONNECTION"
          connection      = "Http"
          connector_id    = "errorConnector"
          capability_name = "customErrorMessage"
          label           = "Error"
          status          = "configured"
          type            = "action"
          properties      = <<-JSON
            {
              "errorCode": {
                "value": "[\n  {\n    \"children\": [\n      {\n        \"text\": \"Unexpected password state\"\n      }\n    ]\n  }\n]"
              },
              "errorDescription": {
                "value": "[\n  {\n    \"children\": [\n      {\n        \"text\": \"An unexpected password state was received\"\n      }\n    ]\n  }\n]"
              },
              "errorMessage": {
                "value": "[\n  {\n    \"children\": [\n      {\n        \"text\": \"\"\n      },\n      {\n        \"text\": \"An unexpected error has occurred\"\n      }\n    ]\n  }\n]"
              },
              "errorReason": {
                "value": "[\n  {\n    \"children\": [\n      {\n        \"text\": \"Received an unexpected password state value from PingOne\"\n      }\n    ]\n  }\n]"
              },
              "nodeDescription": {
                "value": "Received an unexpected value for the password status"
              },
              "nodeTitle": {
                "value": "Received an unexpected value"
              }
            }
          JSON
          position = {
            x = 100
            y = 100
          }
          classes    = ""
          group      = "nodes"
          grabbable  = true
          locked     = false
          pannable   = false
          removed    = false
          selectable = true
          selected   = false
        }
      }
      edges = {
        "e1a" = {
          source = "n1eq"
          target = "n1err"
          position = {
            x = 0
            y = 0
          }
          group      = "edges"
          grabbable  = true
          locked     = false
          pannable   = false
          removed    = false
          selectable = true
          selected   = false
        }
      }
    }
  }
}

davinci_applications = {
  "Web-20Login" = {
    name            = "Web Login"
    api_key_enabled = true
    oauth = {
      enforce_signed_request_openid = false
      grant_types                   = ["authorizationCode"]
      logout_uris                   = ["https://app.dev.example.com/logout"]
      redirect_uris                 = ["https://app.dev.example.com/cb"]
      scopes                        = ["openid", "profile"]
    }
    flow_policies = {
      "Login-20policy" = {
        name   = "Login policy"
        status = "enabled"
        distributions = [
          {
            flow    = "Login"
            version = -1
            weight  = 100
          },
        ]
      }
    }
  }
}

import_ids = {
  davinci_connections = {
    "Custom-20API"     = "c0000000000000000000000000000004"
    "Flow-20Conductor" = "c0000000000000000000000000000002"
    "Http"             = "c0000000000000000000000000000001"
    "PingOne"          = "c0000000000000000000000000000003"
  }
  davinci_flows = {
    "Login"                                       = "f0000000000000000000000000000003"
    "MFA-20subflow"                               = "f0000000000000000000000000000002"
    "PingOne-20SSO-20Forgot-20Password-20subflow" = "f0000000000000000000000000000001"
  }
  davinci_variables = {
    "PingFedAuthNCreds_company" = "00000000-0000-4000-8000-0000000000b2"
    "attempts_company"          = "00000000-0000-4000-8000-0000000000b3"
    "attempts_flow_Login"       = "00000000-0000-4000-8000-0000000000b4"
    "brand_company"             = "00000000-0000-4000-8000-0000000000b1"
    "greeting_flowInstance"     = "00000000-0000-4000-8000-0000000000b5"
    "theme_company"             = "00000000-0000-4000-8000-0000000000b6"
  }
  davinci_applications = {
    "Web-20Login" = "a0000000000000000000000000000001"
  }
  davinci_flow_policies = {
    "Web-20Login/Login-20policy" = "a0000000000000000000000000000001/b0000000000000000000000000000001"
  }
}
