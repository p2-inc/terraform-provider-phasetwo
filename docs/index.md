---
page_title: "Phase Two Provider"
subcategory: ""
description: |-
  Manage Phase Two https://phasetwo.io hosted Keycloak clusters and the realms running on them.
  Authenticate with an API secret — an OIDC client-credentials client created for your organization in the Phase Two console. Every argument can also be supplied by environment variable, which is the recommended way to keep credentials out of configuration files.
---

# Phase Two Provider

!> **Experimental — test environments only.** This provider is pre-1.0. Resource and attribute
shapes may still change in backwards-incompatible ways, and what it manages is real, billable
infrastructure: replacing a `phasetwo_cluster` destroys it and every realm on it, and destroying one
keeps billing and holds the name until the end of the billing cycle. Pin an exact version while it
is at `0.x`, and point it at an environment you do not mind breaking.

Manage [Phase Two](https://phasetwo.io) hosted Keycloak as code: dedicated clusters, the realms on
them, custom domains, IP allow and deny lists, environment variables, and custom providers and
themes.

Phase Two runs single-tenant Keycloak, built from the upstream distribution plus our
[open-source extensions](https://phasetwo.io/docs/introduction) — organizations and multi-tenancy, an SSO setup
wizard, webhooks and an event pipeline, magic links, and themes. This provider is the Terraform
front end to the [Management API](https://phasetwo.io/api/management-api-index) behind
[dash.phasetwo.io](https://dash.phasetwo.io); its client is generated from the same OpenAPI
document that generates that reference, so the two cannot drift.

## Example Usage

```terraform
terraform {
  required_providers {
    phasetwo = {
      source  = "p2-inc/phasetwo"
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
```

## Authentication

Credentials come from an **API secret** — an OIDC client-credentials client you create for your
organization in the console, under your team's **API Credentials** tab. Supply it through the
environment so it stays out of `.tf` files and out of state:

```bash
export PHASETWO_CLIENT_ID='...'
export PHASETWO_CLIENT_SECRET='...'
```

The provider performs the client-credentials exchange itself. A secret holds organization roles and
the API enforces them per operation, so a secret granted only view roles gets `403` on writes —
which makes a genuinely read-only credential worth creating. Full walkthrough:
[API keys](https://phasetwo.io/docs/management-api/api-keys).

## Two providers, two jobs

This provider stops at the realm boundary. It creates realms; it does not create the clients,
identity providers and authentication flows inside them — those belong to Keycloak's own admin API.

`phasetwo_realm_credential` is the handoff. It mints a service-account client on your realm and
hands the [Keycloak provider](https://registry.terraform.io/providers/keycloak/keycloak/latest/docs)
everything it needs, so a single `terraform apply` can create a cluster, a realm, a credential, and
then configure inside that realm:

```hcl
resource "phasetwo_realm_credential" "terraform" {
  realm_id = phasetwo_realm.production.id
  name     = "terraform"
}

ephemeral "phasetwo_realm_credential_secret" "terraform" {
  realm_id  = phasetwo_realm.production.id
  client_id = phasetwo_realm_credential.terraform.client_id
}

provider "keycloak" {
  url           = phasetwo_realm_credential.terraform.server_url
  realm         = phasetwo_realm_credential.terraform.realm
  client_id     = phasetwo_realm_credential.terraform.client_id
  client_secret = ephemeral.phasetwo_realm_credential_secret.terraform.client_secret
  initial_login = false
}
```

The secret is read through an [ephemeral
resource](https://developer.hashicorp.com/terraform/language/resources/ephemeral), never written to
state or plan files — which is why `phasetwo_realm_credential` has no `client_secret` attribute.
`initial_login = false` is required: the Keycloak provider otherwise authenticates during `plan`,
against a cluster that does not exist yet. Needs Terraform 1.10 or later. Worked through in full in
the [provider guide](https://phasetwo.io/docs/management-api/terraform).

## Before your first apply

Four behaviours are worth knowing in advance rather than discovering mid-apply:

- **Every argument on `phasetwo_cluster` forces replacement.** Not the tier, not the region.
  Replacing destroys the cluster and every realm on it. Read the plan.
- **Destroy is deferred and the name stays reserved.** A destroyed cluster moves to
  `PENDING_DELETION` and is torn down at the end of the billing cycle, billing until then. A
  create/destroy/recreate loop under one name fails on the second create — use distinct names in
  ephemeral environments.
- **Environment variable and extension changes restart Keycloak**, and the API refuses a second
  such change while one is in flight. The provider serializes them and waits, so an apply touching
  several takes as long as that many restarts.
- **Custom domains take two applies** by design: the first returns the DNS records for you to
  create, the second promotes the domain to primary once they resolve.

## Links

| | |
| --- | --- |
| Provider guide | [phasetwo.io/docs/management-api/terraform](https://phasetwo.io/docs/management-api/terraform) |
| Management API reference | [phasetwo.io/api/management-api-index](https://phasetwo.io/api/management-api-index) |
| Automation recipes | [phasetwo.io/docs/management-api/recipes](https://phasetwo.io/docs/management-api/recipes) |
| Keycloak extension docs | [phasetwo.io/docs](https://phasetwo.io/docs/introduction) |
| Source and issues | [github.com/p2-inc/terraform-provider-phasetwo](https://github.com/p2-inc/terraform-provider-phasetwo) |
| Console | [dash.phasetwo.io](https://dash.phasetwo.io) |

<!-- schema generated by tfplugindocs -->
## Schema

### Optional

- `access_token` (String, Sensitive) A bearer token to use directly, instead of exchanging client credentials for one. Intended for CI that already holds a token; the provider will not refresh it. May also be set with `PHASETWO_ACCESS_TOKEN`.
- `base_url` (String) Full Keycloak auth root, for self-hosted or local development — for example `http://localhost:8080/auth`. This is the auth root, not the API base: the realm and API path are appended to it. Overrides `environment`. May also be set with `PHASETWO_BASE_URL`.
- `client_id` (String) Client ID of an API secret. May also be set with `PHASETWO_CLIENT_ID`.
- `client_secret` (String, Sensitive) Client secret of an API secret. May also be set with `PHASETWO_CLIENT_SECRET`.
- `environment` (String) Which hosted Phase Two console to talk to: `app` (production, the default) or `app-staging`. Ignored when `base_url` is set. May also be set with `PHASETWO_ENVIRONMENT`.
- `realm` (String) Realm the Phase Two console runs in. Defaults to `self`, which is correct for the hosted service. May also be set with `PHASETWO_REALM`.
