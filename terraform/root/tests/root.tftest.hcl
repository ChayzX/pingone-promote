# The reference root stack with the module as a CHILD module, which is how
# the enterprise pipeline runs it. This proves that for_each import blocks
# inside the child module work (Terraform >= 1.15.0).
# Inputs come from golden_full.auto.tfvars (written by internal/tfvars).

mock_provider "pingone" {
  override_during = plan
}

variables {
  pingone_secrets = {
    "pingone_sso_client_secret" = "fake-secret"
    "login_hmac_key"            = "fake-key"
    "dv_api_token"              = "fake-token"
  }
}

# Mock providers can import only overridden resources.
override_resource {
  target = module.davinci.pingone_davinci_connector_instance.this["pingone_sso"]
  values = { id = "conn-sso" }
}

override_resource {
  target = module.davinci.pingone_davinci_flow.t0["error_leaf"]
  values = { id = "flow-error-leaf" }
}

override_resource {
  target = module.davinci.pingone_davinci_flow.t2["mfa_subflow"]
  values = { id = "flow-mfa-subflow" }
}

override_resource {
  target = module.davinci.pingone_davinci_flow.t3["login"]
  values = { id = "flow-login" }
}

override_resource {
  target = module.davinci.pingone_davinci_variable.global["brand_company"]
  values = { id = "var-brand", min = 0, max = 0 }
}

override_resource {
  target = module.davinci.pingone_davinci_variable.flow_scoped["attempts_flow_login"]
  values = { id = "var-attempts-login", min = 0, max = 10 }
}

override_resource {
  target = module.davinci.pingone_davinci_flow_enable.this["login"]
  values = { id = "enable-login" }
}

override_resource {
  target = module.davinci.pingone_davinci_flow_enable.this["mfa_subflow"]
  values = { id = "enable-mfa" }
}

override_resource {
  target = module.davinci.pingone_davinci_flow_enable.this["error_leaf"]
  values = { id = "enable-error" }
}

override_resource {
  target = module.davinci.pingone_davinci_application.this["web_login"]
  values = { id = "app-web-login" }
}

override_resource {
  target = module.davinci.pingone_davinci_application_flow_policy.this["web_login/default"]
  values = { id = "policy-web-login-default" }
}

run "plan_with_child_module_imports" {
  command = plan

  # Only resource kinds the mock provider can import are used here. An
  # imported pingone_davinci_flow, flow-scoped variable or flow policy
  # refreshes with unknown computed values under mock_provider, which
  # Terraform rejects ("returned state contains unknown values"); their
  # import selection is covered by the module tests, and real imports are
  # exercised in the Phase 0 spike (#61, #62).
  variables {
    import_ids = {
      davinci_connections  = { pingone_sso = "fake-conn-0001" }
      davinci_variables    = { brand_company = "fake-var-0001" }
      davinci_applications = { web_login = "fake-app-0001" }
    }
  }

  assert {
    condition     = jsonencode(keys(var.import_ids.davinci_variables)) == jsonencode(["brand_company"])
    error_message = "import_ids were not passed through"
  }
}

run "apply_with_child_module_imports" {
  variables {
    import_ids = {
      davinci_connections  = { pingone_sso = "fake-conn-0001" }
      davinci_variables    = { brand_company = "fake-var-0001" }
      davinci_applications = { web_login = "fake-app-0001" }
    }
  }

  assert {
    condition     = length(output.flow_ids) == 4
    error_message = "every flow should be managed"
  }
  assert {
    condition     = jsonencode(output.required_secret_keys) == jsonencode(["dv_api_token", "login_hmac_key", "pingone_sso_client_secret"])
    error_message = "required secret keys differ"
  }
}
