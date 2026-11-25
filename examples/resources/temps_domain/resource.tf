resource "temps_domain" "wildcard" {
  domain         = "*.apps.example.com"
  challenge_type = "dns-01"
}
