
resource "pingone_davinci_application_flow_policy" "pingcli__Web-0020-Policy" {
  environment_id         = var.pingone_environment_id
  davinci_application_id = pingone_davinci_application.pingcli__Web.id
  name                   = "Web Policy"
  status                 = "enabled"

  flow_distributions = [
    {
      id      = pingone_davinci_flow.pingcli__Flow-0020-A.id
      version = -1
    }
  ]
}
