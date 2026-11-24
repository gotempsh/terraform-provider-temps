variable "webhook_secret" {
  type      = string
  sensitive = true
}

resource "temps_webhook" "deploys" {
  project_id = temps_project.web.id
  url        = "https://hooks.example.com/temps"
  events     = ["deployment.succeeded", "deployment.failed"]
  secret     = var.webhook_secret
}
