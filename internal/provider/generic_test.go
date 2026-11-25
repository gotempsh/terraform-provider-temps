// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"
)

// engineFake serves the few endpoints the generic engine tests use, keeping
// every object as the JSON document the real API returns.
type engineFake struct {
	mu       sync.Mutex
	srv      *httptest.Server
	seq      int64
	dns      map[int64]map[string]any
	zones    map[int64]map[string]map[string]any // provider ID -> domain -> zone
	secrets  map[string]map[string]any
	values   map[string]string // secret name -> value received
	vaults   map[int64]map[string]any
	settings map[string]any
	// bumpVault makes the next vault PUT find a newer version, as after a
	// concurrent change.
	bumpVault bool
	requests  []string // "METHOD path"
	bodies    map[string]map[string]any
}

func newEngineFake(t *testing.T) *engineFake {
	t.Helper()
	f := &engineFake{
		dns: map[int64]map[string]any{}, zones: map[int64]map[string]map[string]any{},
		secrets: map[string]map[string]any{}, values: map[string]string{}, vaults: map[int64]map[string]any{},
		settings: map[string]any{"preview_domain": "old.example.com", "registry": map[string]any{"password": "********", "user": "ci"}},
		bodies:   map[string]map[string]any{},
	}
	mux := http.NewServeMux()
	h := func(pattern string, fn func(w http.ResponseWriter, r *http.Request, body map[string]any)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+fakeAPIKey {
				problem(w, http.StatusUnauthorized, "Unauthorized", "missing API key")
				return
			}
			var body map[string]any
			if r.Body != nil && r.ContentLength != 0 {
				_ = json.NewDecoder(r.Body).Decode(&body)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			key := r.Method + " " + r.URL.Path
			f.requests = append(f.requests, key)
			if body != nil {
				f.bodies[key] = body
			}
			fn(w, r, body)
		})
	}

	h("POST /api/dns-providers", func(w http.ResponseWriter, _ *http.Request, b map[string]any) {
		if _, ok := b["is_active"]; ok {
			problem(w, 422, "Unprocessable", "is_active is not accepted on create")
			return
		}
		creds, _ := b["credentials"].(map[string]any)
		if creds["type"] != b["provider_type"] {
			problem(w, 422, "Unprocessable", fmt.Sprintf("credentials.type %v must match %v", creds["type"], b["provider_type"]))
			return
		}
		f.seq++
		p := map[string]any{"id": f.seq, "name": b["name"], "provider_type": b["provider_type"],
			"description": b["description"], "is_active": true, "flat_hostnames_supported": false}
		f.dns[f.seq] = p
		writeJSON(w, 201, p)
	})
	h("GET /api/dns-providers/{id}", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		p, ok := f.dns[pathID(r, "id")]
		if !ok {
			notFound(w, "DNS provider", r.PathValue("id"))
			return
		}
		writeJSON(w, 200, p)
	})
	h("PUT /api/dns-providers/{id}", func(w http.ResponseWriter, r *http.Request, b map[string]any) {
		p, ok := f.dns[pathID(r, "id")]
		if !ok {
			notFound(w, "DNS provider", r.PathValue("id"))
			return
		}
		for _, k := range []string{"name", "description", "is_active"} {
			if v, set := b[k]; set {
				p[k] = v
			}
		}
		writeJSON(w, 200, p)
	})
	h("DELETE /api/dns-providers/{id}", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		delete(f.dns, pathID(r, "id"))
		delete(f.zones, pathID(r, "id"))
		w.WriteHeader(204)
	})
	h("POST /api/dns-providers/{pid}/domains", func(w http.ResponseWriter, r *http.Request, b map[string]any) {
		pid := pathID(r, "pid")
		if _, ok := f.dns[pid]; !ok {
			notFound(w, "DNS provider", pid)
			return
		}
		if f.zones[pid] == nil {
			f.zones[pid] = map[string]map[string]any{}
		}
		d, _ := b["domain"].(string)
		z := map[string]any{"domain": d, "auto_manage": b["auto_manage"] == true, "verified": true, "zone_id": "z-" + d}
		f.zones[pid][d] = z
		writeJSON(w, 201, z)
	})
	h("GET /api/dns-providers/{pid}/domains", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		pid := pathID(r, "pid")
		if _, ok := f.dns[pid]; !ok {
			notFound(w, "DNS provider", pid)
			return
		}
		out := []any{}
		for _, z := range f.zones[pid] {
			out = append(out, z)
		}
		writeJSON(w, 200, out)
	})
	h("PATCH /api/dns-providers/{pid}/domains/{domain}", func(w http.ResponseWriter, r *http.Request, b map[string]any) {
		z, ok := f.zones[pathID(r, "pid")][r.PathValue("domain")]
		if !ok {
			notFound(w, "zone", r.PathValue("domain"))
			return
		}
		for k, v := range b {
			z[k] = v
		}
		writeJSON(w, 200, z)
	})
	h("DELETE /api/dns-providers/{pid}/domains/{domain}", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		delete(f.zones[pathID(r, "pid")], r.PathValue("domain"))
		w.WriteHeader(204)
	})

	// Secrets: POST creates or overwrites, the list masks nothing because it
	// never returns values.
	h("POST /api/settings/secrets", func(w http.ResponseWriter, _ *http.Request, b map[string]any) {
		name, _ := b["name"].(string)
		f.values[name], _ = b["value"].(string)
		s := map[string]any{"name": name, "description": b["description"], "secret_type": "env", "mount_path": nil}
		if t, ok := b["secret_type"]; ok {
			s["secret_type"] = t
		}
		f.secrets[name] = s
		writeJSON(w, 200, s)
	})
	h("GET /api/settings/secrets", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		out := []any{}
		for _, s := range f.secrets {
			out = append(out, s)
		}
		writeJSON(w, 200, map[string]any{"items": out})
	})
	h("DELETE /api/settings/secrets/{name}", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		delete(f.secrets, r.PathValue("name"))
		w.WriteHeader(204)
	})

	// Vaults carry a version every write must name.
	h("POST /api/access-vaults", func(w http.ResponseWriter, _ *http.Request, b map[string]any) {
		f.seq++
		v := map[string]any{"id": strconv.FormatInt(f.seq, 10), "name": b["name"], "service_ids": b["service_ids"], "version": 1}
		f.vaults[f.seq] = v
		writeJSON(w, 201, v)
	})
	h("GET /api/access-vaults/{id}", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		v, ok := f.vaults[pathID(r, "id")]
		if !ok {
			notFound(w, "access vault", r.PathValue("id"))
			return
		}
		writeJSON(w, 200, map[string]any{"vault": v})
	})
	h("PUT /api/access-vaults/{id}", func(w http.ResponseWriter, r *http.Request, b map[string]any) {
		v, ok := f.vaults[pathID(r, "id")]
		if !ok {
			notFound(w, "access vault", r.PathValue("id"))
			return
		}
		if f.bumpVault {
			f.bumpVault = false
			v["version"] = v["version"].(int) + 1
		}
		if fmt.Sprint(b["expected_version"]) != fmt.Sprint(v["version"]) {
			problem(w, 409, "Conflict", fmt.Sprintf("vault %s is at version %v, not %v", r.PathValue("id"), v["version"], b["expected_version"]))
			return
		}
		v["name"], v["service_ids"] = b["name"], b["service_ids"]
		v["version"] = v["version"].(int) + 1
		writeJSON(w, 200, v)
	})
	h("DELETE /api/access-vaults/{id}", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		delete(f.vaults, pathID(r, "id"))
		w.WriteHeader(204)
	})

	// Settings: one document, merged on write, a secret masked on read.
	h("GET /api/settings", func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		writeJSON(w, 200, f.settings)
	})
	h("PUT /api/settings", func(w http.ResponseWriter, _ *http.Request, b map[string]any) {
		for k, v := range b {
			if k == "registry" {
				if m, ok := v.(map[string]any); ok {
					m["password"] = "********"
				}
			}
			f.settings[k] = v
		}
		writeJSON(w, 200, f.settings)
	})

	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *engineFake) config(body string) string {
	return fmt.Sprintf("provider \"temps\" {\n  endpoint = %q\n  api_key  = %q\n}\n\n%s", f.srv.URL, fakeAPIKey, body)
}

