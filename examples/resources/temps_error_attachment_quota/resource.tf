resource "temps_error_attachment_quota" "web" {
  project_id  = temps_project.web.id
  quota_bytes = 209715200 # 200 MiB
}
