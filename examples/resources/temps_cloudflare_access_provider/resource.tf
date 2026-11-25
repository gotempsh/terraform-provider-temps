variable "cloudflare_access_token" {
  type      = string
  sensitive = true
}

resource "temps_cloudflare_access_provider" "main" {
  name       = "Cloudflare"
  account_id = "0123456789abcdef0123456789abcdef"
  token      = var.cloudflare_access_token
}
