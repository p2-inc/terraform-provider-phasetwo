# Uploading a version reconciles the cluster, which restarts Keycloak.
resource "phasetwo_cluster_extension_version" "authenticator_kc26" {
  cluster_id             = phasetwo_cluster.main.id
  extension_id           = phasetwo_cluster_extension.custom_authenticator.id
  source                 = "${path.module}/dist/acme-authenticator-1.4.0.jar"
  keycloak_major_version = 26
  label                  = "1.4.0"
}

# PASSWORD_BLACKLIST is version-independent, so keycloak_major_version must be omitted.
resource "phasetwo_cluster_extension" "blacklist" {
  cluster_id    = phasetwo_cluster.main.id
  name          = "common-passwords"
  resource_type = "PASSWORD_BLACKLIST"
}

resource "phasetwo_cluster_extension_version" "blacklist" {
  cluster_id   = phasetwo_cluster.main.id
  extension_id = phasetwo_cluster_extension.blacklist.id
  source       = "${path.module}/files/common-passwords.txt"
}
