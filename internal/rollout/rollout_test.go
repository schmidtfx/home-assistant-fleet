package rollout

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/schmidtfx/home-assistant-fleet/internal/actions"
	"github.com/schmidtfx/home-assistant-fleet/internal/fleet"
	"github.com/schmidtfx/home-assistant-fleet/internal/hass/hasstest"
	"github.com/schmidtfx/home-assistant-fleet/internal/secrets"
	"github.com/schmidtfx/home-assistant-fleet/internal/store"
)

const entity = "update.mosquitto_broker_update"

// fakeInstance is a Home Assistant whose update is installed by update.install.
type fakeInstance struct {
	*hasstest.Server
	mu        sync.Mutex
	installed string
	// breakOnInstall makes a critical repair appear after the install.
	breakOnInstall bool
	broken         bool
}

func newFake(t *testing.T) *fakeInstance {
	f := &fakeInstance{Server: hasstest.New("tok"), installed: "6.4.0"}
	t.Cleanup(f.Close)
	f.Result("get_config", map[string]any{"version": "2026.10.1", "components": []string{}})
	f.Result("auth/current_user", map[string]any{"is_admin": true})
	f.Result("config_entries/get", []any{})
	f.Result("config_entries/flow/progress", []any{})
	f.Result("config/entity_registry/list", []any{})
	f.Result("system_log/list", []any{})
	f.Handle("get_states", func(map[string]any) (any, string) { return []any{f.state()}, "" })
	f.Handle("repairs/list_issues", func(map[string]any) (any, string) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !f.broken {
			return map[string]any{"issues": []any{}}, ""
		}
		return map[string]any{"issues": []any{map[string]any{"domain": "mqtt", "issue_id": "broken",
			"severity": "critical", "translation_key": "broken"}}}, ""
	})
	f.Handle("call_service", func(cmd map[string]any) (any, string) {
		if cmd["domain"] != "update" || cmd["service"] != "install" {
			return map[string]any{}, ""
		}
		f.mu.Lock()
		f.installed = "6.5.1"
		f.broken = f.breakOnInstall
		f.mu.Unlock()
		f.FireEvent(map[string]any{"event_type": "state_changed", "data": map[string]any{"entity_id": entity, "new_state": f.state()}})
		return map[string]any{}, ""
	})
	return f
}

func (f *fakeInstance) state() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := "on"
	if f.installed == "6.5.1" {
		st = "off"
	}
	return map[string]any{"entity_id": entity, "state": st, "attributes": map[string]any{
		"title": "Mosquitto broker", "installed_version": f.installed, "latest_version": "6.5.1", "supported_features": 1 | 8}}
}

type env struct {
	st     *store.Store
	fleet  *fleet.Manager
	runner *actions.Runner
	rm     *Manager
	ctx    context.Context
	cancel context.CancelFunc
}

