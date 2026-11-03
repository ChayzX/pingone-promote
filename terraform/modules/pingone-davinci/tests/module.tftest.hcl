# Module tests with a mocked provider. Inputs come from
# golden_full.auto.tfvars, which internal/tfvars writes from its Go fixture
# (go test ./internal/tfvars -update) and checks byte for byte, so Go and
# Terraform exercise the same schema v1 document. No network or credentials.

mock_provider "pingone" {}

variables {
  # Fake secrets that need JSON escaping: quotes, backslashes, and an escaped
  # trailing quote (the case trim() would get wrong).
  pingone_secrets = {
    "PingOne_DaVinci_connection_PingOne_clientSecret" = "fake\"secret\\with\\\\escapes"
    "login_hmac_key"                                  = "fake-key-ending-in-quote\""
    "davinci_variable_apiToken_value"                 = "fake-api-token"
  }
}

# Known IDs so assertions can check that each placeholder resolved to the
# right resource across tiers. Mock providers can import only overridden
# resources, so every import target in the golden is overridden too.
override_resource {
  target = pingone_davinci_connector_instance.this["pingone_sso"]
  values = { id = "conn-sso" }
}
override_resource {
  target = pingone_davinci_connector_instance.this["flow_conductor"]
  values = { id = "conn-flow-conductor" }
}
override_resource {
  target = pingone_davinci_connector_instance.this["http"]
  values = { id = "conn-http" }
}
override_resource {
  target = pingone_davinci_flow.t0["error_leaf"]
  values = { id = "flow-t0-error-leaf" }
}
override_resource {
  target = pingone_davinci_flow.t1["password_reset"]
  values = { id = "flow-t1-password-reset" }
}
override_resource {
  target = pingone_davinci_flow.t2["mfa_subflow"]
  values = { id = "flow-t2-mfa-subflow" }
}
override_resource {
  target = pingone_davinci_flow.t3["login"]
  values = { id = "flow-t3-login" }
}
override_resource {
  target = pingone_davinci_variable.global["brand_company"]
  values = { id = "var-brand-company" }
}
override_resource {
  target = pingone_davinci_variable.global["attempts_company"]
  values = { id = "var-attempts-company" }
}
override_resource {
  target = pingone_davinci_variable.flow_scoped["attempts_flow_login"]
  values = { id = "var-attempts-flow-login" }
}
override_resource {
  target = pingone_davinci_flow_enable.this["login"]
  values = { id = "enable-login" }
}
override_resource {
  target = pingone_davinci_flow_enable.this["mfa_subflow"]
  values = { id = "enable-mfa" }
}
override_resource {
  target = pingone_davinci_flow_enable.this["error_leaf"]
  values = { id = "enable-error" }
}
override_resource {
  target = pingone_davinci_application.this["web_login"]
  values = { id = "app-web-login" }
}
override_resource {
  target = pingone_davinci_application_flow_policy.this["web_login/default"]
  values = { id = "policy-web-login-default" }
}

