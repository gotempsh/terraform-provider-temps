# Turn the redesigned checkout on in staging only.
resource "temps_feature_flag_environment" "new_checkout_staging" {
  project_id     = temps_project.web.id
  key            = temps_feature_flag.new_checkout.key
  environment_id = temps_environment.staging.id
  value          = jsonencode(true)
}
