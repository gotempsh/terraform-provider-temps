// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

const (
	destAddr     = "temps_backup_destination.primary"
	scheduleAddr = "temps_backup_schedule.nightly"
)

// s3Variables declares the bucket settings: dummy values for the fake, and
// TEMPS_ACC_S3_* for acceptance tests (a reachable S3-compatible bucket).
func (h *harness) s3Variables() string {
	get := func(env, def string) string {
		if h.fake != nil {
			return def
		}
		v := os.Getenv(env)
		if v == "" {
			h.t.Fatalf("%s must be set for backup acceptance tests", env)
		}
		return v
	}
	return fmt.Sprintf(`
locals {
  s3_endpoint = %q
  s3_bucket   = %q
  s3_key_id   = %q
  s3_secret   = %q
}
`, get("TEMPS_ACC_S3_ENDPOINT", "https://s3.example.com"), get("TEMPS_ACC_S3_BUCKET", "backups"),
		get("TEMPS_ACC_S3_ACCESS_KEY_ID", "AKIAEXAMPLE"), get("TEMPS_ACC_S3_SECRET_KEY", "example-secret"))
}

func destinationConfig(h *harness, name, path string) string {
	return h.config(h.s3Variables()+`
resource "temps_backup_destination" "primary" {
  name             = %q
  bucket_name      = local.s3_bucket
  bucket_path      = %q
  endpoint         = local.s3_endpoint
  force_path_style = true
  access_key_id    = local.s3_key_id
  secret_key       = local.s3_secret
  verify_on_apply  = true
}`, name, path)
}

func destinationSteps(h *harness) []resource.TestStep {
	name := h.name("dest")
	return []resource.TestStep{
		{
			Config:           destinationConfig(h, name, h.prefix+"/a"),
			ConfigPlanChecks: emptyPlanAfterApply(), // credentials come back masked: no drift
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(destAddr, "name", name),
				resource.TestCheckResourceAttr(destAddr, "force_path_style", "true"),
				resource.TestCheckResourceAttr(destAddr, "region", "us-east-1"),
				resource.TestCheckResourceAttrSet(destAddr, "is_default"),
			),
		},
		{
			ResourceName: destAddr, ImportState: true, ImportStateVerify: true,
			ImportStateVerifyIgnore: []string{"access_key_id", "secret_key", "verify_on_apply", "force_destroy"},
		},
		{
			Config:           destinationConfig(h, name+"-renamed", h.prefix+"/b"),
			ConfigPlanChecks: expectAction(destAddr, plancheck.ResourceActionUpdate),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(destAddr, "name", name+"-renamed"),
				resource.TestCheckResourceAttr(destAddr, "bucket_path", h.prefix+"/b"),
			),
		},
	}
}

func (h *harness) destinationDestroyed() resource.TestCheckFunc {
	return h.destroyed("temps_backup_destination", func(rs *terraform.ResourceState) error {
		_, err := h.client.GetS3Source(context.Background(), mustID(rs.Primary.ID))
		return err
	})
}

func TestUnitBackupDestination(t *testing.T) {
	h := unitHarness(t)
	h.run(destinationSteps(h), h.destinationDestroyed())
}

func TestAccBackupDestination(t *testing.T) {
	h := accHarness(t)
	h.run(destinationSteps(h), h.destinationDestroyed())
}

func TestUnitBackupDestinationVerifyFails(t *testing.T) {
	h := unitHarness(t)
	h.run([]resource.TestStep{{
		Config: h.config(`
resource "temps_backup_destination" "primary" {
  name            = "bad"
  bucket_name     = "backups"
  access_key_id   = "AKIAEXAMPLE"
  secret_key      = "wrong"
  verify_on_apply = true
}`),
		ExpectError: regexp.MustCompile(`(?s)failed verification.*Access Denied`),
	}}, nil)
}

func TestUnitBackupDestinationIsDefaultOnlyTrue(t *testing.T) {
	h := unitHarness(t)
	h.run([]resource.TestStep{{
		Config: h.config(`
resource "temps_backup_destination" "primary" {
  name          = "x"
  bucket_name   = "backups"
  access_key_id = "a"
  secret_key    = "b"
  is_default    = false
}`),
		ExpectError: regexp.MustCompile(`is_default can only be set to true`),
	}}, nil)
}

// A destination holding backups is only destroyed with force_destroy.
func TestUnitBackupDestinationForceDestroy(t *testing.T) {
	h := unitHarness(t)
	cfg := func(force bool) string {
		return h.config(`
resource "temps_backup_destination" "primary" {
  name          = "x"
  bucket_name   = "backups"
  access_key_id = "a"
  secret_key    = "b"
  force_destroy = %t
}`, force)
	}
	addBackup := func() {
		h.fake.with(func() {
			for id := range h.fake.sources {
				h.fake.backups["0b2d4c3e-0000-4000-8000-000000000001"] = id
			}
		})
	}
	h.run([]resource.TestStep{
		{Config: cfg(false), Check: func(*terraform.State) error { addBackup(); return nil }},
		{Config: cfg(false), Destroy: true, ExpectError: regexp.MustCompile(`(?s)still referenced by 1 backup.*set\s+force_destroy = true`)},
		{Config: cfg(true)},
	}, h.destinationDestroyed())
}

