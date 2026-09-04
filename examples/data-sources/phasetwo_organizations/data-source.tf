data "phasetwo_organizations" "all" {}

output "organization_names" {
  value = [for o in data.phasetwo_organizations.all.organizations : o.name]
}
