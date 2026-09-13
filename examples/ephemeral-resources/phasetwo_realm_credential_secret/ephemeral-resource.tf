# One apply: create the cluster, the realm and its credential, then configure inside the realm
# with the Keycloak provider — without the secret ever reaching terraform.tfstate.

resource "phasetwo_realm" "customer" {
  cluster_id = phasetwo_cluster.main.id
  name       = "production"
}

resource "phasetwo_realm_credential" "terraform" {
  realm_id = phasetwo_realm.customer.id
  name     = "terraform"
}

ephemeral "phasetwo_realm_credential_secret" "terraform" {
  realm_id  = phasetwo_realm.customer.id
  client_id = phasetwo_realm_credential.terraform.client_id
}

provider "keycloak" {
  url           = phasetwo_realm_credential.terraform.server_url
  realm         = phasetwo_realm_credential.terraform.realm
  client_id     = phasetwo_realm_credential.terraform.client_id
  client_secret = ephemeral.phasetwo_realm_credential_secret.terraform.client_secret

  # Required. The Keycloak provider authenticates when Terraform configures it, which happens
  # during plan — before the cluster exists. This defers the login until it first has to act.
  initial_login = false
}

resource "keycloak_openid_client" "app" {
  realm_id    = phasetwo_realm.customer.name
  client_id   = "my-app"
  access_type = "PUBLIC"

  depends_on = [phasetwo_realm_credential.terraform]
}