func twoDestinationsConfig(h *harness) string {
	return h.config(`
resource "temps_backup_destination" "a" {
  name          = "a"
  bucket_name   = "backups"
  bucket_path   = "a"
  access_key_id = "key"
  secret_key    = "secret"
}

resource "temps_backup_destination" "b" {
  name          = "b"
  bucket_name   = "backups"
  bucket_path   = "b"
  access_key_id = "key"
  secret_key    = "secret"
}`)
}

// Temps makes the first destination the default and fails one of two
// concurrent creates racing for it; it also refuses to delete the default
// while another destination exists. Creating and destroying two destinations
// in one run (both in parallel) must work regardless.
func TestUnitBackupDestinationsInParallel(t *testing.T) {
	h := unitHarness(t)
	h.run([]resource.TestStep{{
		Config:           twoDestinationsConfig(h),
		ConfigPlanChecks: emptyPlanAfterApply(),
		Check: func(*terraform.State) error {
			defaults := 0
			h.fake.with(func() {
				for _, s := range h.fake.sources {
					if s.s.IsDefault {
						defaults++
					}
				}
			})
			if defaults != 1 {
				return errorf("%d default destinations, want 1", defaults)
			}
			return nil
		},
	}}, h.destinationDestroyed())
}

// Destroying only the default destination while another one (not managed by
// this configuration) exists fails after a bounded wait, saying what to do.
func TestUnitBackupDestinationDefaultDeleteGivesUp(t *testing.T) {
	h := unitHarness(t)
	defer func(d time.Duration) { defaultDeleteWait = d }(defaultDeleteWait)
	defaultDeleteWait = 100 * time.Millisecond
	cfg := destinationConfig(h, "primary", "a")
	h.run([]resource.TestStep{
		{
			Config: cfg,
			Check: func(*terraform.State) error {
				h.fake.with(func() {
					h.fake.sources[9999] = &fakeSource{s: client.S3Source{ID: 9999, Name: "unmanaged", BucketName: "other"}}
				})
				return nil
			},
		},
		{
			Config: cfg, Destroy: true,
			ExpectError: regexp.MustCompile(`(?s)is\s+the\s+default.*default\s+backup\s+destination.*Set\s+is_default\s+=\s+true\s+on\s+another`),
		},
		{
			PreConfig: func() { h.fake.with(func() { delete(h.fake.sources, 9999) }) },
			Config:    cfg,
		},
	}, h.destinationDestroyed())
}

// A backup delete that fails with a server error is retried during
// force_destroy.
func TestUnitBackupDestinationForceDestroyRetriesServerErrors(t *testing.T) {
	h := unitHarness(t)
	const backup = "0b2d4c3e-0000-4000-8000-000000000002"
	cfg := h.config(`
resource "temps_backup_destination" "primary" {
  name          = "x"
  bucket_name   = "backups"
  access_key_id = "a"
  secret_key    = "b"
  force_destroy = true
}`)
	h.run([]resource.TestStep{{
		Config: cfg,
		Check: func(*terraform.State) error {
			h.fake.with(func() {
				for id := range h.fake.sources {
					h.fake.backups[backup] = id
				}
			})
			h.fake.fail("DELETE /api/backups/"+backup, 500, "Internal Server Error", "The backup operation could not be completed")
			return nil
		},
	}}, func(s *terraform.State) error {
		if err := h.destinationDestroyed()(s); err != nil {
			return err
		}
		retried := 0
		h.fake.with(func() {
			for _, r := range h.fake.requests {
				if r == "DELETE /api/backups/"+backup {
					retried++
				}
			}
		})
		if retried != 2 {
			return errorf("backup delete requests = %d, want 2 (one failure, one retry)", retried)
		}
		return nil
	})
}

func scheduleConfig(h *harness, expr string, retention int, enabled bool, dest string) string {
	return h.config(h.s3Variables()+`
resource "temps_backup_destination" "a" {
  name             = "%[1]s-a"
  bucket_name      = local.s3_bucket
  bucket_path      = "%[2]s/a"
  endpoint         = local.s3_endpoint
  force_path_style = true
  access_key_id    = local.s3_key_id
  secret_key       = local.s3_secret
  force_destroy    = true
}

resource "temps_backup_destination" "b" {
  name             = "%[1]s-b"
  bucket_name      = local.s3_bucket
  bucket_path      = "%[2]s/b"
  endpoint         = local.s3_endpoint
  force_path_style = true
  access_key_id    = local.s3_key_id
  secret_key       = local.s3_secret
  force_destroy    = true
}

resource "temps_service" "db" {
  name                = "%[1]s-db"
  service_type        = "postgres"
  parameters          = { database = "app", username = "app" }
  deletion_protection = false
}

resource "temps_backup_schedule" "nightly" {
  name                = "%[1]s-nightly"
  description         = "managed by terraform"
  schedule_expression = %[3]q
  retention_period    = %[4]d
  s3_source_id        = temps_backup_destination.%[6]s.id
  service_ids         = [temps_service.db.id]
  tags                = ["terraform", "nightly"]
  enabled             = %[5]t
}`, h.prefix, h.prefix, expr, retention, enabled, dest)
}

