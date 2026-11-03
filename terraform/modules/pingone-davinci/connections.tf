resource "pingone_davinci_connector_instance" "this" {
  for_each   = var.davinci_connections
  depends_on = [terraform_data.contract]

  environment_id = var.pingone_environment_id
  name           = each.value.name
  connector = {
    id = each.value.connector
  }
  properties = each.value.properties == null ? null : join("", [
    for i, p in split("@@", each.value.properties) : i % 2 == 0 ? p : lookup(local.refs_connection, p, "")
  ])
}
