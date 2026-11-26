resource "temps_project_analytics_settings" "web" {
  project_id                 = temps_project.web.id
  exclude_datacenter_traffic = true
  exclude_spam_referrers     = true
}
