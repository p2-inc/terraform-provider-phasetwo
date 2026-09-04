data "phasetwo_organization" "team" {
  name = "acme"
}

# Payment methods are added in the Phase Two console, not by Terraform.
data "phasetwo_payment_method" "default" {
  organization_id = data.phasetwo_organization.team.id
  default         = true
}

resource "phasetwo_cluster" "main" {
  name              = "acme-prod"
  region            = "US_EAST_1"
  tier              = "premium"
  billing_period    = "annual"
  organization_id   = data.phasetwo_organization.team.id
  payment_method_id = data.phasetwo_payment_method.default.id

  timeouts {
    create = "60m"
  }
}

output "cluster_host" {
  value = phasetwo_cluster.main.host
}
