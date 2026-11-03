# Reference root with mock_provider, run once per environment the way the
# pipeline runs a plan, with that environment's overlay:
#
#   terraform test -var-file=qa.tfvars   (and dev, stage, prod)
#
# The overlay supplies the environment ID and environment values; the
# pipeline supplies secrets as TF_VAR_<name> (here: test values).

mock_provider "pingone" {}

variables {
  PingOne_DaVinci_connection_PingOne_clientSecret = "test-only"
  davinci_variable_apiKey_value                   = "test-only"
}

run "plan_environment" {
  command = plan

  assert {
    condition     = can(regex("^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$", var.pingone_environment_id))
    error_message = "the overlay must set pingone_environment_id"
  }

  assert {
    condition     = alltrue([for u in var.davinci_application_Web_oauth_redirect_uris : startswith(u, "https://auth.pingone.com/${var.pingone_environment_id}/")])
    error_message = "redirect URIs must belong to this environment (never another environment's URL)"
  }

}

# Mock providers import only overridden resources (the dev overlay adopts
# every managed resource through import_ids).

override_resource {
  target = module.davinci.pingone_davinci_application.pingcli__Web
  values = { id = "mock-imported-id" }
}

override_resource {
  target = module.davinci.pingone_davinci_application_flow_policy.pingcli__Web-0020-Policy
  values = { id = "mock-imported-id" }
}

override_resource {
  target = module.davinci.pingone_davinci_connector_instance.pingcli__Http
  values = { id = "mock-imported-id" }
}

override_resource {
  target = module.davinci.pingone_davinci_connector_instance.pingcli__PingOne
  values = { id = "mock-imported-id" }
}

override_resource {
  target = module.davinci.pingone_davinci_flow.pingcli__Flow-0020-A
  values = { id = "mock-imported-id" }
}

override_resource {
  target = module.davinci.pingone_davinci_flow.pingcli__Flow-0020-B
  values = { id = "mock-imported-id" }
}

override_resource {
  target = module.davinci.pingone_davinci_flow.pingcli__Flow-0020-C
  values = { id = "mock-imported-id" }
}

override_resource {
  target = module.davinci.pingone_davinci_flow_deploy.pingcli__Flow-0020-A
  values = { id = "mock-imported-id" }
}

override_resource {
  target = module.davinci.pingone_davinci_flow_deploy.pingcli__Flow-0020-B
  values = { id = "mock-imported-id" }
}

override_resource {
  target = module.davinci.pingone_davinci_flow_deploy.pingcli__Flow-0020-C
  values = { id = "mock-imported-id" }
}

override_resource {
  target = module.davinci.pingone_davinci_flow_enable.pingcli__Flow-0020-A
  values = { id = "mock-imported-id" }
}

override_resource {
  target = module.davinci.pingone_davinci_flow_enable.pingcli__Flow-0020-B
  values = { id = "mock-imported-id" }
}

override_resource {
  target = module.davinci.pingone_davinci_flow_enable.pingcli__Flow-0020-C
  values = { id = "mock-imported-id" }
}

override_resource {
  target = module.davinci.pingone_davinci_variable.pingcli__apiKey_company
  values = { id = "mock-imported-id" }
}

override_resource {
  target = module.davinci.pingone_davinci_variable.pingcli__attempts_flowInstance
  values = { id = "mock-imported-id" }
}

override_resource {
  target = module.davinci.pingone_davinci_variable.pingcli__companyName_company
  values = { id = "mock-imported-id" }
}

override_resource {
  target = module.davinci.pingone_davinci_flow.pingcli__Flow-0020-B-0020-Subflow
  values = { id = "mock-imported-id" }
}

override_resource {
  target = module.davinci.pingone_davinci_flow_deploy.pingcli__Flow-0020-B-0020-Subflow
  values = { id = "mock-imported-id" }
}

override_resource {
  target = module.davinci.pingone_davinci_flow_enable.pingcli__Flow-0020-B-0020-Subflow
  values = { id = "mock-imported-id" }
}
