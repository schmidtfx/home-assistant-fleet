package fleet

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/schmidtfx/home-assistant-fleet/internal/hass/hasstest"
	"github.com/schmidtfx/home-assistant-fleet/internal/secrets"
	"github.com/schmidtfx/home-assistant-fleet/internal/store"
	"github.com/schmidtfx/home-assistant-fleet/internal/tasks"
)

func updateState(entity string, on bool) map[string]any {
	state := "off"
	if on {
		state = "on"
	}
	return map[string]any{"entity_id": entity, "state": state, "attributes": map[string]any{
		"title": "Demo", "installed_version": "1.0", "latest_version": "1.1", "supported_features": 1,
	}}
}

func fakeHA() *hasstest.Server {
	s := hasstest.New("tok")
	s.Result("get_config", map[string]any{"version": "2026.10.1", "location_name": "Home", "components": []string{"backup"}})
	s.Result("auth/current_user", map[string]any{"is_admin": true})
	s.Result("get_states", []any{updateState("update.demo", true)})
	s.Result("repairs/list_issues", map[string]any{"issues": []any{}})
	s.Result("config_entries/get", []any{})
	s.Result("config_entries/flow/progress", []any{})
	s.Result("config/entity_registry/list", []any{})
	s.Result("system_log/list", []any{})
	s.Result("backup/info", map[string]any{"backups": []any{map[string]any{"date": time.Now().UTC().Format(time.RFC3339)}}})
	return s
}

func newManager(t *testing.T, haURL string) (*Manager, context.CancelFunc) {
	t.Helper()
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	box, _ := secrets.New(bytes.Repeat([]byte{1}, 32))
	enc, _ := box.Seal([]byte("tok"), []byte("i1"))
	err = st.CreateInstance(context.Background(), &store.Instance{
		ID: "i1", Name: "Home", URL: haURL, TokenEnc: enc, AllowInsecureHTTP: true, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	m := New(st, box, Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), PingInterval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return m, cancel
}

func waitFor(t *testing.T, m *Manager, what string, cond func(InstanceView) bool) InstanceView {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if v, ok := m.View("i1"); ok && cond(v) {
			return v
		}
		time.Sleep(20 * time.Millisecond)
	}
	v, _ := m.View("i1")
	t.Fatalf("timed out waiting for %s; view: %+v", what, v)
	return v
}

func TestManagerLifecycle(t *testing.T) {
	ha := fakeHA()
	defer ha.Close()
	m, cancel := newManager(t, ha.URL)
	defer cancel()

	v := waitFor(t, m, "connected with update task", func(v InstanceView) bool {
		return v.Status.State == StateConnected && len(v.Tasks) == 1
	})
	if v.Tasks[0].Kind != tasks.KindUpdate || v.Status.HAVersion != "2026.10.1" || !v.Status.IsAdmin {
		t.Fatalf("unexpected view: %+v", v)
	}

	// Live event: the update was installed.
	ha.FireEvent(map[string]any{"event_type": "state_changed", "data": map[string]any{
		"entity_id": "update.demo", "new_state": updateState("update.demo", false)}})
	waitFor(t, m, "update task removed", func(v InstanceView) bool { return len(v.Tasks) == 0 })

	// Connection drop: reconnects and resyncs (get_states still reports the update).
	ha.DropConnections()
	waitFor(t, m, "reconnected", func(v InstanceView) bool {
		return v.Status.State == StateConnected && len(v.Tasks) == 1
	})
}

func TestManagerUnreachable(t *testing.T) {
	ha := fakeHA()
	url := ha.URL
	ha.Close()
	m, cancel := newManager(t, url)
	defer cancel()
	v := waitFor(t, m, "error state", func(v InstanceView) bool { return v.Status.State == StateError })
	if len(v.Tasks) != 1 || v.Tasks[0].Kind != tasks.KindConnectivity {
		t.Fatalf("expected connectivity task, got %+v", v.Tasks)
	}
}

func TestManagerSubscribeAndRemove(t *testing.T) {
	ha := fakeHA()
	defer ha.Close()
	m, cancel := newManager(t, ha.URL)
	defer cancel()
	ch, unsub := m.Subscribe()
	defer unsub()
	waitFor(t, m, "connected", func(v InstanceView) bool { return v.Status.State == StateConnected })
	m.Remove("i1")
	timeout := time.After(2 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Type == "instance_removed" && ev.ID == "i1" {
				if len(m.Views()) != 0 {
					t.Fatal("view not removed")
				}
				return
			}
		case <-timeout:
			t.Fatal("no instance_removed event")
		}
	}
}

