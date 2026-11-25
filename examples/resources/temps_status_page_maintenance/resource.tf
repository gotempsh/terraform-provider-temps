resource "temps_status_page_maintenance" "db_upgrade" {
  status_page_id = temps_status_page.public.id
  title          = "Database upgrade"
  description    = "Writes may be slow for a few minutes."
  starts_at      = "2030-01-01T02:00:00Z"
  ends_at        = "2030-01-01T03:00:00Z"
  monitor_ids    = [temps_monitor.web.id]
}
