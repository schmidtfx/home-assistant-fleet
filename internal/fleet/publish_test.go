package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/schmidtfx/home-assistant-fleet/internal/hass/hasstest"
	"github.com/schmidtfx/home-assistant-fleet/internal/secrets"
	"github.com/schmidtfx/home-assistant-fleet/internal/store"
	"github.com/schmidtfx/home-assistant-fleet/internal/tasks"
)

func TestStatusEvent(t *testing.T) {
	sync := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	ts := []tasks.Task{
		{Kind: tasks.KindRepair, Severity: tasks.SeverityCritical, Title: "Broken"},
		{Kind: tasks.KindUpdate, Severity: tasks.SeverityWarning, Title: "Core", Ack: &tasks.AckInfo{By: "a"}},
		{Kind: tasks.KindUpdate, Severity: tasks.SeverityInfo, Title: strings.Repeat("x", 300)},
		{Kind: tasks.KindConnectivity, Severity: tasks.SeverityCritical, Title: "Unreachable"},
	}
	v := InstanceView{ID: "i1", Name: "Home", Tasks: ts, Status: Status{State: StateConnected, LastSync: sync}}
	var got struct {
		Schema    int            `json:"schema"`
		Enabled   bool           `json:"enabled"`
		Name      string         `json:"instance_name"`
		LastSync  string         `json:"last_sync"`
		Heartbeat int            `json:"heartbeat_seconds"`
		Warnings  []string       `json:"warnings"`
		Open      map[string]int `json:"open"`
		Kinds     map[string]struct {
			Count       int     `json:"count"`
			MaxSeverity *string `json:"max_severity"`
			Tasks       []struct {
				Title        string `json:"title"`
				Severity     string `json:"severity"`
				Acknowledged bool   `json:"acknowledged"`
			} `json:"tasks"`
		} `json:"kinds"`
	}
	b, _ := json.Marshal(statusEvent(v, 5*time.Minute))
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Schema != 1 || !got.Enabled || got.Name != "Home" || got.LastSync != "2026-10-29T10:00:00Z" ||
		got.Heartbeat != 300 || got.Warnings == nil {
		t.Fatalf("header: %+v", got)
	}
	// Connectivity is never published; acknowledged tasks are listed but not counted.
	want := map[string]int{"total": 2, "critical": 1, "warning": 0, "info": 1, "acknowledged": 1}
	for k, n := range want {
		if got.Open[k] != n {
			t.Errorf("open[%s] = %d, want %d", k, got.Open[k], n)
		}
	}
	if _, ok := got.Kinds["connectivity"]; ok {
		t.Error("connectivity kind published")
	}
	if len(got.Kinds) != len(tasks.Kinds)-1 {
		t.Errorf("got %d kinds, want every kind but connectivity", len(got.Kinds))
	}
	up := got.Kinds["update"]
	if up.Count != 1 || up.MaxSeverity == nil || *up.MaxSeverity != "info" || len(up.Tasks) != 2 ||
		!up.Tasks[0].Acknowledged || len([]rune(up.Tasks[1].Title)) != statusMaxTitleLen {
		t.Errorf("update kind: %+v", up)
	}
	if b := got.Kinds["backup_stale"]; b.Count != 0 || b.MaxSeverity != nil || b.Tasks == nil {
		t.Errorf("empty kind: %+v", b)
	}
}

// statusEvents returns the event_data of every hafleet_status event received.
func statusEvents(ha *hasstest.Server) []map[string]any {
	var out []map[string]any
	for _, c := range ha.Calls() {
		if c["type"] == "fire_event" && c["event_type"] == "hafleet_status" {
			out = append(out, c["event_data"].(map[string]any))
		}
	}
	return out
}

func waitEvents(t *testing.T, ha *hasstest.Server, what string, cond func([]map[string]any) bool) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ev := statusEvents(ha); cond(ev) {
			return ev
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; events: %v", what, statusEvents(ha))
	return nil
}

func updateCount(ev map[string]any) float64 {
	return ev["kinds"].(map[string]any)["update"].(map[string]any)["count"].(float64)
}

