resource "temps_user" "ops" {
  username      = "ops"
  email         = "ops@example.com"
  password      = var.ops_initial_password
  initial_roles = ["user"]
}
