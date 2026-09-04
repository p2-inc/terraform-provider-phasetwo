data "phasetwo_realm" "app" {
  cluster_id = data.phasetwo_cluster.existing.id
  name       = "app"
}

output "realm_state" {
  value = data.phasetwo_realm.app.state
}
