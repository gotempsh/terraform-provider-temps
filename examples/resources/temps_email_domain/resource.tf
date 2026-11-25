resource "temps_email_domain" "mail" {
  provider_id = temps_email_provider.smtp.id
  domain      = "mail.example.com"
}

# Publish these records in the zone so Temps can verify the domain.
output "mail_dns_records" {
  value = jsondecode(temps_email_domain.mail.dns_records)
}
