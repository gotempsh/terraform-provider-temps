data "temps_project_group" "web" {
  slug = "web-apps"
}

output "web_project_ids" {
  value = data.temps_project_group.web.project_ids
}
