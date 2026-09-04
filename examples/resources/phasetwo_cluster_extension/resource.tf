resource "phasetwo_cluster_extension" "custom_authenticator" {
  cluster_id    = phasetwo_cluster.main.id
  name          = "acme-authenticator"
  resource_type = "EXTENSION"
}

resource "phasetwo_cluster_extension" "theme" {
  cluster_id    = phasetwo_cluster.main.id
  name          = "acme-theme"
  resource_type = "THEME"
}
