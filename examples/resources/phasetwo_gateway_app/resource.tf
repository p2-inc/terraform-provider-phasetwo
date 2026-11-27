resource "phasetwo_gateway_app" "admin" {
  realm_id        = phasetwo_realm.customer.id
  name            = "Internal admin console"
  public_hostname = "admin.example.com"
  origin_url      = "https://admin-origin.example.com"
  health_path     = "/healthz"

  # Anyone who can sign in may read /reports; only staff reach /admin; every other path is
  # refused. Every rule that matches a request must pass, so a rule on "/" would apply to all.
  policy = {
    default = "deny"
    rules = [
      { path_prefix = "/reports", methods = ["GET"] },
      { path_prefix = "/admin", groups_any = ["/staff"] },
    ]
  }

  header_mappings = {
    "X-Phasetwo-Auth-Email"  = "email"
    "X-Phasetwo-Auth-Groups" = "groups"
  }
  assertion_enabled = true
}

# Users go to the Gateway: point the public hostname at it.
resource "aws_route53_record" "admin" {
  zone_id = var.zone_id
  name    = phasetwo_gateway_app.admin.public_hostname
  type    = "CNAME"
  records = [phasetwo_gateway_app.admin.dns_target]
  ttl     = 300
}

# Hand the origin secret to whatever runs the origin, here as a secret its ingress reads.
resource "aws_secretsmanager_secret_version" "origin_secret" {
  secret_id     = var.origin_secret_id
  secret_string = phasetwo_gateway_app.admin.origin_secret
}
