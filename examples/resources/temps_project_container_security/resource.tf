resource "temps_project_container_security" "web" {
  project_id           = temps_project.web.id
  allowed_capabilities = ["NET_BIND_SERVICE"]
  acknowledge_risks    = true
}
