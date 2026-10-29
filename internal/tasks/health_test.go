package tasks

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/schmidtfx/home-assistant-fleet/internal/hass"
)

var healthOpts = HealthOptions{DiskFreeMinPercent: 15, BatteryMinPercent: 15}

func TestHostInfo(t *testing.T) {
	var h hass.HostInfo
	load(t, "host_info.json", &h)
	got := FromHostInfo("i1", &h, healthOpts) // 3.4 of 28.6 GB = 11.9%
	if len(got) != 1 || got[0].Severity != SeverityWarning || got[0].Source["free_percent"] != 11.9 {
		t.Fatalf("disk: %+v", got)
	}
	h.DiskFree = 1.5 // under 2 GB
	if got := FromHostInfo("i1", &h, healthOpts); got[0].Severity != SeverityCritical {
		t.Fatalf("critical disk: %+v", got)
	}
	h.DiskFree = 10
	if got := FromHostInfo("i1", &h, healthOpts); got != nil {
		t.Fatalf("enough space: %+v", got)
	}
	if got := FromHostInfo("i1", &hass.HostInfo{}, healthOpts); got != nil {
		t.Fatalf("no data: %+v", got)
	}
}

func TestCertExpiry(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		in    time.Duration
		sev   Severity
		title string
	}{
		{30 * 24 * time.Hour, "", ""},
		{20 * 24 * time.Hour, SeverityWarning, "TLS certificate expires soon"},
		{3 * 24 * time.Hour, SeverityCritical, "TLS certificate expires soon"},
		{-time.Hour, SeverityCritical, "TLS certificate expired"},
	} {
		got := FromCertExpiry("i1", now.Add(tc.in), now)
		if tc.sev == "" {
			if got != nil {
				t.Errorf("%v: unexpected %+v", tc.in, got)
			}
			continue
		}
		if len(got) != 1 || got[0].Severity != tc.sev || got[0].Title != tc.title {
			t.Errorf("%v: %+v", tc.in, got)
		}
	}
	if FromCertExpiry("i1", time.Time{}, now) != nil {
		t.Error("plain http must not create a task")
	}
}

func TestUnavailableEntities(t *testing.T) {
	var states []hass.State
	var reg []hass.RegistryEntry
	load(t, "health_states.json", &states)
	load(t, "entity_registry.json", &reg)
	got := FromUnavailableEntities("i1", states, reg, time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC))
	// zha: 1, 2, 3 and the garage battery. Not: the recent one, the disabled one. hue has only 2.
	if len(got) != 1 || got[0].Title != "Entities unavailable: zha" || got[0].Source["count"] != 4 {
		t.Fatalf("unavailable: %+v", got)
	}
	ents := got[0].Source["entities"].([]string)
	if !slices.Contains(ents, "sensor.garage_battery") || slices.Contains(ents, "light.zha_recent") {
		t.Fatalf("entities: %v", ents)
	}
}

func TestBatteries(t *testing.T) {
	var states []hass.State
	load(t, "health_states.json", &states)
	got := FromBatteries("i1", states, healthOpts)
	if len(got) != 1 || got[0].Title != "2–5 low batteries" || got[0].Source["count"] != 2 {
		t.Fatalf("batteries: %+v", got)
	}
	// Names stay out of the title and detail (they are sent to AI features).
	if slices.ContainsFunc([]string{got[0].Title, got[0].Detail}, func(s string) bool { return containsAny(s, "kitchen", "front") }) {
		t.Fatalf("entity names in title/detail: %+v", got[0])
	}
}

func TestSystemLog(t *testing.T) {
	var entries []hass.LogEntry
	load(t, "system_log.json", &entries)
	// Window 10:30–11:30 UTC. zha: 4 new + 1 recurring critical = 5. hacs: 2. mqtt: old, or only a warning.
	got := FromSystemLog("i1", entries, time.Date(2026, 10, 29, 11, 30, 0, 0, time.UTC))
	if len(got) != 1 || got[0].Title != "Repeated errors: zha" || got[0].Severity != SeverityWarning || got[0].Source["count"] != 5 {
		t.Fatalf("log: %+v", got)
	}
	if containsAny(got[0].Detail, "192.168") {
		t.Fatalf("log message in detail: %s", got[0].Detail)
	}
	for in, want := range map[string]string{
		"homeassistant.components.zha.core": "zha", "custom_components.hacs.base": "hacs", "homeassistant.core": "homeassistant.core",
	} {
		if d := loggerDomain(in); d != want {
			t.Errorf("loggerDomain(%q) = %q", in, d)
		}
	}
}

func containsAny(s string, subs ...string) bool {
	return slices.ContainsFunc(subs, func(sub string) bool { return strings.Contains(strings.ToLower(s), sub) })
}