func TestSupervisorInfoAndAddons(t *testing.T) {
	ha := fakeHA()
	defer ha.Close()
	ha.Result("get_config", map[string]any{"version": "2026.10.1", "components": []string{"hassio"}})
	var mu sync.Mutex
	failAddons := false
	ha.Handle("supervisor/api", func(cmd map[string]any) (any, string) {
		mu.Lock()
		defer mu.Unlock()
		switch cmd["endpoint"] {
		case "/info":
			return map[string]any{"supervisor": "2026.10.0", "hassos": "16.2", "machine": "rpi5", "hostname": "secret-host"}, ""
		case "/addons":
			if failAddons {
				return nil, "unknown_error"
			}
			return map[string]any{"addons": []any{
				map[string]any{"slug": "core_samba", "name": "Samba", "version": "12.3"},
				map[string]any{"slug": "core_mosquitto", "name": "Mosquitto", "version": "6.5"},
			}}, ""
		case "/resolution/info":
			return map[string]any{}, ""
		case "/host/info":
			return map[string]any{"disk_total": 32.0, "disk_free": 20.0, "disk_used": 12.0}, ""
		}
		return nil, "not_found"
	})
	m, cancel := newManager(t, ha.URL)
	defer cancel()

	v := waitFor(t, m, "add-ons", func(v InstanceView) bool { return len(v.Status.Addons) == 2 })
	st := v.Status
	if st.SupervisorVersion != "2026.10.0" || st.OSVersion != "16.2" || st.Machine != "rpi5" || st.Addons[0].Slug != "core_mosquitto" {
		t.Fatalf("status: %+v", st)
	}

	// A failed fetch keeps the last known add-ons and adds a warning.
	mu.Lock()
	failAddons = true
	mu.Unlock()
	m.Resync("i1")
	v = waitFor(t, m, "add-ons warning", func(v InstanceView) bool { return len(v.Status.Warnings) == 1 })
	if len(v.Status.Addons) != 2 {
		t.Fatalf("add-ons dropped after a failed fetch: %+v", v.Status)
	}
}

func TestHealthTasks(t *testing.T) {
	ha := fakeHA()
	defer ha.Close()
	ha.Result("get_config", map[string]any{"version": "2026.10.1", "components": []string{"hassio"}})
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
	now := float64(time.Now().Unix())
	ha.Result("system_log/list", []any{map[string]any{"name": "homeassistant.components.zha", "message": []string{"boom"},
		"level": "ERROR", "timestamp": now, "first_occurred": now - 60, "count": 7}})
	ha.Handle("supervisor/api", func(cmd map[string]any) (any, string) {
		switch cmd["endpoint"] {
		case "/host/info":
			return map[string]any{"disk_total": 32.0, "disk_free": 1.0}, ""
		case "/info", "/resolution/info":
			return map[string]any{}, ""
		case "/addons":
			return map[string]any{"addons": []any{}}, ""
		}
		return nil, "not_found"
	})
	m, cancel := newManager(t, ha.URL)
	defer cancel()

	v := waitFor(t, m, "health tasks", func(v InstanceView) bool { return len(v.Tasks) >= 4 })
	kinds := map[tasks.Kind]tasks.Severity{}
	for _, task := range v.Tasks {
		kinds[task.Kind] = task.Severity
	}
	want := map[tasks.Kind]tasks.Severity{
		tasks.KindDiskLow: tasks.SeverityCritical, tasks.KindBatteryLow: tasks.SeverityInfo,
		tasks.KindEntitiesUnavailable: tasks.SeverityInfo, tasks.KindLogErrors: tasks.SeverityInfo,
	}
	for k, sev := range want {
		if kinds[k] != sev {
			t.Errorf("%s: got %q, want %q (tasks: %v)", k, kinds[k], sev, kinds)
		}
	}
	if len(v.Status.Warnings) != 0 {
		t.Errorf("warnings: %v", v.Status.Warnings)
	}
}
