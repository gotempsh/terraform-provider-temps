resource "temps_survey" "nps" {
  project_id = temps_project.web.id
  name       = "NPS"
  status     = "draft"
  questions  = jsonencode([{ id = "q1", kind = "nps", prompt = "How likely are you to recommend us?" }])
  targeting  = jsonencode({ path_pattern = "/pricing*", sample_rate = 0.25, once_per_visitor = true })
}
