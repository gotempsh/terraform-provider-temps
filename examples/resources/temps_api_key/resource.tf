resource "temps_project" "web" {
  name = "web"
}

# A key for a CI job that only reads deployments of one project.
resource "temps_api_key" "ci" {
  name        = "ci-read-only"
  role        = "custom"
  permissions = ["projects:read", "deployments:read"]
  project_id  = temps_project.web.id
  expires_at  = "2027-06-30T00:00:00Z"
}

output "ci_key" {
  value     = temps_api_key.ci.key
  sensitive = true
}
