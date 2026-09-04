resource "phasetwo_cluster_domain" "auth" {
  cluster_id = phasetwo_cluster.main.id
  host       = "auth.example.com"
}

# Phase Two validates the domain by DNS. Create the records it asks for — here with the AWS
# provider, but any DNS provider works.
resource "aws_route53_record" "validation" {
  for_each = {
    for record in phasetwo_cluster_domain.auth.domain_records : record.name => record
  }

  zone_id = var.zone_id
  name    = each.value.name
  type    = each.value.type
  records = [each.value.value]
  ttl     = 60
}

# Waiting for the certificate is off by default, because issuance depends on the DNS records
# above. Do it in a separate resource that depends on them, or simply apply twice.
output "certificate_status" {
  value = phasetwo_cluster_domain.auth.certificate_status
}
