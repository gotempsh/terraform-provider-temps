resource "temps_wireguard_mesh" "this" {
  cidr        = "10.100.0.0/24"
  listen_port = 51820
}