func (f *engineFake) count(req string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if r == req {
			n++
		}
	}
	return n
}

func (f *engineFake) with(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

func runEngine(t *testing.T, steps []resource.TestStep, destroyed resource.TestCheckFunc) {
	t.Helper()
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories,
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_11_0)},
		Steps:                    steps,
		CheckDestroy:             destroyed,
	})
}

const engineDNSConfig = `
resource "temps_dns_provider" "p" {
  name          = "edge"
  provider_type = "cloudflare"
  credentials   = jsonencode({ api_token = %q })
  %s
}

resource "temps_dns_provider_domain" "z" {
  provider_id = temps_dns_provider.p.id
  domain      = "example.com"
  auto_manage = %t
}`

// TestUnitGenericCRUDListAndImport covers create, a list read, in-place
// update, clearing an optional value, write-only credentials, composite
// import IDs and destroy, through a provider and its zone.
func TestUnitGenericCRUDListAndImport(t *testing.T) {
	f := newEngineFake(t)
	cfg := func(token, extra string, auto bool) string {
		return f.config(fmt.Sprintf(engineDNSConfig, token, extra, auto))
	}
	runEngine(t, []resource.TestStep{
		{
			Config: cfg("tok-1", `description = "zones"`, false),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("temps_dns_provider.p", "description", "zones"),
				resource.TestCheckResourceAttr("temps_dns_provider.p", "is_active", "true"),
				resource.TestCheckResourceAttr("temps_dns_provider_domain.z", "zone_id", "z-example.com"),
				resource.TestCheckResourceAttr("temps_dns_provider_domain.z", "id", "1/example.com"),
				func(*terraform.State) error {
					var creds map[string]any
					f.with(func() { creds, _ = f.bodies["POST /api/dns-providers"]["credentials"].(map[string]any) })
					if creds["type"] != "cloudflare" || creds["api_token"] != "tok-1" {
						return fmt.Errorf("create sent credentials %v", creds)
					}
					return nil
				},
			),
			ConfigPlanChecks: emptyPlanAfterApply(),
		},
		{
			// Dropping description clears it (null is sent); the zone updates in place.
			Config:           cfg("tok-1", "", true),
			ConfigPlanChecks: expectAction("temps_dns_provider_domain.z", plancheck.ResourceActionUpdate),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckNoResourceAttr("temps_dns_provider.p", "description"),
				resource.TestCheckResourceAttr("temps_dns_provider_domain.z", "auto_manage", "true"),
				func(*terraform.State) error {
					var b map[string]any
					f.with(func() { b = f.bodies["PUT /api/dns-providers/1"] })
					if v, ok := b["description"]; !ok || v != nil {
						return fmt.Errorf("update must send description: null, sent %v", b)
					}
					return nil
				},
			),
		},
		{
			ResourceName:            "temps_dns_provider.p",
			ImportState:             true,
			ImportStateVerify:       true,
			ImportStateVerifyIgnore: []string{"credentials"},
		},
		{
			ResourceName:      "temps_dns_provider_domain.z",
			ImportState:       true,
			ImportStateId:     "1/example.com",
			ImportStateVerify: true,
		},
		{
			ResourceName:  "temps_dns_provider_domain.z",
			ImportState:   true,
			ImportStateId: "example.com",
			ExpectError:   regexp.MustCompile(`<provider_id>/<domain>`),
		},
		{
			// Changing only write-only credentials is sent to Temps.
			Config:           cfg("tok-2", "", true),
			ConfigPlanChecks: expectAction("temps_dns_provider.p", plancheck.ResourceActionUpdate),
			Check: func(*terraform.State) error {
				var creds map[string]any
				f.with(func() { creds, _ = f.bodies["PUT /api/dns-providers/1"]["credentials"].(map[string]any) })
				if creds["api_token"] != "tok-2" || creds["type"] != "cloudflare" {
					return fmt.Errorf("update sent credentials %v", creds)
				}
				return nil
			},
		},
		{
			// Changing the provider type replaces the provider and its zone.
			Config: f.config(`
resource "temps_dns_provider" "p" {
  name          = "edge"
  provider_type = "route53"
  credentials   = jsonencode({ access_key_id = "a", secret_access_key = "b" })
}`),
			ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction("temps_dns_provider.p", plancheck.ResourceActionDestroyBeforeCreate),
				plancheck.ExpectResourceAction("temps_dns_provider_domain.z", plancheck.ResourceActionDestroy),
			}},
		},
	}, func(*terraform.State) error {
		var n int
		f.with(func() { n = len(f.dns) })
		if n != 0 {
			return fmt.Errorf("%d DNS providers left after destroy", n)
		}
		return nil
	})
}

