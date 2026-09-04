# Payment methods are referenced, not managed — adding a card is a Stripe browser flow.
data "phasetwo_payment_method" "default" {
  organization_id = data.phasetwo_organization.team.id
  default         = true
}

# Or select a specific card by id.
data "phasetwo_payment_method" "corporate" {
  organization_id = data.phasetwo_organization.team.id
  id              = "pm_1234567890"
}
