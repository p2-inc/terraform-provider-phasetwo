# Terraform Provider for Phase Two

<!-- phasetwo:banner:start - managed by p2-inc/shared-github-actions, edit there -->
[![build](https://img.shields.io/github/actions/workflow/status/p2-inc/terraform-provider-phasetwo/test.yml?branch=main&label=build)](https://github.com/p2-inc/terraform-provider-phasetwo/actions/workflows/test.yml) [![license](https://img.shields.io/badge/license-MPL--2.0-blue)](LICENSE) [![docs](https://img.shields.io/badge/docs-phasetwo.io-blue)](https://phasetwo.io/docs/management-api/terraform/)

📚 [Documentation](https://phasetwo.io/docs/management-api/terraform/) • 🚀 [Hosted Keycloak](https://dash.phasetwo.io/?utm_source=github&utm_medium=readme&utm_campaign=terraform-provider-phasetwo) • 🤖 [Keycloak Skills](https://github.com/p2-inc/keycloak-skills) • 💬 [Support](mailto:support@phasetwo.io)

> [!TIP]
> **Run this in our cloud, no setup needed.** Phase Two hosts Keycloak with all of our extensions built in. [Try it free with your own cluster →](https://dash.phasetwo.io/?utm_source=github&utm_medium=readme&utm_campaign=terraform-provider-phasetwo)
>
> **Using a coding agent?** Add the Phase Two Keycloak skills ([launch post](https://phasetwo.io/blog/keycloak-skills-mcp-launch)) to Claude Code, Cursor or Codex so your agent can set up passwordless login, SSO, organizations and clusters for you:
>
> ```sh
> # Install the skills (connects to our hosted MCP server)
> claude plugin marketplace add p2-inc/keycloak-skills
> claude plugin install phasetwo@keycloak-skills
> # Or connect to the MCP server directly
> claude mcp add --transport http keycloak https://mcp.phasetwo.io/mcp
> ```
<!-- phasetwo:banner:end -->

Manage [Phase Two](https://phasetwo.io) hosted Keycloak clusters and the realms running on them.

> [!CAUTION]
> **Experimental — test environments only.**
> This provider is pre-1.0. Resource and attribute shapes may still change in
> backwards-incompatible ways, and what it manages is real, billable infrastructure: replacing a
> `phasetwo_cluster` destroys it and every realm on it, and destroying one keeps billing and holds
> the name until the end of the billing cycle. Pin an exact version while it is at `0.x`, and point
> it at an environment you do not mind breaking.

The provider covers cluster lifecycle and the things configured on a cluster: realms, custom
domains, IP allow/deny rules, environment variables and extensions. Organizations (teams) and
payment methods are **referenced, not managed** — both involve flows that only make sense in a
browser, so you create them in the Phase Two console and look them up here.

## Usage

```hcl
terraform {
  required_providers {
    phasetwo = {
      source  = "p2-inc/phasetwo"
      version = "~> 0.1"
    }
  }
}

provider "phasetwo" {
  # Credentials come from PHASETWO_CLIENT_ID / PHASETWO_CLIENT_SECRET.
  environment = "app"
}

data "phasetwo_organization" "team" {
  name = "acme"
}

data "phasetwo_payment_method" "default" {
  organization_id = data.phasetwo_organization.team.id
  default         = true
}

resource "phasetwo_cluster" "main" {
  name              = "acme-prod"
  region            = "US_EAST_1"
  tier              = "premium"
  organization_id   = data.phasetwo_organization.team.id
  payment_method_id = data.phasetwo_payment_method.default.id
}

resource "phasetwo_realm" "app" {
  cluster_id   = phasetwo_cluster.main.id
  name         = "app"
  display_name = "Acme Application"
}
```

The realm this creates is empty. To configure what is inside it — clients, identity providers,
authentication flows — point a Keycloak provider at `phasetwo_cluster.main.host`.

## Authentication

Create an **API secret** for your organization in the Phase Two console. That is an OIDC
client-credentials client; the provider exchanges its ID and secret for an access token. The
client secret is shown only once.

| Argument | Environment variable | Notes |
|---|---|---|
| `client_id` | `PHASETWO_CLIENT_ID` | |
| `client_secret` | `PHASETWO_CLIENT_SECRET` | Sensitive |
| `access_token` | `PHASETWO_ACCESS_TOKEN` | Instead of the above; not refreshed |
| `environment` | `PHASETWO_ENVIRONMENT` | `app` (default) or `app-staging` |
| `base_url` | `PHASETWO_BASE_URL` | Self-hosted or local; overrides `environment` |
| `realm` | `PHASETWO_REALM` | Defaults to `self` |

## Things worth knowing before you apply

**Destroying a cluster does not delete it immediately.** Unless it never completed billing setup,
`terraform destroy` moves the cluster to `PENDING_DELETION` and Phase Two tears it down at the end
of the billing cycle. It keeps billing until then, and its name stays reserved — so recreating it
under the same name will fail. The provider emits a warning saying so.

**Nothing about a cluster can be changed in place.** Every argument on `phasetwo_cluster` forces
replacement, which destroys the cluster and every realm on it. Terraform will tell you, but it is
worth reading the plan carefully.

**`payment_method_id` is required**, even though the API treats it as optional. Omitting it makes
the API return a Stripe Checkout link to open in a browser, which Terraform cannot complete.

**Environment variable and extension changes restart Keycloak.** The API rejects a second such
change while one is in flight. The provider serializes them per cluster and waits for each restart,
so a config with several works — but an apply that touches many will take as long as that many
restarts.

**A `SECRET` environment variable's value cannot be read back.** The API returns a mask, so the
provider keeps the last applied value in state and cannot detect a change made outside Terraform.

**Custom domains need DNS records you create yourself.** `phasetwo_cluster_domain` returns
`domain_records`; create them at your DNS provider, and only then point
`phasetwo_cluster_primary_host` at the domain. `wait_for_certificate` is off by default because
turning it on in the same apply that creates those records would deadlock.

**Tier limits are enforced by the API** and surface as 409s:

| Tier | Realms | Themes | Extensions | Domains | IP rules |
|---|---|---|---|---|---|
| `starter` | 5 | 1 | 0 | 2 | 0 |
| `premium` | 20 | 1 | 1 | 5 | 2 |
| `enterprise` | 100 | ∞ | ∞ | 15 | ∞ |

## Development

Requires Go (see `go.mod`) and Terraform 1.8+.

```sh
make build      # build the provider
make test       # unit tests
make docs       # regenerate docs/ from schema descriptions and examples/
make generate   # regenerate the API client from api/openapi.yaml
```

To try it locally, add a dev override to `~/.terraformrc`:

```hcl
provider_installation {
  dev_overrides {
    "p2-inc/phasetwo" = "/path/to/terraform-provider-phasetwo"
  }
  direct {}
}
```

### The API client is generated

`internal/client` is generated by `oapi-codegen` from `api/openapi.yaml`, which is itself a build
artifact of `phasetwo-keycloak` — it is not published anywhere, so it is vendored here and pinned
to a commit. See [`api/SPEC.md`](api/SPEC.md) for how to refresh it. `internal/api` is the hand-written layer on
top: authentication, typed errors, and the restart serialization described above.

### Acceptance tests

`make testacc` runs against a real Phase Two environment and **creates real, billable
infrastructure**. It needs credentials and a test organization with a saved payment method:

```sh
export TF_ACC=1
export PHASETWO_ENVIRONMENT=app-staging
export PHASETWO_CLIENT_ID=... PHASETWO_CLIENT_SECRET=...
export PHASETWO_TEST_ORG_ID=... PHASETWO_TEST_PAYMENT_METHOD_ID=...
make testacc
```

Because clusters only really disappear at the end of a billing cycle, acceptance tests leave
`PENDING_DELETION` clusters behind. Use a dedicated test organization.

## License

[MPL-2.0](LICENSE).
