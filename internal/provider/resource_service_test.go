// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

const serviceAddr = "temps_service.db"

func serviceConfig(h *harness, name, maxConns string, protected bool) string {
	return h.config(`
resource "temps_service" "db" {
  name         = %q
  service_type = "postgres"
  parameters = {
    database        = "app"
    username        = "app"
    max_connections = %q
  }
  deletion_protection = %t
}`, name, maxConns, protected)
}

func serviceSteps(h *harness) []resource.TestStep {
	name := h.name("db")
	return []resource.TestStep{
		{
			Config:           serviceConfig(h, name, "50", false),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(serviceAddr, "status", "running"),
				resource.TestCheckResourceAttr(serviceAddr, "topology", "standalone"),
				resource.TestCheckResourceAttrSet(serviceAddr, "docker_image"),
				resource.TestCheckResourceAttr(serviceAddr, "parameters.max_connections", "50"),
			),
		},
		{
			ResourceName: serviceAddr, ImportState: true, ImportStateVerify: true,
			// Parameters are not importable (sensitive ones are masked); the
			// protection flag and timeouts are provider-side settings.
			ImportStateVerifyIgnore: []string{"parameters", "deletion_protection", "timeouts"},
		},
		{
			Config:           serviceConfig(h, name, "80", false),
			ConfigPlanChecks: expectAction(serviceAddr, plancheck.ResourceActionUpdate),
			Check:            resource.TestCheckResourceAttr(serviceAddr, "parameters.max_connections", "80"),
		},
		{
			Config:           serviceConfig(h, name+"-2", "80", false),
			ConfigPlanChecks: expectAction(serviceAddr, plancheck.ResourceActionReplace),
			Check:            resource.TestCheckResourceAttr(serviceAddr, "name", name+"-2"),
		},
	}
}

func (h *harness) serviceDestroyed() resource.TestCheckFunc {
	return h.destroyed("temps_service", func(rs *terraform.ResourceState) error {
		_, err := h.client.GetService(context.Background(), mustID(rs.Primary.ID))
		return err
	})
}

func TestUnitService(t *testing.T) {
	h := unitHarness(t)
	h.run(serviceSteps(h), h.serviceDestroyed())
}

func TestAccService(t *testing.T) {
	h := accHarness(t)
	h.run(serviceSteps(h), h.serviceDestroyed())
}

func TestUnitServiceDeletionProtection(t *testing.T) {
	h := unitHarness(t)
	h.run([]resource.TestStep{
		{
			Config: serviceConfig(h, "db", "50", true),
			Check:  resource.TestCheckResourceAttr(serviceAddr, "deletion_protection", "true"),
		},
		{
			Config: serviceConfig(h, "db", "50", true), Destroy: true,
			ExpectError: regexp.MustCompile(`(?s)protected against deletion.*Set deletion_protection = false`),
		},
		{Config: serviceConfig(h, "db", "50", false)},
	}, h.serviceDestroyed())
}

func TestUnitServiceParameterTypes(t *testing.T) {
	h := unitHarness(t)
	h.run([]resource.TestStep{{
		Config:      serviceConfig(h, "db", "many", false),
		ExpectError: regexp.MustCompile(`parameter "max_connections" must be an integer, got "many"`),
	}}, nil)
}

func TestUnitServiceUnknownParameter(t *testing.T) {
	h := unitHarness(t)
	h.run([]resource.TestStep{{
		Config: h.config(`
resource "temps_service" "db" {
  name                = "db"
  service_type        = "postgres"
  parameters          = { database = "app", colour = "blue" }
  deletion_protection = false
}`),
		ExpectError: regexp.MustCompile(`parameter "colour" is not supported by this service type`),
	}}, nil)
}

// The server masks the password; a configured password must not cause drift.
func TestUnitServiceMaskedPasswordNoDrift(t *testing.T) {
	h := unitHarness(t)
	h.run([]resource.TestStep{{
		Config: h.config(`
resource "temps_service" "db" {
  name                = "db"
  service_type        = "postgres"
  parameters          = { password = "configured-secret", database = "app" }
  deletion_protection = false
}`),
		ConfigPlanChecks: emptyPlanAfterApply(),
		Check:            resource.TestCheckResourceAttr(serviceAddr, "parameters.password", "configured-secret"),
	}}, nil)
}

// database cannot change after creation, so changing it replaces the service.
func TestUnitServiceReadOnlyParameterReplaces(t *testing.T) {
	h := unitHarness(t)
	cfg := func(db string) string {
		return h.config(`
resource "temps_service" "db" {
  name                = "db"
  service_type        = "postgres"
  parameters          = { database = %q, max_connections = "50" }
  deletion_protection = false
}`, db)
	}
	h.run([]resource.TestStep{
		{Config: cfg("app")},
		{Config: cfg("other"), ConfigPlanChecks: expectAction(serviceAddr, plancheck.ResourceActionReplace)},
	}, nil)
}

