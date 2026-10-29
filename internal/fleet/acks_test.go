package fleet

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/schmidtfx/home-assistant-fleet/internal/tasks"
)

func demoUpdate(latest string) map[string]any {
	return map[string]any{"entity_id": "update.demo", "state": "on", "attributes": map[string]any{
		"title": "Demo", "installed_version": "1.0", "latest_version": latest, "supported_features": 1,
	}}
}

func acked(v InstanceView) bool { return len(v.Tasks) == 1 && v.Tasks[0].Ack != nil }

func TestAckLifecycle(t *testing.T) {
	ha := fakeHA()
	defer ha.Close()
	m, cancel := newManager(t, ha.URL)
	defer cancel()
	ctx := context.Background()

	v := waitFor(t, m, "update task", func(v InstanceView) bool { return v.Status.State == StateConnected && len(v.Tasks) == 1 })
	task := v.Tasks[0]
	if _, err := m.Acknowledge(ctx, "i1", "nope", "admin", "", 0); err != ErrTaskNotFound {
		t.Fatalf("expected ErrTaskNotFound, got %v", err)
	}
	if _, err := m.Acknowledge(ctx, "i1", task.ID, "admin", "known issue", 0); err != nil {
		t.Fatal(err)
	}
	v = waitFor(t, m, "acked", acked)
	if v.Tasks[0].Ack.By != "admin" || v.Tasks[0].Ack.Note != "known issue" {
		t.Fatalf("ack info: %+v", v.Tasks[0].Ack)
	}

	// Survives a resync and a restart (persisted).
	m.Resync("i1")
	time.Sleep(200 * time.Millisecond)
	waitFor(t, m, "still acked after resync", acked)
	m2 := New(m.store, m.box, Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), PingInterval: time.Hour})
	ctx2, cancel2 := context.WithCancel(ctx)
	m2.Start(ctx2)
	waitFor(t, m2, "acked after restart", acked)
	cancel2()

	// A failing source does not prune acks of tasks that are only temporarily missing.
	ha.Handle("repairs/list_issues", func(map[string]any) (any, string) { return nil, "unknown_error" })
	ha.Result("get_states", []any{})
	m.Resync("i1")
	waitFor(t, m, "task gone during partial sync", func(v InstanceView) bool { return len(v.Tasks) == 0 })
	ha.Result("repairs/list_issues", map[string]any{"issues": []any{}})
	ha.Result("get_states", []any{demoUpdate("1.1")})
	m.Resync("i1")
	waitFor(t, m, "ack kept after partial sync", acked)

	// A new version (changed fingerprint) brings the task back.
	ha.Result("get_states", []any{demoUpdate("1.2")})
	m.Resync("i1")
	waitFor(t, m, "new version unacked", func(v InstanceView) bool {
		return len(v.Tasks) == 1 && v.Tasks[0].Ack == nil && v.Tasks[0].Source["latest_version"] == "1.2"
	})

	// Resolved (fully successful sync without the task) prunes the ack, so a recurrence is visible.
	v, _ = m.View("i1")
	m.Acknowledge(ctx, "i1", v.Tasks[0].ID, "admin", "", 0)
	waitFor(t, m, "acked again", acked)
	ha.Result("get_states", []any{})
	m.Resync("i1")
	waitFor(t, m, "resolved", func(v InstanceView) bool { return len(v.Tasks) == 0 })
	ha.Result("get_states", []any{demoUpdate("1.2")})
	m.Resync("i1")
	waitFor(t, m, "recurrence visible", func(v InstanceView) bool { return len(v.Tasks) == 1 && v.Tasks[0].Ack == nil })
	if acks, _ := m.store.ListAcks(ctx); len(acks) != 0 {
		t.Fatalf("stale acks left in store: %+v", acks)
	}

	// Snooze expiry and unacknowledge.
	v, _ = m.View("i1")
	id := v.Tasks[0].ID
	m.Acknowledge(ctx, "i1", id, "admin", "", time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	m.refreshAcks("i1") // what expireAcksLoop does every minute
	waitFor(t, m, "snooze expired", func(v InstanceView) bool { return len(v.Tasks) == 1 && v.Tasks[0].Ack == nil })
	m.Acknowledge(ctx, "i1", id, "admin", "", 0)
	waitFor(t, m, "acked", acked)
	if _, err := m.Unacknowledge(ctx, "i1", id); err != nil {
		t.Fatal(err)
	}
	waitFor(t, m, "unacked", func(v InstanceView) bool { return len(v.Tasks) == 1 && v.Tasks[0].Ack == nil })
}

func TestFingerprint(t *testing.T) {
	a := tasks.Task{Severity: tasks.SeverityWarning, Title: "Integration X failed: Setup retry", Detail: "timeout"}
	b := a
	b.Detail = "other error text"
	if tasks.Fingerprint(a) != tasks.Fingerprint(b) {
		t.Error("detail changes must not invalidate an ack")
	}
	b.Title = "Integration X failed: Setup error"
	if tasks.Fingerprint(a) == tasks.Fingerprint(b) {
		t.Error("a different failure state must invalidate an ack")
	}
}
