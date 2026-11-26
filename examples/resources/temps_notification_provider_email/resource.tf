resource "temps_notification_provider_email" "oncall" {
  name         = "On-call mail"
  smtp_host    = "smtp.example.com"
  smtp_port    = 587
  username     = "alerts@example.com"
  password     = var.smtp_password
  from_address = "alerts@example.com"
  from_name    = "Temps"
  to_addresses = ["oncall@example.com"]
}
