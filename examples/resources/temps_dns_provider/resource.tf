resource "temps_dns_provider" "cloudflare" {
  name          = "Cloudflare"
  provider_type = "cloudflare"
  credentials   = jsonencode({ api_token = var.cloudflare_token })
}
