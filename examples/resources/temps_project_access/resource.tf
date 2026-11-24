resource "temps_project_access" "platform_web" {
  project_id = temps_project.web.id
  team_id    = temps_team.platform.id
  role       = "deployer"
}