run "resolves_references" {
  variables {
    import_ids = {}
  }

  # Connector properties: secret with quotes and backslashes, env:id, param.
  assert {
    condition     = jsondecode(nonsensitive(pingone_davinci_connector_instance.this["pingone_sso"].properties)).clientSecret.value == nonsensitive(var.pingone_secrets["PingOne_DaVinci_connection_PingOne_clientSecret"])
    error_message = "secret with quote and backslash was not JSON-escaped correctly"
  }
  assert {
    condition     = jsondecode(nonsensitive(pingone_davinci_connector_instance.this["pingone_sso"].properties)).envId.value == var.pingone_environment_id
    error_message = "@@env:id@@ did not resolve"
  }
  assert {
    condition     = jsondecode(nonsensitive(pingone_davinci_connector_instance.this["pingone_sso"].properties)).baseUrl.value == "https://idp.dev.example.com/as"
    error_message = "@@param:idp_base_url@@ did not resolve"
  }
  assert {
    condition     = pingone_davinci_connector_instance.this["pingone_sso"].connector.id == "pingOneSSOConnector"
    error_message = "connector key did not map to connector.id"
  }

  # @@@@ is a literal @@.
  assert {
    condition     = jsondecode(nonsensitive(pingone_davinci_connector_instance.this["functions"].properties)).note.value == "literal @@ stays"
    error_message = "@@@@ escape did not produce a literal @@"
  }

  # Node connection keys become connection IDs.
  assert {
    condition     = pingone_davinci_flow.t3["login"].graph_data.elements.nodes["a1b2c3"].data.connection_id == "conn-sso"
    error_message = "node connection key did not resolve to the connector instance ID"
  }

  # Secret ending in an escaped quote inside a flow node.
  assert {
    condition     = jsondecode(nonsensitive(pingone_davinci_flow.t3["login"].graph_data.elements.nodes["a1b2c3"].data.properties)).hmacKey.value == nonsensitive(var.pingone_secrets["login_hmac_key"])
    error_message = "secret ending in a quote was mangled"
  }
  assert {
    condition     = jsondecode(nonsensitive(pingone_davinci_flow.t3["login"].graph_data.elements.nodes["a1b2c3"].data.properties)).username.value == "$${global.parameters.username}"
    error_message = "escaped template sequence did not reach the provider literally"
  }

  # Subflow references across tiers, including skipping a tier.
  assert {
    condition     = jsondecode(nonsensitive(pingone_davinci_flow.t3["login"].graph_data.elements.nodes["d4e5f6"].data.properties)).subFlowId.value.value == "flow-t2-mfa-subflow"
    error_message = "t3 -> t2 subflow reference did not resolve"
  }
  assert {
    condition     = jsondecode(nonsensitive(pingone_davinci_flow.t3["login"].graph_data.elements.nodes["g7h8i9"].data.properties)).subFlowId.value.value == "flow-t0-error-leaf"
    error_message = "t3 -> t0 subflow reference did not resolve"
  }
  assert {
    condition     = jsondecode(nonsensitive(pingone_davinci_flow.t2["mfa_subflow"].graph_data.elements.nodes["n2sub1"].data.properties)).subFlowId.value.value == "flow-t1-password-reset"
    error_message = "t2 -> t1 subflow reference did not resolve"
  }
  assert {
    condition     = jsondecode(nonsensitive(pingone_davinci_flow.t1["password_reset"].graph_data.elements.nodes["n1sub1"].data.properties)).subFlowId.value.value == "flow-t0-error-leaf"
    error_message = "t1 -> t0 subflow reference did not resolve"
  }
  assert {
    condition     = jsondecode(nonsensitive(pingone_davinci_flow.t2["mfa_subflow"].graph_data.elements.nodes["n2var1"].data.properties)).variableId.value == "var-brand-company"
    error_message = "@@variable:key@@ did not resolve"
  }
  assert {
    condition     = jsondecode(nonsensitive(pingone_davinci_flow.t0["error_leaf"].graph_data.elements.nodes["n0err1"].data.properties)).message.value == "Hi $${global.variables.brand}, 100%%{done}"
    error_message = "template sequences in JSON did not round-trip"
  }

  # Variables: secret value, json_object params, plain string escaping.
  assert {
    condition     = nonsensitive(pingone_davinci_variable.global["apiToken_company"].value.secret_string) == "fake-api-token"
    error_message = "secret variable did not take its value from pingone_secrets"
  }
  assert {
    condition     = jsondecode(pingone_davinci_variable.global["theme_company"].value.json_object).logo == "https://idp.dev.example.com/logo.png"
    error_message = "json_object param reference did not resolve"
  }
  assert {
    condition     = pingone_davinci_variable.global["greeting_flowInstance"].value.string == "Hello $${user.name}"
    error_message = "plain string with a template sequence changed"
  }

  # Applications and policies.
  assert {
    condition     = one([for d in pingone_davinci_application_flow_policy.this["web_login/default"].flow_distributions : d.id]) == "flow-t3-login"
    error_message = "flow policy distribution did not resolve the flow key"
  }
  assert {
    condition     = pingone_davinci_application.this["web_login"].api_key.enabled == true
    error_message = "api_key_enabled did not map to api_key.enabled"
  }

  # Deploy and enable.
  assert {
    condition     = length(pingone_davinci_flow_deploy.this) == 4 && pingone_davinci_flow_deploy.this["login"].deploy_trigger_values.definition == output.flow_deploy_digests["login"]
    error_message = "every flow should deploy with its definition digest"
  }
  assert {
    condition     = pingone_davinci_flow_enable.this["mfa_subflow"].enabled == false && pingone_davinci_flow_enable.this["login"].enabled == true
    error_message = "flow enable state did not follow the tfvars"
  }

  assert {
    condition     = jsonencode(output.required_secret_keys) == jsonencode(["PingOne_DaVinci_connection_PingOne_clientSecret", "davinci_variable_apiToken_value", "login_hmac_key"])
    error_message = "required secret key names differ from the Go validator"
  }
}

