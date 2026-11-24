# Only the office and the VPN may reach staging.
resource "temps_ip_access_policy" "staging" {
  project_id     = temps_project.web.id
  environment_id = temps_environment.staging.id
  mode           = "allow"
  cidrs = [
    { cidr = "198.51.100.0/24", description = "Office" },
    { cidr = "2001:db8:100::/48", description = "VPN" },
  ]
}

# Block a misbehaving network everywhere else.
resource "temps_ip_access_policy" "project" {
  project_id = temps_project.web.id
  mode       = "deny"
  cidrs      = [{ cidr = "203.0.113.0/24", description = "Scraper" }]
}
