// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

var protoV6Factories = map[string]func() (tfprotov6.ProviderServer, error){
	"temps": providerserver.NewProtocol6WithError(New("test")()),
}

func init() { pollInterval = 10 * time.Millisecond }

// harness runs the same test steps against the in-memory fake (unit tests)
// or a real Temps server (acceptance tests, TF_ACC=1).
type harness struct {
	t      *testing.T
	fake   *fakeTemps
	client *client.Client
	prefix string
	// IDs recorded by step checks for later out-of-band changes.
	lastNotifiers []int64
	lastEdges     [][2]int64
}

func unitHarness(t *testing.T) *harness {
	t.Helper()
	f := newFakeTemps(t)
	c, err := client.New(client.Config{Endpoint: f.srv.URL, APIKey: fakeAPIKey})
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, fake: f, client: c, prefix: "unit"}
}

func accHarness(t *testing.T) *harness {
	t.Helper()
	if os.Getenv("TF_ACC") == "" {
		t.Skip("acceptance tests run with TF_ACC=1 against the server in TEMPS_ENDPOINT")
	}
	for _, v := range []string{envEndpoint, envAPIKey} {
		if os.Getenv(v) == "" {
			t.Fatalf("%s must be set for acceptance tests", v)
		}
	}
	c, err := client.New(client.Config{Endpoint: os.Getenv(envEndpoint), APIKey: os.Getenv(envAPIKey)})
	if err != nil {
		t.Fatal(err)
	}
	pollInterval = 2 * time.Second
	return &harness{t: t, client: c, prefix: acctest.RandomWithPrefix("tfacc")}
}

// config prepends a provider block pointing at the fake; acceptance tests
// configure the provider from the environment.
func (h *harness) config(body string, args ...any) string {
	if len(args) > 0 {
		body = fmt.Sprintf(body, args...)
	}
	if h.fake == nil {
		return body
	}
	return fmt.Sprintf("provider \"temps\" {\n  endpoint = %q\n  api_key  = %q\n}\n\n%s", h.fake.srv.URL, fakeAPIKey, body)
}

func (h *harness) name(s string) string { return h.prefix + "-" + s }

func (h *harness) run(steps []resource.TestStep, checkDestroy resource.TestCheckFunc) {
	tc := resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories,
		TerraformVersionChecks:   []tfversion.TerraformVersionCheck{tfversion.SkipBelow(tfversion.Version1_11_0)},
		Steps:                    steps,
		CheckDestroy:             checkDestroy,
	}
	if h.fake != nil {
		resource.UnitTest(h.t, tc)
	} else {
		resource.Test(h.t, tc)
	}
}

func emptyPlanAfterApply() resource.ConfigPlanChecks {
	return resource.ConfigPlanChecks{PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()}}
}

func expectAction(addr string, action plancheck.ResourceActionType) resource.ConfigPlanChecks {
	return resource.ConfigPlanChecks{
		PreApply:             []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, action)},
		PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
	}
}

// destroyed checks that every resource of type rtype in state is gone on the server.
func (h *harness) destroyed(rtype string, get func(rs *terraform.ResourceState) error) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		for _, rs := range s.RootModule().Resources {
			if rs.Type != rtype {
				continue
			}
			err := get(rs)
			if err == nil {
				return fmt.Errorf("%s %s still exists after destroy", rtype, rs.Primary.ID)
			}
			if !client.IsNotFound(err) {
				return fmt.Errorf("checking %s %s: %w", rtype, rs.Primary.ID, err)
			}
		}
		return nil
	}
}

func mustID(s string) int64 {
	id, err := parseID(s)
	if err != nil {
		panic(err)
	}
	return id
}

// stateID returns an import ID built from attributes of a resource in state.
func stateID(addr string, attrs ...string) resource.ImportStateIdFunc {
	return func(s *terraform.State) (string, error) {
		rs, ok := s.RootModule().Resources[addr]
		if !ok {
			return "", fmt.Errorf("%s not in state", addr)
		}
		id := ""
		for i, a := range attrs {
			if i > 0 {
				id += "/"
			}
			id += rs.Primary.Attributes[a]
		}
		return id, nil
	}
}

// projectConfig is a minimal project used as a parent by other tests.
func (h *harness) projectConfig(label string) string {
	return fmt.Sprintf(`
resource "temps_project" "parent" {
  name        = %q
  source_type = "docker_image"
  preset      = "dockerfile"
}

data "temps_environment" "production" {
  project_id = temps_project.parent.id
  name       = "production"
}
`, h.name(label))
}

func errorf(format string, args ...any) error { return fmt.Errorf(format, args...) }
