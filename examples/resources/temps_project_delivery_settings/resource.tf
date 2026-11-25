resource "temps_project_delivery_settings" "web" {
  project_id         = temps_project.web.id
  default_profile_id = temps_delivery_profile.direct.id
}