// TestUnitGenericGoneOutOfBand checks that an object deleted outside
// Terraform is planned for re-creation, for a direct read and a list read.
func TestUnitGenericGoneOutOfBand(t *testing.T) {
	f := newEngineFake(t)
	cfg := f.config(fmt.Sprintf(engineDNSConfig, "tok", "", false))
	runEngine(t, []resource.TestStep{
		{Config: cfg},
		{
			PreConfig: func() { f.with(func() { delete(f.zones[1], "example.com") }) },
			Config:    cfg,
			ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction("temps_dns_provider_domain.z", plancheck.ResourceActionCreate),
			}},
		},
		{
			PreConfig: func() { f.with(func() { delete(f.dns, 1); delete(f.zones, 1) }) },
			Config:    cfg,
			ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction("temps_dns_provider.p", plancheck.ResourceActionCreate),
				plancheck.ExpectResourceAction("temps_dns_provider_domain.z", plancheck.ResourceActionCreate),
			}},
		},
	}, nil)
}

// TestUnitGenericWriteOnlyUpsert covers a resource keyed by name whose value
// is never read back: import keeps working, and an update without a change
// to anything Temps takes is not sent.
func TestUnitGenericWriteOnlyUpsert(t *testing.T) {
	f := newEngineFake(t)
	cfg := func(value, desc string) string {
		return f.config(fmt.Sprintf(`
resource "temps_global_secret" "s" {
  name        = "REGISTRY_TOKEN"
  value       = %q
  description = %q
}`, value, desc))
	}
	runEngine(t, []resource.TestStep{
		{
			Config:           cfg("v1", "registry"),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("temps_global_secret.s", "id", "REGISTRY_TOKEN"),
				resource.TestCheckResourceAttr("temps_global_secret.s", "secret_type", "env"),
			),
		},
		{
			Config:           cfg("v2", "registry"),
			ConfigPlanChecks: expectAction("temps_global_secret.s", plancheck.ResourceActionUpdate),
			Check: func(*terraform.State) error {
				var v string
				f.with(func() { v = f.values["REGISTRY_TOKEN"] })
				if v != "v2" {
					return fmt.Errorf("secret value is %q, want v2", v)
				}
				return nil
			},
		},
		{
			ResourceName:            "temps_global_secret.s",
			ImportState:             true,
			ImportStateVerify:       true,
			ImportStateVerifyIgnore: []string{"value"},
		},
	}, func(*terraform.State) error {
		var n int
		f.with(func() { n = len(f.secrets) })
		if n != 0 {
			return fmt.Errorf("%d secrets left after destroy", n)
		}
		return nil
	})
}