func newPublishingManager(t *testing.T, haURL string, heartbeat time.Duration) (*Manager, *store.Store, context.CancelFunc) {
	t.Helper()
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	box, _ := secrets.New(bytes.Repeat([]byte{1}, 32))
	enc, _ := box.Seal([]byte("tok"), []byte("i1"))
	err = st.CreateInstance(context.Background(), &store.Instance{
		ID: "i1", Name: "Home", URL: haURL, TokenEnc: enc, AllowInsecureHTTP: true, Enabled: true, PublishStatus: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	m := New(st, box, Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), PingInterval: time.Hour,
		StatusHeartbeat: heartbeat, StatusDebounce: 10 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return m, st, cancel
}

func TestPublishStatus(t *testing.T) {
	ha := fakeHA()
	ha.Result("auth/current_user", map[string]any{"id": "u-hafleet", "is_admin": true})
	ha.Handle("fire_event", nil)
	defer ha.Close()
	m, st, cancel := newPublishingManager(t, ha.URL, time.Hour)
	defer cancel()

	ev := waitEvents(t, ha, "first status event", func(ev []map[string]any) bool { return len(ev) > 0 })
	if ev[0]["enabled"] != true || ev[0]["instance_name"] != "Home" || updateCount(ev[0]) != 1 {
		t.Fatalf("first event: %v", ev[0])
	}
	if v, _ := m.View("i1"); v.Status.UserID != "u-hafleet" || !v.PublishStatus {
		t.Fatalf("view: %+v", v)
	}

	// No change, no event (the heartbeat is an hour).
	n := len(ev)
	time.Sleep(200 * time.Millisecond)
	if got := len(statusEvents(ha)); got != n {
		t.Fatalf("unchanged view sent %d more events", got-n)
	}

	// Acknowledging changes the counts and republishes.
	v, _ := m.View("i1")
	if _, err := m.Acknowledge(context.Background(), "i1", v.Tasks[0].ID, "admin", "", 0); err != nil {
		t.Fatal(err)
	}
	waitEvents(t, ha, "event after ack", func(ev []map[string]any) bool {
		return len(ev) > n && updateCount(ev[len(ev)-1]) == 0
	})

	// A reconnect republishes the same data (HA may have restarted).
	n = len(statusEvents(ha))
	ha.DropConnections()
	waitEvents(t, ha, "event after reconnect", func(ev []map[string]any) bool { return len(ev) > n })

	// Turning publishing off sends the disabled event, then nothing more.
	in, _ := st.GetInstance(context.Background(), "i1")
	in.PublishStatus = false
	if err := st.UpdateInstance(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if err := m.Reload(context.Background(), "i1"); err != nil {
		t.Fatal(err)
	}
	ev = waitEvents(t, ha, "disabled event", func(ev []map[string]any) bool { return ev[len(ev)-1]["enabled"] == false })
	n = len(ev)
	waitFor(t, m, "resync without publishing", func(v InstanceView) bool { return !v.Status.LastSync.IsZero() && !v.PublishStatus })
	time.Sleep(100 * time.Millisecond)
	if got := len(statusEvents(ha)); got != n {
		t.Fatalf("%d events after publishing was turned off", got-n)
	}
}

func TestPublishStatusHeartbeat(t *testing.T) {
	ha := fakeHA()
	ha.Handle("fire_event", nil)
	defer ha.Close()
	_, _, cancel := newPublishingManager(t, ha.URL, 50*time.Millisecond)
	defer cancel()
	waitEvents(t, ha, "heartbeat events", func(ev []map[string]any) bool { return len(ev) >= 3 })
}

func TestPublishStatusNeedsAdmin(t *testing.T) {
	ha := fakeHA()
	ha.Result("auth/current_user", map[string]any{"id": "u1", "is_admin": false})
	ha.Handle("fire_event", nil)
	defer ha.Close()
	m, _, cancel := newPublishingManager(t, ha.URL, time.Hour)
	defer cancel()

	v := waitFor(t, m, "synced", func(v InstanceView) bool { return !v.Status.LastSync.IsZero() })
	found := false
	for _, w := range v.Status.Warnings {
		found = found || strings.Contains(w, "publishing status")
	}
	if !found {
		t.Errorf("no publishing warning in %v", v.Status.Warnings)
	}
	time.Sleep(100 * time.Millisecond)
	if ev := statusEvents(ha); len(ev) != 0 {
		t.Fatalf("non-admin token published %d events", len(ev))
	}
}
