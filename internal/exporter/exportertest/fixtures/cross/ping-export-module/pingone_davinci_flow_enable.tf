

resource "pingone_davinci_flow_enable" "pingcli__Flow-0020-A" {
  environment_id = var.pingone_environment_id
  flow_id        = pingone_davinci_flow.pingcli__Flow-0020-A.id
  enabled        = true
}


resource "pingone_davinci_flow_enable" "pingcli__Flow-0020-B" {
  environment_id = var.pingone_environment_id
  flow_id        = pingone_davinci_flow.pingcli__Flow-0020-B.id
  enabled        = true
}


resource "pingone_davinci_flow_enable" "pingcli__Flow-0020-B-0020-Subflow" {
  environment_id = var.pingone_environment_id
  flow_id        = pingone_davinci_flow.pingcli__Flow-0020-B-0020-Subflow.id
  enabled        = true
}


resource "pingone_davinci_flow_enable" "pingcli__Flow-0020-C" {
  environment_id = var.pingone_environment_id
  flow_id        = pingone_davinci_flow.pingcli__Flow-0020-C.id
  enabled        = true
}
