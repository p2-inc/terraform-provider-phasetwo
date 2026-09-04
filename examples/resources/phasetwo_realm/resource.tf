resource "phasetwo_realm" "app" {
  cluster_id   = phasetwo_cluster.main.id
  name         = "app"
  display_name = "Acme Application"
}

# The realm is empty. Configure what is inside it with a Keycloak provider pointed at the
# cluster's host.
output "realm_issuer" {
  value = "${phasetwo_cluster.main.host}/realms/${phasetwo_realm.app.name}"
}
