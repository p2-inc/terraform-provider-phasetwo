data "phasetwo_cluster" "existing" {
  name = "acme-prod"
}

output "cluster_host" {
  value = data.phasetwo_cluster.existing.host
}
