resource "temps_ingress_connector" "tunnel" {
  name          = "edge-tunnel"
  provider_type = "cloudflare"
  node_ids      = [2, 3]
  host_suffix   = "apps.example.com"
  values        = jsonencode({ account_id = var.cloudflare_account_id })
  secrets       = { api_token = var.cloudflare_tunnel_token }
}
