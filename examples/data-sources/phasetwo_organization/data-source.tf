# Organizations (teams) are referenced, not managed. Create one in the Phase Two console.
data "phasetwo_organization" "team" {
  name = "acme"
}

output "organization_id" {
  value = data.phasetwo_organization.team.id
}

output "my_roles" {
  value = data.phasetwo_organization.team.roles
}
