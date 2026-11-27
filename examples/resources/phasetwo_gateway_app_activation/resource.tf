# Activation verifies that the origin refuses requests without the secret, so it must come after
# whatever makes the origin require it, and after the DNS record.
resource "phasetwo_gateway_app_activation" "admin" {
  realm_id = phasetwo_gateway_app.admin.realm_id
  app_id   = phasetwo_gateway_app.admin.id

  depends_on = [
    aws_route53_record.admin,
    aws_secretsmanager_secret_version.origin_secret,
  ]
}
