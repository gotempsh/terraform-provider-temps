# Every project in the group gets this variable.
resource "temps_project_group_variable" "region" {
  group_id         = temps_project_group.web.id
  key              = "REGION"
  value            = "eu-west"
  environment_slug = "production"
}

resource "temps_project_group_variable" "api_token" {
  group_id  = temps_project_group.web.id
  key       = "SHARED_API_TOKEN"
  value     = var.shared_api_token
  is_secret = true
}
