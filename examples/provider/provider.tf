terraform {
  required_providers {
    phasetwo = {
      source  = "p2-inc/terraform-provider-phasetwo"
      version = "~> 0.1"
    }
  }
}

# Credentials come from PHASETWO_CLIENT_ID and PHASETWO_CLIENT_SECRET.
# Create an API secret for your organization in the Phase Two console; the client secret is
# shown only once.
provider "phasetwo" {
  environment = "app" # or "app-staging"
}
