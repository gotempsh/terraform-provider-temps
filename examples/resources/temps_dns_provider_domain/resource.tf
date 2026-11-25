resource "temps_dns_provider_domain" "example" {
  provider_id = temps_dns_provider.cloudflare.id
  domain      = "example.com"
  auto_manage = true
}
