data "phasetwo_regions" "available" {}

output "regions" {
  value = data.phasetwo_regions.available.names
}
