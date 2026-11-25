resource "temps_project_compose_security" "stack" {
  project_id        = temps_project.stack.id
  disabled_checks   = ["published_ports"]
  acknowledge_risks = true
}