func TestUnitServiceUpgradeImage(t *testing.T) {
	h := unitHarness(t)
	cfg := func(image string) string {
		return h.config(`
resource "temps_service" "db" {
  name                = "db"
  service_type        = "postgres"
  parameters          = { database = "app" }
  docker_image        = %q
  deletion_protection = false
}`, image)
	}
	h.run([]resource.TestStep{
		{Config: cfg("postgres:17"), Check: resource.TestCheckResourceAttr(serviceAddr, "docker_image", "postgres:17")},
		{
			Config: cfg("postgres:18"), ConfigPlanChecks: expectAction(serviceAddr, plancheck.ResourceActionUpdate),
			Check: resource.TestCheckResourceAttr(serviceAddr, "docker_image", "postgres:18"),
		},
	}, nil)
}

const linkAddr = "temps_service_link.web_db"

func serviceLinkConfig(h *harness, mode string) string {
	return h.config(h.projectConfig("link")+`
resource "temps_service" "db" {
  name                = %q
  service_type        = "postgres"
  parameters          = { database = "app", username = "app" }
  deletion_protection = false
}

resource "temps_service_link" "web_db" {
  service_id                 = temps_service.db.id
  project_id                 = temps_project.parent.id
  database_provisioning_mode = %q
}

data "temps_service" "by_name" {
  name       = temps_service.db.name
  depends_on = [temps_service.db]
}

data "temps_service_types" "all" {}

data "temps_service_connection" "web_db" {
  service_id = temps_service_link.web_db.service_id
  project_id = temps_service_link.web_db.project_id
}

data "temps_project" "by_slug" {
  slug = temps_project.parent.slug
}`, h.name("linkdb"), mode)
}

func serviceLinkSteps(h *harness) []resource.TestStep {
	return []resource.TestStep{
		{
			Config:           serviceLinkConfig(h, "project"),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(linkAddr, "database_provisioning_mode", "project"),
				resource.TestCheckResourceAttrPair("data.temps_service.by_name", "id", serviceAddr, "id"),
				resource.TestCheckResourceAttr("data.temps_service.by_name", "service_type", "postgres"),
				resource.TestCheckTypeSetElemAttr("data.temps_service_types.all", "types.*", "postgres"),
				resource.TestCheckResourceAttrSet("data.temps_service_connection.web_db", "variables.POSTGRES_HOST"),
				resource.TestCheckResourceAttrSet("data.temps_service_connection.web_db", "variables.POSTGRES_PASSWORD"),
				resource.TestCheckResourceAttrPair("data.temps_project.by_slug", "id", "temps_project.parent", "id"),
			),
		},
		{
			ResourceName: linkAddr, ImportState: true, ImportStateVerify: true,
			ImportStateIdFunc: stateID(linkAddr, "service_id", "project_id"),
		},
		{
			// Links have no in-place updates: every change replaces them.
			Config:           serviceLinkConfig(h, "project_environment"),
			ConfigPlanChecks: expectAction(linkAddr, plancheck.ResourceActionReplace),
			Check:            resource.TestCheckResourceAttr(linkAddr, "database_provisioning_mode", "project_environment"),
		},
	}
}

func (h *harness) serviceLinkDestroyed() resource.TestCheckFunc {
	return h.destroyed("temps_service_link", func(rs *terraform.ResourceState) error {
		_, err := h.client.GetServiceLink(context.Background(), mustID(rs.Primary.Attributes["service_id"]), mustID(rs.Primary.Attributes["project_id"]))
		return err
	})
}

func TestUnitServiceLink(t *testing.T) {
	h := unitHarness(t)
	h.run(serviceLinkSteps(h), h.serviceLinkDestroyed())
}

func TestAccServiceLink(t *testing.T) {
	h := accHarness(t)
	h.run(serviceLinkSteps(h), h.serviceLinkDestroyed())
}

func TestUnitServiceFailedProvisioning(t *testing.T) {
	h := unitHarness(t)
	h.run([]resource.TestStep{{
		Config:      serviceConfig(h, "db-broken", "50", false),
		ExpectError: regexp.MustCompile(`(?s)did not become ready.*is failed: container exited with code 1`),
	}}, nil)
}

func TestUnitServiceCreateTimeout(t *testing.T) {
	h := unitHarness(t)
	h.run([]resource.TestStep{{
		Config: h.config(`
resource "temps_service" "db" {
  name                = "db-stuck"
  service_type        = "postgres"
  parameters          = { database = "app" }
  deletion_protection = false
  timeouts {
    create = "1s"
  }
}`),
		ExpectError: regexp.MustCompile(`(?s)timed out waiting for temps_service "db-stuck" \(id \d+\) to\s+become\s+running;\s+last\s+status\s+"pending"`),
	}}, nil)
}
