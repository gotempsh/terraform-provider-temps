resource "temps_funnel" "signup" {
  project_id  = temps_project.web.id
  name        = "Signup"
  description = "Landing page to completed signup"
  steps = jsonencode([
    { event_name = "page_view", event_filter = [] },
    { event_name = "signup_completed", event_filter = [] },
  ])
}
