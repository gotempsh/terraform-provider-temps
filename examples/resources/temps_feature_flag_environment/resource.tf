# Turn the redesigned checkout on in staging only.
resource "temps_feature_flag_environment" "new_checkout_staging" {
  project_id     = temps_project.web.id
  key            = temps_feature_flag.new_checkout.key
  environment_id = temps_environment.staging.id
  value          = jsonencode(true)
}

# In production, serve it to half of the paying customers and to the team.
resource "temps_feature_flag_environment" "new_checkout_production" {
  project_id     = temps_project.web.id
  key            = temps_feature_flag.new_checkout.key
  environment_id = temps_environment.production.id
  value          = jsonencode(false)

  rules = [
    {
      description = "Team"
      conditions  = [{ attribute = "email", operator = "matches", value = jsonencode(".*@example\\.com$") }]
      serve_value = jsonencode(true)
    },
    {
      description        = "Paying customers"
      conditions         = [{ attribute = "plan", operator = "in", value = jsonencode(["pro", "team"]) }]
      rollout_percentage = 50
      serve_value        = jsonencode(true)
    },
  ]
}
