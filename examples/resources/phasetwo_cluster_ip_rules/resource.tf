# One of these per cluster: it holds every rule list, because the API replaces a whole category
# at a time.
resource "phasetwo_cluster_ip_rules" "main" {
  cluster_id = phasetwo_cluster.main.id

  # Careful: once this is non-empty the admin console is reachable only from these addresses.
  admin_allow {
    alias   = "office"
    address = "203.0.113.0/24"
  }

  admin_allow {
    alias   = "vpn"
    address = "198.51.100.7"
  }

  realm_block {
    alias   = "known-abuse"
    address = "192.0.2.0/24"
  }
}
