resource "temps_email_provider" "smtp" {
  name          = "Relay"
  provider_type = "smtp"
  region        = "eu-west-1"
  smtp_credentials = jsonencode({
    host     = "smtp.example.com"
    port     = 587
    username = "temps"
    password = var.smtp_password
  })
}
