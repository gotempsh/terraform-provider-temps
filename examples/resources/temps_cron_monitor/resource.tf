resource "temps_cron_monitor" "nightly_report" {
  project_id     = temps_project.web.id
  slug           = "nightly-report"
  schedule       = { crontab = "0 3 * * *" }
  timezone       = "Europe/Madrid"
  checkin_margin = 5
  max_runtime    = 60
}

resource "temps_cron_monitor" "sync" {
  project_id = temps_project.web.id
  slug       = "inventory-sync"
  schedule   = { interval = 15, unit = "minute" }
}
