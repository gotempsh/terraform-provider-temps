resource "temps_http_check" "api_auth" {
  project_id       = temps_project.web.id
  name             = "API token still valid"
  kind             = "http"
  interval_seconds = 3600
  credential       = var.api_token
  spec = jsonencode({
    url               = "https://api.example.com/me"
    method            = "get"
    accepted_statuses = [200]
    credential_header = "Authorization"
    credential_prefix = "Bearer "
  })
}