# A company variable and a flow variable with the same name stay distinct.
run "company_and_flow_variable_share_name" {
  variables {
    import_ids = {}
  }

  assert {
    condition = (
      pingone_davinci_variable.global["attempts_company"].name == "attempts" &&
      pingone_davinci_variable.global["attempts_company"].context == "company" &&
      pingone_davinci_variable.global["attempts_company"].flow == null &&
      pingone_davinci_variable.flow_scoped["attempts_flow_login"].name == "attempts" &&
      pingone_davinci_variable.flow_scoped["attempts_flow_login"].context == "flow" &&
      pingone_davinci_variable.flow_scoped["attempts_flow_login"].flow.id == "flow-t3-login"
    )
    error_message = "same-name company and flow variables were not kept as separate resources"
  }
  assert {
    condition     = output.variable_ids["attempts_company"] == "var-attempts-company" && output.variable_ids["attempts_flow_login"] == "var-attempts-flow-login" && length(output.variable_ids) == 6
    error_message = "variable IDs collided"
  }
}

run "tier_violation_fails" {
  command = plan

  variables {
    davinci_variables    = {}
    davinci_applications = {}
    import_ids           = {}
    davinci_flows = {
      parent = {
        name = "Parent"
        tier = 1
        graph = {
          nodes = {
            n1 = {
              node_type  = "CONNECTION"
              connection = "flow_conductor"
              properties = "{\"subFlowId\": {\"value\": {\"value\": \"@@flow:sibling@@\"}}}"
            }
          }
          edges = {}
        }
      }
      sibling = {
        name  = "Sibling"
        tier  = 1
        graph = { nodes = {}, edges = {} }
      }
    }
  }

  expect_failures = [terraform_data.contract_tiers]
}

run "missing_secret_fails" {
  command = plan

  variables {
    pingone_secrets = {
      "login_hmac_key"                  = "fake"
      "davinci_variable_apiToken_value" = "fake"
    }
  }

  expect_failures = [terraform_data.contract_secrets]
}

run "unknown_reference_fails" {
  command = plan

  variables {
    davinci_variables    = {}
    davinci_applications = {}
    import_ids           = {}
    davinci_flows = {
      lonely = {
        name = "Lonely"
        tier = 0
        graph = {
          nodes = {
            n1 = {
              node_type  = "CONNECTION"
              connection = "no_such_connection"
              properties = "{\"x\": {\"value\": \"@@connection:also_missing@@\"}}"
            }
          }
          edges = {}
        }
      }
    }
  }

  expect_failures = [terraform_data.contract_references]
}

run "duplicate_variable_identity_fails" {
  command = plan

  variables {
    import_ids = {}
    davinci_variables = {
      brand_company = { name = "brand", context = "company", data_type = "string", mutable = true }
      brand_copy    = { name = "brand", context = "company", data_type = "string", mutable = true }
    }
  }

  expect_failures = [terraform_data.contract_variables]
}

run "import_without_definition_fails" {
  command = plan

  variables {
    import_ids = {
      davinci_flows = { no_such_flow = "fake-flow-9999" }
    }
  }

  expect_failures = [terraform_data.contract_imports]
}

# The root stack passes every declared secret variable, so an unset one
# arrives as null and must count as missing.
run "null_secret_counts_as_missing" {
  command = plan

  variables {
    pingone_secrets = {
      "PingOne_DaVinci_connection_PingOne_clientSecret" = "fake"
      "davinci_variable_apiToken_value"                 = "fake"
      "login_hmac_key"                                  = null
    }
  }

  expect_failures = [terraform_data.contract_secrets]
}
