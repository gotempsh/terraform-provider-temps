resource "temps_team_member" "ada" {
  team_id = temps_team.platform.id
  user_id = temps_user.ada.id
  role    = "deployer"
}
