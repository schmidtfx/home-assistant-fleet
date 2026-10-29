package fleet

import (
	"context"
	"testing"
	"time"

	"github.com/schmidtfx/home-assistant-fleet/internal/hass/hasstest"
	"github.com/schmidtfx/home-assistant-fleet/internal/tasks"
)

func countCalls(ha *hasstest.Server, typ string) int {
	n := 0
	for _, c := range ha.Calls() {
		if c["type"] == typ {
			n++
		}
	}
	return n
}

func hasKind(v InstanceView, k tasks.Kind) bool {
	for _, t := range v.Tasks {
		if t.Kind == k {
			return true
		}
	}
	return false
}

func TestSettingsSwitchChecks(t *testing.T) {
	ha := fakeHA()
	defer ha.Close()
	old := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	unavailable := func(id string) map[string]any {
		return map[string]any{"entity_id": id, "state": "unavailable", "last_changed": old, "attributes": map[string]any{}}
	}
	ha.Result("get_states", []any{
		map[string]any{"entity_id": "sensor.door_battery", "state": "5", "last_changed": old,
			"attributes": map[string]any{"device_class": "battery", "unit_of_measurement": "%"}},
		unavailable("light.a"), unavailable("light.b"), unavailable("light.c"),
	})
	ha.Result("config/entity_registry/list", []any{
		map[string]any{"entity_id": "light.a", "platform": "zha"},
		map[string]any{"entity_id": "light.b", "platform": "zha"},
		map[string]any{"entity_id": "light.c", "platform": "zha"},
	})
	m, cancel := newManager(t, ha.URL)
	defer cancel()
	ctx := context.Background()

	waitFor(t, m, "health tasks", func(v InstanceView) bool {
		return hasKind(v, tasks.KindEntitiesUnavailable) && hasKind(v, tasks.KindBatteryLow)
	})

	// Fleet-wide: switch off unavailable entities, lower the battery threshold below 5%.
	off, battery := false, 3.0
	if err := m.SetSettings(ctx, tasks.Settings{Checks: map[tasks.Kind]bool{tasks.KindEntitiesUnavailable: off}, BatteryMin: &battery}); err != nil {
		t.Fatal(err)
	}
	v := waitFor(t, m, "tasks gone", func(v InstanceView) bool {
		return !hasKind(v, tasks.KindEntitiesUnavailable) && !hasKind(v, tasks.KindBatteryLow)
	})

	// The entity registry is no longer queried.
	before := countCalls(ha, "config/entity_registry/list")
	m.Resync("i1")
	waitFor(t, m, "resync", func(nv InstanceView) bool { return nv.Status.LastSync.After(v.Status.LastSync) })
	if after := countCalls(ha, "config/entity_registry/list"); after != before {
		t.Errorf("entity registry fetched although the check is off (%d -> %d calls)", before, after)
	}

	// History entries of switched-off checks are closed.
	open, err := m.store.OpenHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range open {
		if e.Kind == string(tasks.KindEntitiesUnavailable) || e.Kind == string(tasks.KindBatteryLow) {
			t.Errorf("history entry still open: %+v", e)
		}
	}

	// Invalid settings are rejected and not applied.
	if err := m.SetSettings(ctx, tasks.Settings{Checks: map[tasks.Kind]bool{tasks.KindUpdate: false}}); err == nil {
		t.Error("switching off updates should fail")
	}
	if m.Settings().BatteryMin == nil {
		t.Error("rejected settings replaced the stored ones")
	}

	// Instance override turns the check back on for this instance only.
	in, _ := m.store.GetInstance(ctx, "i1")
	in.Settings = `{"checks":{"entities_unavailable":true}}`
	if err := m.store.UpdateInstance(ctx, in); err != nil {
		t.Fatal(err)
	}
	if err := m.Reload(ctx, "i1"); err != nil {
		t.Fatal(err)
	}
	v = waitFor(t, m, "override applied", func(v InstanceView) bool { return hasKind(v, tasks.KindEntitiesUnavailable) })
	if !v.Settings.Checks[tasks.KindEntitiesUnavailable] || hasKind(v, tasks.KindBatteryLow) {
		t.Errorf("view: settings %+v, tasks %+v", v.Settings, v.Tasks)
	}
}
