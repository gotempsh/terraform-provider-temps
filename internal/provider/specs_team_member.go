// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import "net/http"

var teamMemberSpec = &gspec{
	Name: "team_member",
	Desc: "A user's membership of a `temps_team`, with the role the user holds in the team. Adding a user " +
		"who is already a member fails; import the membership instead.",
	Perms:   []string{"users:read", "users:write"},
	Parents: []string{"team_id"},
	IDKey:   "user_id",
	Fields: []gfield{
		{Name: "team_id", Kind: kInt, Required: true, Path: true, Desc: "ID of the team."},
		{Name: "user_id", Kind: kInt, Required: true, CreateOnly: true, Desc: "ID of the user."},
		{Name: "role", Kind: kString, Required: true, OneOf: []string{"owner", "admin", "deployer", "viewer"},
			Desc: "Role of the user in the team."},
		{Name: "user_email", Kind: kString, Computed: true, Desc: "Email address of the user."},
		{Name: "user_name", Kind: kString, Computed: true, Desc: "Display name of the user."},
	},
	Create: gop{Method: http.MethodPost, Path: "/teams/{team_id}/members"},
	Read:   gop{Path: "/teams/{team_id}/members/{id}"},
	Update: gop{Method: http.MethodPatch, Path: "/teams/{team_id}/members/{id}"},
	Delete: gop{Method: http.MethodDelete, Path: "/teams/{team_id}/members/{id}"},
}
