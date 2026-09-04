# Each of these restarts the cluster's Keycloak deployment. The provider serializes them per
# cluster, so an apply with several will take as long as that many restarts.
resource "phasetwo_cluster_environment_variable" "log_level" {
  cluster_id = phasetwo_cluster.main.id
  name       = "KC_SPI_MY_PROVIDER_LOG_LEVEL"
  value      = "DEBUG"
}

resource "phasetwo_cluster_environment_variable" "api_key" {
  cluster_id = phasetwo_cluster.main.id
  name       = "MY_INTEGRATION_API_KEY"
  value      = var.integration_api_key
  type       = "SECRET"
}
