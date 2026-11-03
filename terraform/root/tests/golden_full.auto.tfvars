# Managed by pingone-promote (tfvars schema v1). Comments outside this header are not preserved.

tfvars_schema_version  = 1
pingone_environment_id = "00000000-0000-4000-8000-000000000001"

parameters = {
  "idp_base_url"  = "https://idp.dev.example.com"
  "support_email" = "support@example.com"
}

davinci_connections = {
  "flow_conductor" = {
    name      = "Flow Conductor"
    connector = "flowConnector"
  }
  "functions" = {
    name       = "Functions"
    connector  = "functionsConnector"
    properties = <<-JSON
      {
        "note": {
          "type": "string",
          "value": "literal @@@@ stays"
        }
      }
    JSON
  }
  "http" = {
    name      = "Http"
    connector = "httpConnector"
  }
  "pingone_sso" = {
    name       = "PingOne"
    connector  = "pingOneSSOConnector"
    properties = <<-JSON
      {
        "baseUrl": {
          "type": "string",
          "value": "@@param:idp_base_url@@/as"
        },
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
        }
      }
    JSON
  }
}

davinci_variables = {
  "apiToken_company" = {
    name      = "apiToken"
    context   = "company"
    data_type = "secret"
    mutable   = false
    secret    = "davinci_variable_apiToken_value"
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
  "attempts_flow_login" = {
    name      = "attempts"
    context   = "flow"
    data_type = "number"
    mutable   = true
    flow      = "login"
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
      string = "Hello $${user.name}"
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
          "dark": false,
          "logo": "@@param:idp_base_url@@/logo.png"
        }
      JSON
    }
  }
}

