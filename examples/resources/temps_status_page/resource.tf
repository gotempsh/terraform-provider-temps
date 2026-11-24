resource "temps_status_page" "public" {
  title       = "Example status"
  description = "Current status of the example services."
  published   = true

  monitors = [
    {
      monitor_id   = temps_monitor.web.id
      display_name = "Web"
      group_name   = "Core"
    },
  ]

  appearance = {
    theme                  = "auto"
    show_uptime_percentage = true
  }
}
