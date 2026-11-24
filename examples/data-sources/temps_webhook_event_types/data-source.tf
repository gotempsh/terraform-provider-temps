data "temps_webhook_event_types" "all" {}

# Subscribe to every deployment event.
resource "temps_webhook" "all_deployments" {
  project_id = temps_project.web.id
  url        = "https://hooks.example.com/temps"
  events     = [for e, c in data.temps_webhook_event_types.all.categories : e if c == "Deployment"]
}
