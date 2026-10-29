package fleet

import (
	"testing"
	"time"

	"github.com/schmidtfx/home-assistant-fleet/internal/hass"
)

func TestBuildDrift(t *testing.T) {
	synced := time.Now()
	view := func(id, ha string, addons []hass.Addon, integrations ...string) InstanceView {
		return InstanceView{ID: id, Name: id, Status: Status{
			LastSync: synced, HAVersion: ha, Addons: addons, Integrations: integrations}}
	}
	mqtt := func(v string) hass.Addon { return hass.Addon{Slug: "core_mosquitto", Name: "Mosquitto", Version: v} }
	views := []InstanceView{
		view("a", "2026.10.1", []hass.Addon{mqtt("6.5")}, "hue", "mqtt"),
		view("b", "2026.10.1", []hass.Addon{mqtt("6.4")}, "mqtt"),
		view("c", "2026.9.0", []hass.Addon{mqtt("6.5")}, "mqtt"),
		{ID: "d", Name: "d"}, // never synced: unknown, ignored
	}
	d := BuildDrift(views)
	if len(d.Instances) != 4 || d.Instances[3].Known {
		t.Fatalf("instances: %+v", d.Instances)
	}
	byKey := map[string]DriftRow{}
	for _, r := range d.Rows {
		byKey[r.Category+"/"+r.Key] = r
	}
	if r := byKey["version/core"]; !r.Drift || r.Majority != "2026.10.1" || r.Values["c"] != "2026.9.0" {
		t.Fatalf("core: %+v", r)
	}
	if r := byKey["addon/core_mosquitto"]; !r.Drift || r.Majority != "6.5" || r.Label != "Mosquitto" {
		t.Fatalf("mosquitto: %+v", r)
	}
	if r := byKey["integration/mqtt"]; r.Drift || r.Majority != DriftInstalled {
		t.Fatalf("mqtt everywhere: %+v", r)
	}
	if r := byKey["integration/hue"]; !r.Drift || !r.HasMajority || r.Majority != "" {
		t.Fatalf("hue on one of three: %+v", r)
	}
	// Two instances that differ: no majority, so neither is singled out.
	tie := BuildDrift(views[:2])
	for _, r := range tie.Rows {
		if r.Key == "core_mosquitto" && (!r.Drift || r.HasMajority) {
			t.Fatalf("tie: %+v", r)
		}
	}
	if d.Rows[0].Key != "core" || d.Rows[len(d.Rows)-1].Category != "integration" {
		t.Fatalf("order: %+v", d.Rows)
	}
}
