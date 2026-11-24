resource "temps_notification_provider" "alerts" {
  name         = "Team alerts"
  url          = "https://hooks.example.com/alerts"
  method       = "POST"
  timeout_secs = 10
  headers = {
    Authorization = "Bearer ${var.alerts_token}"
  }
}

variable "alerts_token" {
  type      = string
  sensitive = true
}