// TestUnitGenericExpectedVersion checks that updates name the version read
// at refresh, so a change made elsewhere since is refused, not overwritten.
func TestUnitGenericExpectedVersion(t *testing.T) {
	f := newEngineFake(t)
	cfg := func(name string) string {
		return f.config(fmt.Sprintf(`
resource "temps_access_vault" "v" {
  name        = %q
  service_ids = ["svc-a"]
}`, name))
	}
	runEngine(t, []resource.TestStep{
		{Config: cfg("one"), ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.TestCheckResourceAttr("temps_access_vault.v", "version", "1")},
		{Config: cfg("two"), ConfigPlanChecks: expectAction("temps_access_vault.v", plancheck.ResourceActionUpdate),
			Check: func(*terraform.State) error {
				var b map[string]any
				f.with(func() { b = f.bodies["PUT /api/access-vaults/1"] })
				if fmt.Sprint(b["expected_version"]) != "1" {
					return fmt.Errorf("update sent expected_version %v, want 1", b["expected_version"])
				}
				return nil
			}},
		{
			// Another client bumps the version after Terraform's refresh: the
			// update must fail with Temps' conflict, not overwrite.
			Config:      cfg("three"),
			PreConfig:   func() { f.with(func() { f.bumpVault = true }) },
			ExpectError: regexp.MustCompile(`(?s)409.*version`),
		},
	}, nil)
}

