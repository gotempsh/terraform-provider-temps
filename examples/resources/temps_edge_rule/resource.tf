# Block the admin area for everyone outside the office network. New rules
# start in log_only mode; switch to enforce once the matches look right.
resource "temps_edge_rule" "admin" {
  project_id = temps_project.web.id
  name       = "admin from office only"
  mode       = "enforce"
  action     = "block"
  status     = 403
  body       = "Not available from this network."

  expression = jsonencode({
    combinator = "all"
    children = [
      { kind = "condition", field = "path", op = "starts_with", values = ["/admin"] },
      {
        kind       = "group"
        combinator = "none"
        children = [
          { kind = "condition", field = "ip", op = "in_cidr", values = ["203.0.113.0/24"] },
        ]
      },
    ]
  })
}

# Send old blog links to the new path, keeping the rest of the URL.
resource "temps_edge_rule" "blog" {
  project_id      = temps_project.web.id
  name            = "old blog"
  mode            = "enforce"
  action          = "redirect"
  status          = 301
  redirect_target = "/articles{path}?{query}"

  expression = jsonencode({
    combinator = "all"
    children   = [{ kind = "condition", field = "path", op = "starts_with", values = ["/blog/"] }]
  })
}