// setup starts a fleet with one instance per fake; ids are i0, i1, ...
func setup(t *testing.T, fakes []*fakeInstance, windows ...string) *env {
	t.Helper()
	st, _ := store.OpenMemory()
	t.Cleanup(func() { st.Close() })
	box, _ := secrets.New(bytes.Repeat([]byte{3}, 32))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	for i, f := range fakes {
		id := "i" + string(rune('0'+i))
		enc, _ := box.Seal([]byte("tok"), []byte(id))
		in := &store.Instance{ID: id, Name: "Home " + id, URL: f.URL, TokenEnc: enc, AllowInsecureHTTP: true, Enabled: true}
		if i < len(windows) {
			in.MaintWindow = windows[i]
		}
		if err := st.CreateInstance(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	fm := fleet.New(st, box, fleet.Options{Logger: log, PingInterval: time.Hour})
	if err := fm.Start(ctx); err != nil {
		t.Fatal(err)
	}
	runner := actions.NewRunner(fm, st, log)
	rm := New(st, fm, runner, Options{Poll: 10 * time.Millisecond, GateUnit: 50 * time.Millisecond, VerifyTimeout: 3 * time.Second, Logger: log})
	if err := rm.Start(ctx); err != nil {
		t.Fatal(err)
	}
	e := &env{st: st, fleet: fm, runner: runner, rm: rm, ctx: ctx, cancel: cancel}
	for i := range fakes {
		id := "i" + string(rune('0'+i))
		e.wait(t, "update task on "+id, func() bool { _, r := rm.pendingUpdate(id, entity, "6.5.1"); return r == "" })
	}
	return e
}

func (e *env) wait(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (e *env) waitState(t *testing.T, id, state string) *store.Rollout {
	t.Helper()
	var r *store.Rollout
	e.wait(t, "rollout "+state, func() bool {
		r, _ = e.rm.Get(e.ctx, id)
		return r != nil && r.State == state
	})
	return r
}

func stepStates(r *store.Rollout) string {
	var s []string
	for _, st := range r.Steps {
		s = append(s, st.State)
	}
	return strings.Join(s, ",")
}

func TestRolloutCompletes(t *testing.T) {
	e := setup(t, []*fakeInstance{newFake(t), newFake(t)})
	r, err := e.rm.Create(e.ctx, "alice", CreateRequest{EntityID: entity, TargetVersion: "6.5.1",
		Instances: []string{"i1", "i0"}, CanaryCount: 1, Backup: true, GateMinutes: 1})
	if err != nil {
		t.Fatal(err)
	}
	if r.Title != "Update Mosquitto broker to 6.5.1" || !r.Steps[0].Canary || r.Steps[0].InstanceID != "i1" || r.Steps[1].Canary {
		t.Fatalf("created: %+v", r)
	}
	r = e.waitState(t, r.ID, StateCompleted)
	if got := stepStates(r); got != "done,done" {
		t.Fatalf("steps: %s", got)
	}
	// Installs went through the action runner and its audit log, with the backup option.
	audit, _ := e.st.ListAudit(e.ctx, 50)
	n := 0
	for _, a := range audit {
		if a.Action == "install_update" && a.Username == "rollout:alice" {
			n++
		}
	}
	if n != 4 { // submitted + succeeded, twice
		t.Fatalf("audit entries: %d (%+v)", n, audit)
	}
	stored, _ := e.st.GetRollout(e.ctx, r.ID)
	if stored.State != StateCompleted || stepStates(stored) != "done,done" {
		t.Fatalf("not persisted: %+v", stored)
	}
}

func TestCanaryFailurePauses(t *testing.T) {
	canary, other := newFake(t), newFake(t)
	canary.breakOnInstall = true
	e := setup(t, []*fakeInstance{canary, other})
	r, err := e.rm.Create(e.ctx, "alice", CreateRequest{EntityID: entity, TargetVersion: "6.5.1",
		Instances: []string{"i0", "i1"}, CanaryCount: 1, GateMinutes: 2})
	if err != nil {
		t.Fatal(err)
	}
	r = e.waitState(t, r.ID, StatePaused)
	if got := stepStates(r); got != "failed,pending" || !strings.Contains(r.Steps[0].Message, "new critical task") {
		t.Fatalf("after canary failure: %s %+v", got, r.Steps[0])
	}
	// The second instance was never touched.
	for _, c := range other.Calls() {
		if c["type"] == "call_service" {
			t.Fatal("rollout continued past a failed canary")
		}
	}
	if _, err := e.rm.Resume(e.ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("resume unknown: %v", err)
	}

	// Skip the canary and resume: the rest runs.
	if _, err := e.rm.Skip(e.ctx, r.ID, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := e.rm.Resume(e.ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	r = e.waitState(t, r.ID, StateCompleted)
	if got := stepStates(r); got != "skipped,done" {
		t.Fatalf("after skip: %s", got)
	}
	if _, err := e.rm.Pause(e.ctx, r.ID); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("pause finished rollout: %v", err)
	}
}

func TestCreateValidation(t *testing.T) {
	e := setup(t, []*fakeInstance{newFake(t), newFake(t)})
	_, err := e.rm.Create(e.ctx, "alice", CreateRequest{EntityID: entity, TargetVersion: "9.9.9", Instances: []string{"i0"}})
	var ve *ValidationError
	if !errors.As(err, &ve) || !strings.Contains(ve.Instances["i0"], "offers version 6.5.1") {
		t.Fatalf("wrong version: %v %+v", err, ve)
	}
	if _, err := e.rm.Create(e.ctx, "alice", CreateRequest{EntityID: entity, TargetVersion: "6.5.1",
		Instances: []string{"i0", "i0"}}); !errors.As(err, &ve) {
		t.Fatalf("duplicate instance: %v", err)
	}
	if _, err := e.rm.Create(e.ctx, "alice", CreateRequest{EntityID: entity, TargetVersion: "6.5.1",
		Instances: []string{"i0"}, CanaryCount: 2}); !errors.As(err, &ve) {
		t.Fatalf("too many canaries: %v", err)
	}
	// i0 is busy in a rollout that waits for its start time.
	later := time.Now().Add(time.Hour)
	if _, err := e.rm.Create(e.ctx, "alice", CreateRequest{EntityID: entity, TargetVersion: "6.5.1",
		Instances: []string{"i0"}, StartAt: &later}); err != nil {
		t.Fatal(err)
	}
	_, err = e.rm.Create(e.ctx, "bob", CreateRequest{EntityID: entity, TargetVersion: "6.5.1", Instances: []string{"i0", "i1"}})
	if !errors.As(err, &ve) || ve.Instances["i0"] == "" || ve.Instances["i1"] != "" {
		t.Fatalf("busy instance: %v %+v", err, ve)
	}
}

func TestMaintenanceWindowAndPause(t *testing.T) {
	// A one-minute window that is not now.
	now := time.Now()
	from := now.Add(2 * time.Hour)
	win := from.Format("15:04") + "-" + from.Add(time.Minute).Format("15:04")
	f := newFake(t)
	e := setup(t, []*fakeInstance{f}, win)
	r, err := e.rm.Create(e.ctx, "alice", CreateRequest{EntityID: entity, TargetVersion: "6.5.1", Instances: []string{"i0"}})
	if err != nil {
		t.Fatal(err)
	}
	e.wait(t, "step waiting", func() bool {
		r, _ = e.rm.Get(e.ctx, r.ID)
		return r.Steps[0].State == StepWaiting
	})
	if !strings.Contains(r.Steps[0].Message, win) {
		t.Fatalf("message: %q", r.Steps[0].Message)
	}
	if _, err := e.rm.Pause(e.ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	e.wait(t, "step back to pending", func() bool {
		r, _ = e.rm.Get(e.ctx, r.ID)
		return r.State == StatePaused && r.Steps[0].State == StepPending
	})
	if _, err := e.rm.Cancel(e.ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	r, _ = e.rm.Get(e.ctx, r.ID)
	if r.State != StateCancelled || r.Steps[0].State != StepSkipped {
		t.Fatalf("cancelled: %+v", r)
	}
	for _, c := range f.Calls() {
		if c["type"] == "call_service" {
			t.Fatal("installed outside the maintenance window")
		}
	}
}

func TestRestartRecovery(t *testing.T) {
	e := setup(t, []*fakeInstance{newFake(t), newFake(t)})
	started := time.Now()
	r := &store.Rollout{ID: "r1", CreatedBy: "alice", EntityID: entity, Title: "x", TargetVersion: "6.5.1",
		State: StateRunning, CreatedAt: started, UpdatedAt: started, Steps: []store.RolloutStep{
			{Ord: 0, InstanceID: "i0", State: StepInstalling}, {Ord: 1, InstanceID: "i1", State: StepPending}}}
	if err := e.st.CreateRollout(e.ctx, r); err != nil {
		t.Fatal(err)
	}
	r.Steps[0].StartedAt = &started
	if err := e.st.SaveRollout(e.ctx, r); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	rm := New(e.st, e.fleet, e.runner, Options{Poll: 10 * time.Millisecond, Logger: log})
	if err := rm.Start(e.ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := rm.Get(e.ctx, "r1")
	if got.State != StatePaused || stepStates(got) != "failed,pending" || !strings.Contains(got.Steps[0].Message, "restarted") {
		t.Fatalf("recovered: %+v", got)
	}
}

func TestWindow(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 10, 29, h, m, 0, 0, time.Local) }
	w, err := ParseWindow("02:00-05:00")
	if err != nil || !w.Contains(at(2, 0)) || !w.Contains(at(4, 59)) || w.Contains(at(5, 0)) || w.Contains(at(1, 59)) {
		t.Fatalf("02:00-05:00: %v", err)
	}
	w, _ = ParseWindow("23:00-01:30")
	if !w.Contains(at(23, 30)) || !w.Contains(at(0, 10)) || w.Contains(at(12, 0)) || w.String() != "23:00-01:30" {
		t.Fatal("wrapping window")
	}
	if w, err := ParseWindow(""); err != nil || !w.Contains(at(12, 0)) {
		t.Fatal("empty window allows any time")
	}
	for _, bad := range []string{"2-5", "02:00", "25:00-01:00", "03:00-03:00"} {
		if _, err := ParseWindow(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestAIGate(t *testing.T) {
	e := setup(t, []*fakeInstance{newFake(t), newFake(t)})
	req := CreateRequest{EntityID: entity, TargetVersion: "6.5.1", Instances: []string{"i0", "i1"}, CanaryCount: 1, AIGate: true}
	var ve *ValidationError
	if _, err := e.rm.Create(e.ctx, "alice", req); !errors.As(err, &ve) {
		t.Fatalf("AI gate without AI: %v", err)
	}

	var mu sync.Mutex
	var checked []actions.Record
	e.rm.opts.HealthCheck = func(_ context.Context, rec actions.Record) (string, []string, error) {
		mu.Lock()
		defer mu.Unlock()
		checked = append(checked, rec)
		if rec.InstanceID == "i1" {
			return VerdictSkipped, nil, nil
		}
		return "suspect", []string{"MQTT errors started after the update"}, nil
	}
	r, err := e.rm.Create(e.ctx, "alice", req)
	if err != nil {
		t.Fatal(err)
	}
	r = e.waitState(t, r.ID, StatePaused)
	if got := stepStates(r); got != "failed,pending" || r.Steps[0].Message != "AI health check: suspect: MQTT errors started after the update" {
		t.Fatalf("suspect canary: %s %q", got, r.Steps[0].Message)
	}
	mu.Lock()
	if len(checked) != 1 || checked[0].Status != actions.StatusSucceeded || checked[0].ToVersion != "6.5.1" || checked[0].FromVersion != "6.4.0" {
		t.Fatalf("checked: %+v", checked)
	}
	mu.Unlock()

	// A user decides: skip the canary; the second instance has AI off, so its check is skipped.
	e.rm.Skip(e.ctx, r.ID, 0)
	e.rm.Resume(e.ctx, r.ID)
	r = e.waitState(t, r.ID, StateCompleted)
	if r.Steps[1].Message != "AI health check skipped: AI is not enabled for this instance" || !r.AIGate {
		t.Fatalf("second step: %+v", r)
	}
}
