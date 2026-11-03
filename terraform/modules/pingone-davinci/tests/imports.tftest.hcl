# for_each import blocks. This file has its own (empty) state, so the import
# blocks really run instead of being skipped for addresses already in state.
#
# Mock providers can import only overridden resources, and an imported
# pingone_davinci_flow, flow-scoped variable or flow policy refreshes with
# unknown computed values under mock_provider, which Terraform rejects. Real
# imports therefore cover connections, global variables and applications;
# flow routing to the right tier resource is asserted on the for_each maps.

mock_provider "pingone" {}

variables {
  pingone_secrets = {
    "pingone_sso_client_secret" = "fake"
    "login_hmac_key"            = "fake"
    "dv_api_token"              = "fake"
  }
}

override_resource {
  target = pingone_davinci_connector_instance.this["pingone_sso"]
  values = { id = "conn-sso" }
}
override_resource {
  target = pingone_davinci_variable.global["brand_company"]
  values = { id = "var-brand-company", min = 0, max = 0 }
}
override_resource {
  target = pingone_davinci_application.this["web_login"]
  values = { id = "app-web-login" }
}

# Flow import routing: the golden's import_ids put flows in t0, t2 and t3.
# The plan is targeted at the contract so the flow import blocks are planned
# but not executed (mock providers cannot import flows, see above).
run "golden_flow_imports_route_to_their_tier" {
  command = plan

  plan_options {
    target = [terraform_data.contract]
  }

  assert {
    condition = jsonencode({ for t, m in local.import_flows_by_tier : t => keys(m) }) == jsonencode({
      "0" = ["error_leaf"]
      "1" = []
      "2" = ["mfa_subflow"]
      "3" = ["login"]
    })
    error_message = "flow import IDs were not routed to their tier resources"
  }
  assert {
    condition     = length(local.import_problems) == 0
    error_message = "every golden flow import should match a defined flow"
  }
}

run "imports_connection_variable_and_application" {
  variables {
    import_ids = {
      davinci_connections  = { pingone_sso = "fake-conn-0001" }
      davinci_variables    = { brand_company = "fake-var-0001" }
      davinci_applications = { web_login = "fake-app-0001" }
    }
  }

  assert {
    condition     = output.connection_ids["pingone_sso"] == "conn-sso" && output.variable_ids["brand_company"] == "var-brand-company" && output.application_ids["web_login"] == "app-web-login"
    error_message = "imported resources did not take their overridden IDs"
  }
}
