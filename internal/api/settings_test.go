package api

import (
	"encoding/json"
	"testing"

	"github.com/schmidtfx/home-assistant-fleet/internal/fleet"
)

func TestSettingsEndpoints(t *testing.T) {
	c, ha := setup(t)
	c.login()

	resp, body := c.do("GET", "/api/settings", nil)
	var got settingsResponse
	if resp.StatusCode != 200 || json.Unmarshal(body, &got) != nil {
		t.Fatalf("get: %d %s", resp.StatusCode, body)
	}
	if got.Defaults.DiskFreeMin == 0 || got.Defaults.BackupMaxAgeHours == 0 || len(got.Switchable) == 0 {
		t.Errorf("defaults/switchable missing: %s", body)
	}

	for _, bad := range []map[string]any{
		{"checks": map[string]bool{"update": false}},
		{"battery_min": 120},
		{"unknown_field": 1},
	} {
		if resp, body := c.do("PUT", "/api/settings", bad); resp.StatusCode != 400 {
			t.Errorf("PUT %v: %d %s, want 400", bad, resp.StatusCode, body)
		}
	}

	resp, body = c.do("PUT", "/api/settings", map[string]any{
		"checks": map[string]bool{"entities_unavailable": false}, "battery_min": 20})
	if resp.StatusCode != 200 || json.Unmarshal(body, &got) != nil {
		t.Fatalf("put: %d %s", resp.StatusCode, body)
	}
	if got.Settings.Checks["entities_unavailable"] || got.Settings.BatteryMin == nil || *got.Settings.BatteryMin != 20 {
		t.Errorf("settings not saved: %s", body)
	}
	_, body = c.do("GET", "/api/audit", nil)
	var audit []map[string]any
	json.Unmarshal(body, &audit)
	if len(audit) == 0 || audit[0]["action"] != "settings_update" {
		t.Errorf("audit: %s", body)
	}

	// Per-instance overrides are validated and returned with the instance.
	resp, body = c.do("POST", "/api/instances", map[string]any{
		"name": "Cabin", "url": ha.URL, "token": "ha-token", "allow_insecure_http": true,
		"settings": map[string]any{"checks": map[string]bool{"connectivity": false}}})
	if resp.StatusCode != 400 {
		t.Errorf("invalid instance settings: %d %s", resp.StatusCode, body)
	}
	resp, body = c.do("POST", "/api/instances", map[string]any{
		"name": "Cabin", "url": ha.URL, "token": "ha-token", "allow_insecure_http": true,
		"settings": map[string]any{"checks": map[string]bool{"entities_unavailable": true}}})
	var inst fleet.InstanceView
	if resp.StatusCode != 201 || json.Unmarshal(body, &inst) != nil {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	if !inst.Settings.Checks["entities_unavailable"] {
		t.Errorf("instance settings: %s", body)
	}
}
