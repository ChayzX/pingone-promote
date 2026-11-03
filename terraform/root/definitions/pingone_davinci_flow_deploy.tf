resource "pingone_davinci_flow_deploy" "pingcli__Flow-0020-A" {
  environment_id = var.pingone_environment_id
  flow_id        = pingone_davinci_flow.pingcli__Flow-0020-A.id

  deploy_trigger_values = {
    deployed_version = pingone_davinci_flow.pingcli__Flow-0020-A.current_version
  }
}

resource "pingone_davinci_flow_deploy" "pingcli__Flow-0020-B" {
  environment_id = var.pingone_environment_id
  flow_id        = pingone_davinci_flow.pingcli__Flow-0020-B.id

  deploy_trigger_values = {
    deployed_version = pingone_davinci_flow.pingcli__Flow-0020-B.current_version
  }
}

resource "pingone_davinci_flow_deploy" "pingcli__Flow-0020-B-0020-Subflow" {
  environment_id = var.pingone_environment_id
  flow_id        = pingone_davinci_flow.pingcli__Flow-0020-B-0020-Subflow.id

  deploy_trigger_values = {
    deployed_version = pingone_davinci_flow.pingcli__Flow-0020-B-0020-Subflow.current_version
  }
}

resource "pingone_davinci_flow_deploy" "pingcli__Flow-0020-C" {
  environment_id = var.pingone_environment_id
  flow_id        = pingone_davinci_flow.pingcli__Flow-0020-C.id

  deploy_trigger_values = {
    deployed_version = pingone_davinci_flow.pingcli__Flow-0020-C.current_version
  }
}
