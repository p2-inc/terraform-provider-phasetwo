# Only switch once the domain's certificate has been issued — the API checks the host is
# reachable before it will change.
resource "phasetwo_cluster_primary_host" "main" {
  cluster_id = phasetwo_cluster.main.id
  host       = phasetwo_cluster_domain.auth.host

  depends_on = [aws_route53_record.validation]
}