davinci_flows = {
  "error_leaf" = {
    name = "Error leaf"
    tier = 0
    graph = {
      nodes = {
        "n0err1" = {
          node_type       = "CONNECTION"
          connection      = "http"
          connector_id    = "httpConnector"
          capability_name = "customHtmlMessage"
          label           = "Error"
          properties      = <<-JSON
            {
              "message": {
                "value": "Hi $${global.variables.brand}, 100%%{done}"
              }
            }
          JSON
          position = {
            x = 277
            y = 236.25
          }
        }
      }
      edges = {}
    }
  }
  "login" = {
    name        = "Login"
    description = "Primary sign-on"
    color       = "#AFD5FF"
    tier        = 3
    enabled     = true
    settings = {
      csp                          = "script-src 'self'"
      css_links                    = ["https://cdn.example.com/a.css", "https://cdn.example.com/b.css"]
      flow_http_timeout_in_seconds = 300
      log_level                    = 2
      use_csp                      = true
    }
    trigger = {
      type = "AUTHENTICATION"
    }
    graph = {
      max_zoom = 1e+50
      min_zoom = 1e-50
      pan = {
        x = 0
        y = -12.5
      }
      panning_enabled = true
      renderer        = "{\"name\":\"null\"}"
      zoom            = 1
      nodes = {
        "a1b2c3" = {
          node_type       = "CONNECTION"
          connection      = "pingone_sso"
          connector_id    = "pingOneSSOConnector"
          capability_name = "userLookup"
          properties      = <<-JSON
            {
              "hmacKey": {
                "value": "@@secret:login_hmac_key@@"
              },
              "support": {
                "value": "mailto:@@param:support_email@@"
              },
              "username": {
                "value": "$${global.parameters.username}"
              }
            }
          JSON
          outcomes = [
            {
              id     = "found"
              label  = "Found"
              result = "true"
            },
          ]
          position = {
            x = 240
            y = 120
          }
        }
        "d4e5f6" = {
          node_type       = "CONNECTION"
          connection      = "flow_conductor"
          connector_id    = "flowConnector"
          capability_name = "startUiSubFlow"
          properties      = <<-JSON
            {
              "subFlowId": {
                "value": {
                  "label": "MFA subflow",
                  "value": "@@flow:mfa_subflow@@"
                }
              },
              "subFlowVersionId": {
                "value": -1
              }
            }
          JSON
        }
        "g7h8i9" = {
          node_type       = "CONNECTION"
          connection      = "flow_conductor"
          connector_id    = "flowConnector"
          capability_name = "startUiSubFlow"
          properties      = <<-JSON
            {
              "subFlowId": {
                "value": {
                  "label": "Error leaf",
                  "value": "@@flow:error_leaf@@"
                }
              },
              "subFlowVersionId": {
                "value": -1
              }
            }
          JSON
        }
        "j0eval" = {
          node_type = "EVAL"
          label     = "Evaluator"
        }
      }
      edges = {
        "e1" = {
          source = "a1b2c3"
          target = "d4e5f6"
        }
        "e2" = {
          source                = "a1b2c3"
          target                = "j0eval"
          multi_value_source_id = "found"
        }
        "e3" = {
          source = "j0eval"
          target = "g7h8i9"
        }
      }
    }
  }
  "mfa_subflow" = {
    name    = "MFA subflow"
    tier    = 2
    enabled = false
    input_schema = [
      {
        preferred_data_type = "string"
        property_name       = "username"
        required            = true
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
      nodes = {
        "n2sub1" = {
          node_type       = "CONNECTION"
          connection      = "flow_conductor"
          connector_id    = "flowConnector"
          capability_name = "startUiSubFlow"
          properties      = <<-JSON
            {
              "subFlowId": {
                "value": {
                  "label": "Password reset",
                  "value": "@@flow:password_reset@@"
                }
              },
              "subFlowVersionId": {
                "value": -1
              }
            }
          JSON
        }
        "n2sub2" = {
          node_type       = "CONNECTION"
          connection      = "flow_conductor"
          connector_id    = "flowConnector"
          capability_name = "startUiSubFlow"
          properties      = <<-JSON
            {
              "subFlowId": {
                "value": {
                  "label": "Error leaf",
                  "value": "@@flow:error_leaf@@"
                }
              },
              "subFlowVersionId": {
                "value": -1
              }
            }
          JSON
        }
        "n2var1" = {
          node_type       = "CONNECTION"
          connection      = "http"
          connector_id    = "httpConnector"
          capability_name = "customHtmlMessage"
          properties      = <<-JSON
            {
              "variableId": {
                "value": "@@variable:brand_company@@"
              }
            }
          JSON
        }
      }
      edges = {
        "e2a" = {
          source = "n2sub1"
          target = "n2sub2"
        }
      }
    }
  }
  "password_reset" = {
    name   = "Password reset"
    tier   = 1
    deploy = true
    graph = {
      nodes = {
        "n1sub1" = {
          node_type       = "CONNECTION"
          connection      = "flow_conductor"
          connector_id    = "flowConnector"
          capability_name = "startUiSubFlow"
          properties      = <<-JSON
            {
              "subFlowId": {
                "value": {
                  "label": "Error leaf",
                  "value": "@@flow:error_leaf@@"
                }
              },
              "subFlowVersionId": {
                "value": -1
              }
            }
          JSON
        }
      }
      edges = {}
    }
  }
}

davinci_applications = {
  "web_login" = {
    name            = "Web Login"
    api_key_enabled = true
    oauth = {
      enforce_signed_request_openid = false
      grant_types                   = ["authorizationCode"]
      redirect_uris                 = ["https://app.dev.example.com/cb"]
      scopes                        = ["openid", "profile"]
    }
    flow_policies = {
      "default" = {
        name   = "Login"
        status = "enabled"
        distributions = [
          {
            flow    = "login"
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
    "pingone_sso" = "fake-conn-0001"
  }
  davinci_flows = {
    "error_leaf"  = "fake-flow-0000"
    "login"       = "fake-flow-0003"
    "mfa_subflow" = "fake-flow-0002"
  }
  davinci_variables = {
    "attempts_flow_login" = "fake-var-0002"
    "brand_company"       = "fake-var-0001"
  }
  davinci_applications = {
    "web_login" = "fake-app-0001"
  }
  davinci_flow_policies = {
    "web_login/default" = "fake-app-0001/fake-policy-0001"
  }
}
