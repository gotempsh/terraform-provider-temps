// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// moreFake serves the notification provider, team member, alert rule,
// OpenTelemetry, analytics and project access endpoints, with the quirks of
// the real handlers the provider has to cope with. Objects are kept as the
// JSON documents Temps returns.
type moreFake struct {
	mu  sync.Mutex
	srv *httptest.Server
	seq int64

	notifiers map[int64]map[string]any  // stored unmasked
	members   map[string]map[string]any // "<team>/<user>"
	nodeRules map[int64]map[string]any
	svcRules  map[int64]map[string]any
	otelRules map[int64]map[string]any
	dashes    map[int64]map[string]any
	goals     map[int64]map[string]any
	filters   map[int64]map[string]any
	settings  map[int64]map[string]any // project -> analytics settings
	services  map[string]map[string]any
	vaults    map[string]map[string]any
	links     []map[string]any

	requests []string // "METHOD path[?query]"
	bodies   map[string]map[string]any
}

// Known parents. Anything else answers 404.
var (
	moreProjects = []int64{8, 9}
	moreTeams    = []int64{3}
	moreUsers    = map[int64][2]string{11: {"ada@example.com", "Ada"}, 12: {"lin@example.com", "Lin"}}
	moreNodes    = []int64{0, 7}
	moreServices = []int64{21}
)

func newMoreFake(t *testing.T) *moreFake {
	t.Helper()
	f := &moreFake{
		notifiers: map[int64]map[string]any{}, members: map[string]map[string]any{},
		nodeRules: map[int64]map[string]any{}, svcRules: map[int64]map[string]any{},
		otelRules: map[int64]map[string]any{}, dashes: map[int64]map[string]any{},
		goals: map[int64]map[string]any{}, filters: map[int64]map[string]any{},
		settings: map[int64]map[string]any{}, services: map[string]map[string]any{},
		vaults: map[string]map[string]any{}, bodies: map[string]map[string]any{},
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
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					problem(w, http.StatusBadRequest, "Bad Request", err.Error())
					return
				}
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			key := r.Method + " " + r.URL.Path
			if r.URL.RawQuery != "" && !strings.Contains(r.URL.RawQuery, "page=") {
				key += "?" + r.URL.RawQuery
			}
			f.requests = append(f.requests, key)
			if body != nil {
				f.bodies[key] = body
			}
			fn(w, r, body)
		})
	}
	for _, routes := range moreRoutes {
		routes(f, h)
	}
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

type moreHandler = func(string, func(http.ResponseWriter, *http.Request, map[string]any))

// moreRoutes install each area's endpoints; the files serving them register
// here.
var moreRoutes []func(*moreFake, moreHandler)

func (f *moreFake) next() int64 { f.seq++; return f.seq }

func (f *moreFake) config(body string, args ...any) string {
	if len(args) > 0 {
		body = fmt.Sprintf(body, args...)
	}
	return fmt.Sprintf("provider \"temps\" {\n  endpoint = %q\n  api_key  = %q\n}\n\n%s", f.srv.URL, fakeAPIKey, body)
}

func (f *moreFake) with(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

func (f *moreFake) count(req string) int {
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

func (f *moreFake) body(req string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bodies[req]
}

func knownParent(w http.ResponseWriter, r *http.Request, name, what string, ids []int64) (int64, bool) {
	id := pathID(r, name)
	if !slices.Contains(ids, id) || (id == 0 && r.PathValue(name) != "0") {
		notFound(w, what, r.PathValue(name))
		return 0, false
	}
	return id, true
}

func copyDoc(m map[string]any) map[string]any {
	b, _ := json.Marshal(m)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

func sortedIDs(m map[int64]map[string]any) []int64 {
	ids := make([]int64, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// plannedCreate expects addr to be created again (it is gone from Temps).
func plannedCreate(addr string) resource.ConfigPlanChecks {
	return resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionCreate)}}
}

// idOf reads the ID of a resource in state.
func idOf(s *terraform.State, addr string) string {
	rs, ok := s.RootModule().Resources[addr]
	if !ok {
		return ""
	}
	return rs.Primary.ID
}
