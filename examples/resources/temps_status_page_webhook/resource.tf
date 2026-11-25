resource "temps_status_page_webhook" "chatops" {
  status_page_id = temps_status_page.public.id
  url            = "https://hooks.example.com/status"
  enabled        = true
}

# The signing secret is returned once, on create.
output "status_webhook_secret" {
  value     = temps_status_page_webhook.chatops.secret
  sensitive = true
}
