resource "temps_ai_instance_limits" "this" {
  max_requests_per_minute       = 1200
  max_cost_per_month_microcents = 50000000000
}
