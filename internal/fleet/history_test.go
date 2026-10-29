package fleet

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/schmidtfx/home-assistant-fleet/internal/tasks"
)

// openKinds returns the kinds of the open history entries.
func openKinds(t *testing.T, m *Manager) map[tasks.Kind]int {
	t.Helper()
	list, err := m.store.OpenHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := map[tasks.Kind]int{}
	for _, e := range list {
		out[tasks.Kind(e.Kind)]++
	}
	return out
}

func waitHistory(t *testing.T, m *Manager, what string, cond func(map[tasks.Kind]int) bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond(openKinds(t, m)) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; open: %v", what, openKinds(t, m))
}

func TestHistory(t *testing.T) {
	ha := fakeHA()
	defer ha.Close()
	var mu sync.Mutex
	repairs := []any{map[string]any{"domain": "zha", "issue_id": "x", "severity": "warning", "translation_key": "x"}}
	failRepairs := false
	setRepairs := func(fail bool, issues []any) {
		mu.Lock()
		failRepairs, repairs = fail, issues
		mu.Unlock()
	}
	ha.Handle("repairs/list_issues", func(map[string]any) (any, string) {
		mu.Lock()
		defer mu.Unlock()
		if failRepairs {
			return nil, "unknown_error"
		}
		return map[string]any{"issues": repairs}, ""
	})
	m, cancel := newManager(t, ha.URL)
	defer cancel()

	waitHistory(t, m, "update and repair open", func(k map[tasks.Kind]int) bool {
		return k[tasks.KindUpdate] == 1 && k[tasks.KindRepair] == 1
	})

	// The repairs source fails: its task disappears from the view, but it did not resolve.
	setRepairs(true, repairs)
	m.Resync("i1")
	waitFor(t, m, "repairs warning", func(v InstanceView) bool { return len(v.Status.Warnings) == 1 })
	if k := openKinds(t, m); k[tasks.KindRepair] != 1 {
		t.Fatalf("repair closed although its source failed: %v", k)
	}

	// A live event resolves the update even while another source is failing.
	ha.FireEvent(map[string]any{"event_type": "state_changed", "data": map[string]any{
		"entity_id": "update.demo", "new_state": updateState("update.demo", false)}})
	waitHistory(t, m, "update closed", func(k map[tasks.Kind]int) bool { return k[tasks.KindUpdate] == 0 })

	// The source works again and the issue is gone: now it resolved.
	setRepairs(false, []any{})
	m.Resync("i1")
	waitHistory(t, m, "repair closed", func(k map[tasks.Kind]int) bool { return k[tasks.KindRepair] == 0 })

	// Unreachable: the outage is recorded, nothing else is closed. (The resync
	// also reopens the update: the fake's get_states still reports it.)
	setRepairs(false, []any{map[string]any{"domain": "zha", "issue_id": "y", "severity": "warning", "translation_key": "y"}})
	m.Resync("i1")
	waitHistory(t, m, "second repair open", func(k map[tasks.Kind]int) bool { return k[tasks.KindRepair] == 1 })
	ha.DropConnections()
	ha.Close()
	waitHistory(t, m, "outage open", func(k map[tasks.Kind]int) bool {
		return k[tasks.KindConnectivity] == 1 && k[tasks.KindRepair] == 1 && k[tasks.KindUpdate] == 1
	})

	sum, err := m.History(context.Background(), 7, "")
	if err != nil {
		t.Fatal(err)
	}
	today := sum.Days[len(sum.Days)-1]
	// Opened: update, repair x, update again, repair y, outage. Closed: update, repair x.
	if len(sum.Days) != 7 || today.Opened != 5 || today.Closed != 2 || today.Open != 3 {
		t.Fatalf("summary: %+v", today)
	}
}
