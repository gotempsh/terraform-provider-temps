resource "temps_user_role" "ops_admin" {
  user_id   = temps_user.ops.id
  role_type = "admin"
}
