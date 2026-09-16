resource "phasetwo_realm" "customer" {
  cluster_id = phasetwo_cluster.main.id
  name       = "production"
}

resource "phasetwo_realm_credential" "terraform" {
  realm_id    = phasetwo_realm.customer.id
  name        = "terraform"
  description = "terraform, ${terraform.workspace}"
}

# Narrow the roles where the holder does less than full administration.
resource "phasetwo_realm_credential" "audit" {
  realm_id    = phasetwo_realm.customer.id
  name        = "audit"
  description = "read-only reporting job"
  roles       = ["view-users", "view-realm", "view-events"]
}