// TestUnitGenericSettingsSubset checks a singleton JSON document: only the
// configured keys are compared, a masked secret counts as unchanged, and a
// change elsewhere to a configured key is detected.
func TestUnitGenericSettingsSubset(t *testing.T) {
	f := newEngineFake(t)
	cfg := f.config(`
resource "temps_settings" "s" {
  values = jsonencode({
    preview_domain = "preview.example.com"
    registry       = { user = "ci", password = "hunter2" }
  })
}`)
	runEngine(t, []resource.TestStep{
		{Config: cfg, ConfigPlanChecks: emptyPlanAfterApply()},
		{
			PreConfig: func() { f.with(func() { f.settings["preview_domain"] = "drift.example.com" }) },
			Config:    cfg,
			ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
				plancheck.ExpectResourceAction("temps_settings.s", plancheck.ResourceActionUpdate),
			}},
			Check: func(*terraform.State) error {
				var d any
				f.with(func() { d = f.settings["preview_domain"] })
				if d != "preview.example.com" {
					return fmt.Errorf("preview_domain is %v after apply", d)
				}
				return nil
			},
		},
		{ResourceName: "temps_settings.s", ImportState: true, ImportStateId: "settings",
			ImportStateCheck: func(s []*terraform.InstanceState) error {
				if len(s) != 1 || s[0].Attributes["values"] == "" {
					return fmt.Errorf("import produced %v", s)
				}
				return nil
			}},
	}, func(*terraform.State) error {
		// Destroying only stops managing the settings.
		if n := f.count("DELETE /api/settings"); n != 0 {
			return fmt.Errorf("destroy sent %d DELETE /api/settings", n)
		}
		return nil
	})
}

func TestGenericJSONHelpers(t *testing.T) {
	for _, tc := range []struct {
		configured, server string
		want               bool
	}{
		{`{"a":1}`, `{"a":1,"b":2}`, true},
		{`{"a":{"x":1}}`, `{"a":{"x":1,"y":2}}`, true},
		{`{"a":1}`, `{"a":2}`, false},
		{`{"a":1}`, `{}`, false},
		{`{"a":null}`, `{}`, true},
		{`{"p":"secret"}`, `{"p":"********"}`, true},
		{`{"p":"secret"}`, `{"p":"sk-***"}`, true},
		{`{"p":"secret"}`, `{"p":"other"}`, false},
		{`{"l":[1,2]}`, `{"l":[1,2,3]}`, false},
		{`{"l":[{"a":1}]}`, `{"l":[{"a":1,"b":2}]}`, true},
		{`not json`, `{}`, false},
	} {
		if got := jsonSubset(tc.configured, tc.server); got != tc.want {
			t.Errorf("jsonSubset(%s, %s) = %v, want %v", tc.configured, tc.server, got, tc.want)
		}
	}

	obj := map[string]any{}
	setPath(obj, "a.b.c", 1)
	if v, ok := getPath(obj, "a.b.c"); !ok || v != 1 {
		t.Errorf("getPath after setPath = %v, %v", v, ok)
	}
	if _, ok := getPath(obj, "a.x.c"); ok {
		t.Error("getPath found a missing key")
	}
	merged := overlay(map[string]any{"a": map[string]any{"x": 1, "y": 2}, "k": 1},
		map[string]any{"a": map[string]any{"y": 3}, "k": "v"})
	if fmt.Sprint(merged) != "map[a:map[x:1 y:3] k:v]" {
		t.Errorf("overlay = %v", merged)
	}
}

