# End-to-end: promoted.auto.tfvars is produced by internal/promote's
# TestEndToEnd (fixture inventory -> translate -> merge into the target ->
# write). Run with: terraform test -test-directory=tests/e2e

mock_provider "pingone" {}

variables {
  # One value per secret the promoted file requires (see
  # internal/promote/testdata/e2e_secrets.tf).
  pingone_secrets = {
    "PingOne_DaVinci_connection_client"        = "fake\"target\\secret"
    "PingOne_DaVinci_flow_Login_n3sso_hmacKey" = "fake-hmac"
  }
}

override_resource {
  target = pingone_davinci_connector_instance.this["PingOne"]
  values = { id = "conn-sso" }
}
override_resource {
  target = pingone_davinci_connector_instance.this["Http"]
  values = { id = "conn-http" }
}
override_resource {
  target = pingone_davinci_flow.t1["PingOne-20SSO-20Forgot-20Password-20subflow"]
  values = { id = "flow-forgot" }
}
override_resource {
  target = pingone_davinci_flow.t2["MFA-20subflow"]
  values = { id = "flow-mfa" }
}
override_resource {
  target = pingone_davinci_flow.t3["Login"]
  values = { id = "flow-login" }
}

run "promoted_document_resolves_in_target" {
  assert {
    condition     = pingone_davinci_flow.t3["Login"].graph_data.elements.nodes["n3sso"].data.connection_id == "conn-sso"
    error_message = "node connection key did not resolve"
  }
  assert {
    condition     = jsondecode(nonsensitive(pingone_davinci_flow.t3["Login"].graph_data.elements.nodes["n3sso"].data.properties)).helper.value.connectionId == "conn-http"
    error_message = "connectionId inside properties did not resolve"
  }
  assert {
    condition     = jsondecode(nonsensitive(pingone_davinci_flow.t3["Login"].graph_data.elements.nodes["n3sso"].data.properties)).issuer.value == "https://auth.pingone.example/${var.pingone_environment_id}/as"
    error_message = "source environment ID was not replaced by the target's"
  }
  assert {
    condition     = jsondecode(nonsensitive(pingone_davinci_flow.t3["Login"].graph_data.elements.nodes["n3sso"].data.properties)).note.value == "literal @@ in text"
    error_message = "@@ escape did not round-trip"
  }
  assert {
    condition     = jsondecode(nonsensitive(pingone_davinci_flow.t3["Login"].graph_data.elements.nodes["n3sso"].data.properties)).hmacKey.value == "fake-hmac"
    error_message = "node secret did not resolve"
  }
  assert {
    condition     = jsondecode(nonsensitive(pingone_davinci_flow.t3["Login"].graph_data.elements.nodes["n3sub"].data.properties)).subFlowId.value.value == "flow-mfa"
    error_message = "Login -> MFA subflow did not resolve"
  }
  assert {
    condition     = jsondecode(nonsensitive(pingone_davinci_flow.t2["MFA-20subflow"].graph_data.elements.nodes["n2sub"].data.properties)).subFlowId.value.value == "flow-forgot"
    error_message = "MFA -> Forgot password subflow did not resolve"
  }
  assert {
    condition     = jsondecode(nonsensitive(pingone_davinci_flow.t2["MFA-20subflow"].graph_data.elements.nodes["n2form"].data.properties)).form.value == "00000000-0000-4000-8000-00000000e001"
    error_message = "form ID parameter did not take the target's value"
  }
  assert {
    condition     = jsondecode(nonsensitive(pingone_davinci_connector_instance.this["PingOne"].properties)).clientSecret.value == nonsensitive(var.pingone_secrets["PingOne_DaVinci_connection_client"])
    error_message = "connection secret did not resolve with JSON escaping"
  }
  assert {
    condition     = jsondecode(nonsensitive(pingone_davinci_connector_instance.this["PingOne"].properties)).clientId.value == "fake-target-client-id"
    error_message = "target's own connection definition was not kept"
  }
  assert {
    condition     = one([for d in pingone_davinci_application_flow_policy.this["Web-20Login/Login-20policy"].flow_distributions : d.id]) == "flow-login"
    error_message = "policy distribution did not resolve"
  }
  assert {
    condition     = jsonencode(pingone_davinci_application.this["Web-20Login"].oauth.redirect_uris) == jsonencode(["https://app.test.example.com/cb"])
    error_message = "target redirect URIs were not kept"
  }
  assert {
    condition     = pingone_davinci_variable.flow_scoped["attempts_flow_Login"].flow.id == "flow-login"
    error_message = "flow-scoped variable did not follow its flow"
  }
}
