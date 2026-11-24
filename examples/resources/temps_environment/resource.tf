resource "temps_environment" "staging" {
  project_id  = temps_project.web.id
  name        = "staging"
  branch      = "develop"
  protected   = false
  force_https = true
}