func TestGenericRenderPathEscapes(t *testing.T) {
	v := gvals{}
	got := renderPath("/dns-providers/{id}/domains/{id}", v, "a b/c")
	if got != "/dns-providers/a%20b%2Fc/domains/a%20b%2Fc" {
		t.Errorf("renderPath = %q", got)
	}
}

func TestGenericClearWithSendsEmptyString(t *testing.T) {
	r := &genericResource{s: &gspec{Name: "thing", Fields: []gfield{
		{Name: "description", Kind: kString, Clearable: true, ClearWith: ""},
		{Name: "note", Kind: kString, Clearable: true},
	}}}
	v := gvals{"description": types.StringNull(), "note": types.StringNull()}
	var diags diag.Diagnostics
	body := r.body(context.Background(), v, false, &diags)
	if diags.HasError() {
		t.Fatal(diags)
	}
	if d, ok := body["description"]; !ok || d != "" {
		t.Fatalf("description = %#v (present %v), want \"\"", d, ok)
	}
	if n, ok := body["note"]; !ok || n != nil {
		t.Fatalf("note = %#v (present %v), want null", n, ok)
	}
	// Nothing is cleared on create.
	if body := r.body(context.Background(), v, true, &diags); len(body) != 0 {
		t.Fatalf("create body = %#v, want empty", body)
	}
}

func TestGenericClearWithReadsEmptyAsUnset(t *testing.T) {
	r := &genericResource{s: &gspec{Name: "thing", Fields: []gfield{
		{Name: "description", Kind: kString, Clearable: true, ClearWith: ""},
		{Name: "note", Kind: kString, Clearable: true},
	}}}
	v := gvals{"description": types.StringValue("old"), "note": types.StringValue("old")}
	var diags diag.Diagnostics
	r.apply(v, map[string]any{"description": "", "note": ""}, &diags)
	if diags.HasError() {
		t.Fatal(diags)
	}
	if !v["description"].IsNull() {
		t.Fatalf("description = %v, want null", v["description"])
	}
	// Without ClearWith an empty string is a value like any other.
	if got := v["note"].(types.String).ValueString(); got != "" || v["note"].IsNull() {
		t.Fatalf("note = %v, want \"\"", v["note"])
	}
}

func TestErrorSpecsNormalizeServerKeys(t *testing.T) {
	rules := map[string]any{"data": []any{map[string]any{
		"id": 7.0, "position": 0.0, "owner_name": "Admin", "created_at": "x", "updated_at": "x",
		"pattern": "src/**", "owner_user_id": 1.0,
	}}}
	errorOwnershipRulesSpec.Normalize(rules)
	if got := rules["data"].([]any)[0]; !sameJSON(got, map[string]any{"pattern": "src/**", "owner_user_id": 1.0}) {
		t.Fatalf("rule = %#v", got)
	}
	search := map[string]any{"filters": map[string]any{"status": "unresolved", "assignee": nil, "sort_order": "desc"}}
	errorSavedSearchSpec.Normalize(search)
	if got := search["filters"]; !sameJSON(got, map[string]any{"status": "unresolved", "sort_order": "desc"}) {
		t.Fatalf("filters = %#v", got)
	}
}

func sameJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
