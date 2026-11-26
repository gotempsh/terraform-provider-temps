resource "temps_notification_provider_cloudflare" "mail" {
  name         = "Cloudflare mail"
  account_id   = "0123456789abcdef0123456789abcdef"
  api_token    = var.cloudflare_email_token
  from_address = "alerts@mail.example.com"
  to_addresses = ["ops@example.com"]
}
