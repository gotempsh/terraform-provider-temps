resource "temps_custom_domain" "app" {
  project_id     = temps_project.web.id
  environment_id = data.temps_environment.production.id
  domain         = "app.example.com"
}

# Send the bare domain to the app with a permanent redirect.
resource "temps_custom_domain" "apex" {
  project_id     = temps_project.web.id
  environment_id = data.temps_environment.production.id
  domain         = "example.com"
  redirect_to    = "https://app.example.com"
  status_code    = 308
}
