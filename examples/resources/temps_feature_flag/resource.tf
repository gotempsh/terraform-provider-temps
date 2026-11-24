resource "temps_feature_flag" "new_checkout" {
  project_id    = temps_project.web.id
  key           = "new-checkout"
  value_type    = "bool"
  default_value = jsonencode(false)
  description   = "Serve the redesigned checkout"
}

resource "temps_feature_flag" "checkout_limits" {
  project_id    = temps_project.web.id
  key           = "checkout.limits"
  value_type    = "json"
  default_value = jsonencode({ max_items = 50, currencies = ["EUR", "USD"] })
}
