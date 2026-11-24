resource "temps_status_page_domain" "status" {
  status_page_id = temps_status_page.public.id
  hostname       = "status.example.com"
}

# Point these records at Temps before verification can succeed.
output "status_dns_records" {
  value = temps_status_page_domain.status.dns_records
}