func scheduleSteps(h *harness) []resource.TestStep {
	return []resource.TestStep{
		{
			Config:           scheduleConfig(h, "0 0 3 * * *", 7, true, "a"),
			ConfigPlanChecks: emptyPlanAfterApply(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(scheduleAddr, "schedule_expression", "0 0 3 * * *"),
				resource.TestCheckResourceAttr(scheduleAddr, "backup_type", "full"),
				resource.TestCheckResourceAttr(scheduleAddr, "service_ids.#", "1"),
				resource.TestCheckResourceAttrPair(scheduleAddr, "service_ids.0", "temps_service.db", "id"),
				resource.TestCheckResourceAttrPair(scheduleAddr, "s3_source_id", "temps_backup_destination.a", "id"),
				resource.TestCheckResourceAttr(scheduleAddr, "tags.#", "2"),
			),
		},
		{ResourceName: scheduleAddr, ImportState: true, ImportStateVerify: true},
		{
			Config:           scheduleConfig(h, "0 30 4 * * *", 14, false, "a"),
			ConfigPlanChecks: expectAction(scheduleAddr, plancheck.ResourceActionUpdate),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(scheduleAddr, "schedule_expression", "0 30 4 * * *"),
				resource.TestCheckResourceAttr(scheduleAddr, "retention_period", "14"),
				resource.TestCheckResourceAttr(scheduleAddr, "enabled", "false"),
			),
		},
		{
			Config:           scheduleConfig(h, "0 30 4 * * *", 14, false, "b"),
			ConfigPlanChecks: expectAction(scheduleAddr, plancheck.ResourceActionReplace),
			Check:            resource.TestCheckResourceAttrPair(scheduleAddr, "s3_source_id", "temps_backup_destination.b", "id"),
		},
	}
}

func (h *harness) scheduleDestroyed() resource.TestCheckFunc {
	return h.destroyed("temps_backup_schedule", func(rs *terraform.ResourceState) error {
		_, err := h.client.GetBackupSchedule(context.Background(), mustID(rs.Primary.ID))
		return err
	})
}

func TestUnitBackupSchedule(t *testing.T) {
	h := unitHarness(t)
	h.run(scheduleSteps(h), h.scheduleDestroyed())
}

// Released servers send next_run and last_run as epoch milliseconds.
func TestUnitBackupScheduleLegacyTimes(t *testing.T) {
	h := unitHarness(t)
	h.fake.legacyShapes = true
	h.run(scheduleSteps(h), h.scheduleDestroyed())
}

// Moving from a released server to a newer one changes how times are sent,
// not what the provider reads or plans.
func TestUnitBackupScheduleAcrossServerVersions(t *testing.T) {
	h := unitHarness(t)
	var before string
	runAs := func(legacy bool) func() { return func() { h.fake.with(func() { h.fake.legacyShapes = legacy }) } }
	readNextRun := func(record bool) resource.TestCheckFunc {
		return func(s *terraform.State) error {
			id, err := stateInt64(s, scheduleAddr, "id")
			if err != nil {
				return err
			}
			got, err := h.client.GetBackupSchedule(context.Background(), id)
			if err != nil {
				return err
			}
			if got.NextRun == nil || string(*got.NextRun) != fakeNextRun {
				return fmt.Errorf("next_run = %v, want %s", got.NextRun, fakeNextRun)
			}
			if record {
				before = string(*got.NextRun)
			} else if string(*got.NextRun) != before {
				return fmt.Errorf("next_run read %q, then %q after the upgrade", before, *got.NextRun)
			}
			return nil
		}
	}
	cfg := scheduleConfig(h, "0 0 3 * * *", 7, true, "a")
	h.run([]resource.TestStep{
		{PreConfig: runAs(true), Config: cfg, ConfigPlanChecks: emptyPlanAfterApply(), Check: readNextRun(true)},
		{PreConfig: runAs(false), Config: cfg, PlanOnly: true},
		{Config: cfg, ConfigPlanChecks: emptyPlanAfterApply(), Check: readNextRun(false)},
	}, h.scheduleDestroyed())
}

func TestAccBackupSchedule(t *testing.T) {
	h := accHarness(t)
	h.run(scheduleSteps(h), h.scheduleDestroyed())
}

func TestUnitBackupScheduleFiveFieldCron(t *testing.T) {
	h := unitHarness(t)
	h.run([]resource.TestStep{{
		Config: h.config(`
resource "temps_backup_schedule" "nightly" {
  name                = "nightly"
  schedule_expression = "0 3 * * *"
  retention_period    = 7
}`),
		ExpectError: regexp.MustCompile(`(?s)Expected 6 or 7.*"0 3 \* \* \*" becomes\s+"0 0 3 \* \* \*"`),
	}}, nil)
}
