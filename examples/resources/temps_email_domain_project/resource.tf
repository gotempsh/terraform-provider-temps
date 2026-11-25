resource "temps_email_domain_project" "web" {
  email_domain_id = temps_email_domain.mail.id
  project_id      = temps_project.web.id
}
