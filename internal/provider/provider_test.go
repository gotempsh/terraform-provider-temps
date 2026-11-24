// SPDX-FileCopyrightText: 2024-2026 Temps Contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package provider

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/gotempsh/terraform-provider-temps/internal/client"
)

func TestUnitProviderRequiresEndpointAndKey(t *testing.T) {
	t.Setenv(envEndpoint, "")
	t.Setenv(envAPIKey, "")
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories,
		Steps: []resource.TestStep{{
			Config:      `data "temps_service_types" "all" {}`,
			ExpectError: regexp.MustCompile(`(?s)Missing Temps endpoint.*Missing Temps API key`),
		}},
	})
}

func TestUnitProviderReadsEnvironment(t *testing.T) {
	f := newFakeTemps(t)
	t.Setenv(envEndpoint, f.srv.URL)
	t.Setenv(envAPIKey, fakeAPIKey)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories,
		Steps: []resource.TestStep{{
			Config: `data "temps_service_types" "all" {}`,
			Check:  resource.TestCheckResourceAttr("data.temps_service_types.all", "types.#", "3"),
		}},
	})
}

func TestUnitProviderWrongKeyIsUnauthorized(t *testing.T) {
	f := newFakeTemps(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6Factories,
		Steps: []resource.TestStep{{
			Config: `
provider "temps" {
  endpoint = "` + f.srv.URL + `"
  api_key  = "tk_wrong"
}
data "temps_service_types" "all" {}`,
			ExpectError: regexp.MustCompile(`HTTP 401 Unauthorized`),
		}},
	})
}

func TestConvertParameters(t *testing.T) {
	var sch client.ParameterSchema
	_ = json.Unmarshal([]byte(`{"properties":{
		"database":{"type":"string"},"port":{"type":["string","null"]},
		"max_connections":{"type":"integer"},"ratio":{"type":"number"},"tls":{"type":"boolean"},
		"tags":{"type":"array"}}}`), &sch)
	got, err := convertParameters(map[string]string{
		"database": "app", "port": "5432", "max_connections": "50", "ratio": "0.5", "tls": "true", "tags": `["a"]`,
	}, &sch)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"database": "app", "port": "5432", "max_connections": int64(50), "ratio": 0.5, "tls": true, "tags": []any{"a"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
	for in, msg := range map[string]string{
		"max_connections": `must be an integer`, "tls": `must be true or false`, "ratio": `must be a number`, "nope": `is not supported`,
	} {
		_, err := convertParameters(map[string]string{in: "x"}, &sch)
		if err == nil || !regexp.MustCompile(msg).MatchString(err.Error()) {
			t.Errorf("%s: got %v, want %q", in, err, msg)
		}
	}
}

func TestMergeParametersKeepsMaskedAndSensitive(t *testing.T) {
	d := &client.ServiceDetails{
		CurrentParameters:   map[string]any{"password": "***", "max_connections": float64(80), "token": "server-value", "database": "app"},
		SensitiveParameters: []string{"token"},
	}
	got := mergeParameters(map[string]string{"password": "mine", "max_connections": "50", "token": "mine", "database": "app"}, d)
	want := map[string]string{"password": "mine", "max_connections": "80", "token": "mine", "database": "app"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if mergeParameters(nil, d) != nil {
		t.Fatal("unconfigured parameters must stay null")
	}
}

func TestChangedParametersAndReadOnly(t *testing.T) {
	got := changedParameters(map[string]string{"a": "1", "b": "2", "c": "3"}, map[string]string{"a": "1", "b": "9"})
	if !reflect.DeepEqual(got, map[string]string{"b": "2", "c": "3"}) {
		t.Fatalf("changed = %v", got)
	}
	f := false
	ro := readOnlyParameters(&client.ParameterSchema{Properties: map[string]client.ParameterProperty{"region": {Editable: &f}, "port": {}}})
	if !ro["region"] || !ro["password"] || ro["port"] {
		t.Fatalf("read-only = %v", ro)
	}
}

func TestParseCompositeID(t *testing.T) {
	a, b, err := parseCompositeID("12/34", "<a>/<b>")
	if err != nil || a != 12 || b != 34 {
		t.Fatalf("%d %d %v", a, b, err)
	}
	for _, bad := range []string{"12", "12/x", "0/1", "1/2/3", "/"} {
		if _, _, err := parseCompositeID(bad, "<a>/<b>"); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestWaitForTimeoutNamesLastStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := waitFor(ctx, `temps_service "db" (id 3) to become running`, 5*time.Millisecond, func() (bool, string, error) {
		return false, "starting", nil
	})
	if err == nil || !regexp.MustCompile(`timed out waiting for temps_service "db" \(id 3\) to become running; last status "starting"`).MatchString(err.Error()) {
		t.Fatalf("unexpected %v", err)
	}
	// A deadline that expires inside the poll request is still a timeout.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel2()
	err = waitFor(ctx2, "thing", time.Millisecond, func() (bool, string, error) {
		<-ctx2.Done()
		return false, "pending", ctx2.Err()
	})
	if err == nil || !regexp.MustCompile(`timed out waiting for thing; last status "pending"`).MatchString(err.Error()) {
		t.Fatalf("unexpected %v", err)
	}
	want := errors.New("boom")
	if err := waitFor(context.Background(), "x", time.Millisecond, func() (bool, string, error) { return false, "", want }); !errors.Is(err, want) {
		t.Fatalf("unexpected %v", err)
	}
}
