resource "temps_error_ownership_rules" "web" {
  project_id = temps_project.web.id
  rules = jsonencode([
    { pattern = "src/billing/**", owner_user_id = 1 },
    { pattern = "src/auth/**", owner_user_id = 2 },
  ])
}
