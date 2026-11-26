resource "temps_domain" "wildcard" {
  domain         = "*.apps.example.com"
  challenge_type = "dns-01"

  # Renew three weeks ahead and warn 10 days before expiry. Left out,
  # alerts_enabled follows the instance SSL expiry alert setting.
  certificate_settings = {
    renewal_days = 21
    alert_days   = 10
  }
}
