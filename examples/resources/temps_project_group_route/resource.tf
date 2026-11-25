resource "temps_project_group_route" "api" {
  group_id          = temps_project_group.shop.id
  source_project_id = temps_project.web.id
  target_project_id = temps_project.api.id
  path_prefix       = "/api"
  strip_prefix      = true
}
