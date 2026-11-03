# Environment values for one environment. Shared definitions live in definitions/.

pingone_environment_id                      = "11111111-1111-4111-8111-111111111111"
davinci_application_Web_oauth_redirect_uris = ["https://auth.pingone.com/11111111-1111-4111-8111-111111111111/rp/callback/openid_connect"]
davinci_variable_companyName_value          = "Example Corp (dev)"
import_ids = {
  "module.davinci.pingone_davinci_application.pingcli__Web"                         = "a0000000000000000000000000000001"
  "module.davinci.pingone_davinci_application_flow_policy.pingcli__Web-0020-Policy" = "a0000000000000000000000000000001/b0000000000000000000000000000001"
  "module.davinci.pingone_davinci_connector_instance.pingcli__Http"                 = "c0000000000000000000000000000001"
  "module.davinci.pingone_davinci_connector_instance.pingcli__PingOne"              = "c0000000000000000000000000000002"
  "module.davinci.pingone_davinci_flow.pingcli__Flow-0020-A"                        = "f0000000000000000000000000000001"
  "module.davinci.pingone_davinci_flow.pingcli__Flow-0020-B"                        = "f0000000000000000000000000000002"
  "module.davinci.pingone_davinci_flow.pingcli__Flow-0020-C"                        = "f0000000000000000000000000000004"
  "module.davinci.pingone_davinci_flow_deploy.pingcli__Flow-0020-A"                 = "f0000000000000000000000000000001"
  "module.davinci.pingone_davinci_flow_deploy.pingcli__Flow-0020-B"                 = "f0000000000000000000000000000002"
  "module.davinci.pingone_davinci_flow_deploy.pingcli__Flow-0020-C"                 = "f0000000000000000000000000000004"
  "module.davinci.pingone_davinci_flow_enable.pingcli__Flow-0020-A"                 = "f0000000000000000000000000000001"
  "module.davinci.pingone_davinci_flow_enable.pingcli__Flow-0020-B"                 = "f0000000000000000000000000000002"
  "module.davinci.pingone_davinci_flow_enable.pingcli__Flow-0020-C"                 = "f0000000000000000000000000000004"
  "module.davinci.pingone_davinci_variable.pingcli__apiKey_company"                 = "d0000000000000000000000000000001"
  "module.davinci.pingone_davinci_variable.pingcli__attempts_flowInstance"          = "d0000000000000000000000000000002"
  "module.davinci.pingone_davinci_variable.pingcli__companyName_company"            = "d0000000000000000000000000000003"
}
