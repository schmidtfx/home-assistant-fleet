// Package rollout installs one update on several instances in turn: canaries
// first, each followed by a health gate, pausing on the first failure.
//
// Every install goes through actions.Runner.Submit, so the action allowlist
// and the audit log apply exactly as for a single install.
package rollout

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/schmidtfx/home-assistant-fleet/internal/actions"
	"github.com/schmidtfx/home-assistant-fleet/internal/fleet"
	"github.com/schmidtfx/home-assistant-fleet/internal/store"
	"github.com/schmidtfx/home-assistant-fleet/internal/tasks"
)

// Rollout states. A failure pauses a rollout; it never continues on its own.
const (
	StateRunning   = "running"
	StatePaused    = "paused"
	StateCompleted = "completed"
	StateCancelled = "cancelled"
)

// Step states.
const (
	StepPending    = "pending"
	StepWaiting    = "waiting"    // for the start time or the maintenance window
	StepInstalling = "installing" // the install action is running
	StepVerifying  = "verifying"  // waiting for the new version and the health gate
	StepDone       = "done"
	StepFailed     = "failed"
	StepSkipped    = "skipped"
)

const MaxGateMinutes = 24 * 60

var (
	ErrNotFound     = errors.New("rollout not found")
	ErrInvalidState = errors.New("not possible in the rollout's current state")
	errStopped      = errors.New("rollout stopped")
)

// ValidationError lists why instances cannot be part of a rollout.
type ValidationError struct {
	Message   string
	Instances map[string]string // instance id -> reason
}

func (e *ValidationError) Error() string { return e.Message }

type Options struct {
	// Poll is how often waits re-check the fleet (default 15s).
	Poll time.Duration
	// GateUnit is the length of one gate "minute" (tests shorten it).
	GateUnit time.Duration
	// VerifyTimeout bounds the wait for the new version after the install finished (default 45m).
	VerifyTimeout time.Duration
	// HealthCheck runs the AI health check for a finished install (nil when
	// AI is not configured). It returns the verdict (ok, suspect, broken, or
	// skipped when the instance has not opted in to AI) and the findings.
	HealthCheck func(ctx context.Context, rec actions.Record) (verdict string, findings []string, err error)
	Logger      *slog.Logger
}

// Health check verdicts.
const (
	VerdictOK      = "ok"
	VerdictSkipped = "skipped"
)

// Manager runs rollouts. Active rollouts (running or paused) are kept in
// memory and written to the store on every change.
type Manager struct {
	store  *store.Store
	fleet  *fleet.Manager
	runner *actions.Runner
	opts   Options
	log    *slog.Logger

	mu      sync.Mutex
	ctx     context.Context
	active  map[string]*store.Rollout
	running map[string]chan struct{} // executor wake-up channel by rollout id
}

func New(s *store.Store, f *fleet.Manager, r *actions.Runner, opts Options) *Manager {
	if opts.Poll == 0 {
		opts.Poll = 15 * time.Second
	}
	if opts.GateUnit == 0 {
		opts.GateUnit = time.Minute
	}
	if opts.VerifyTimeout == 0 {
		opts.VerifyTimeout = 45 * time.Minute
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Manager{store: s, fleet: f, runner: r, opts: opts, log: opts.Logger,
		active: map[string]*store.Rollout{}, running: map[string]chan struct{}{}}
}

// Start loads active rollouts and resumes running ones. A step that was
// installing or verifying when hafleet stopped is marked failed and its
// rollout paused: nobody watched it finish.
func (m *Manager) Start(ctx context.Context) error {
	list, err := m.store.RolloutsInState(ctx, StateRunning, StatePaused)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.ctx = ctx
	m.mu.Unlock()
	for _, r := range list {
		interrupted := false
		for i := range r.Steps {
			switch r.Steps[i].State {
			case StepInstalling, StepVerifying:
				r.Steps[i].State = StepFailed
				r.Steps[i].Message = "hafleet restarted during this step; check the instance, then resume or skip"
				r.Steps[i].FinishedAt = ptr(time.Now())
				interrupted = true
			case StepWaiting:
				r.Steps[i].State = StepPending
			}
		}
		if interrupted {
			r.State, r.Message = StatePaused, "Paused: hafleet restarted during a step"
		}
		r.UpdatedAt = time.Now()
		if err := m.store.SaveRollout(ctx, r); err != nil {
			return err
		}
		m.mu.Lock()
		m.active[r.ID] = r
		m.mu.Unlock()
		if r.State == StateRunning {
			m.startExecutor(r.ID)
		}
	}
	return nil
}

// CreateRequest describes a new rollout. Instances are in rollout order; the
// first CanaryCount of them are canaries.
type CreateRequest struct {
	EntityID      string     `json:"entity_id"`
	TargetVersion string     `json:"target_version"`
	Instances     []string   `json:"instances"`
	CanaryCount   int        `json:"canary_count"`
	Backup        bool       `json:"backup"`
	GateMinutes   int        `json:"gate_minutes"`
	AIGate        bool       `json:"ai_gate"`
	StartAt       *time.Time `json:"start_at"`
}

// Create validates and starts a rollout.
func (m *Manager) Create(ctx context.Context, user string, req CreateRequest) (*store.Rollout, error) {
	if req.EntityID == "" || req.TargetVersion == "" || len(req.Instances) == 0 {
		return nil, &ValidationError{Message: "entity_id, target_version and at least one instance are required"}
	}
	if req.CanaryCount < 0 || req.CanaryCount > len(req.Instances) {
		return nil, &ValidationError{Message: "canary_count must be between 0 and the number of instances"}
	}
	if req.GateMinutes < 0 || req.GateMinutes > MaxGateMinutes {
		return nil, &ValidationError{Message: "gate_minutes must be between 0 and 1440"}
	}
	if req.AIGate && m.opts.HealthCheck == nil {
		return nil, &ValidationError{Message: "the AI health check needs AI features to be configured"}
	}
	busy := m.busyInstances()
	reasons := map[string]string{}
	seen := map[string]bool{}
	title := ""
	for _, id := range req.Instances {
		if seen[id] {
			return nil, &ValidationError{Message: "each instance may only appear once"}
		}
		seen[id] = true
		if busy[id] {
			reasons[id] = "already part of another active rollout"
			continue
		}
		t, reason := m.pendingUpdate(id, req.EntityID, req.TargetVersion)
		if reason != "" {
			reasons[id] = reason
			continue
		}
		if title == "" {
			title = t.Title
		}
	}
	if len(reasons) > 0 {
		var parts []string
		for _, id := range req.Instances {
			if reason := reasons[id]; reason != "" {
				name := id
				if v, ok := m.fleet.View(id); ok {
					name = v.Name
				}
				parts = append(parts, name+": "+reason)
			}
		}
		return nil, &ValidationError{Message: "some instances cannot be part of this rollout: " + strings.Join(parts, "; "), Instances: reasons}
	}

	now := time.Now()
	r := &store.Rollout{
		ID: newID(), CreatedBy: user, EntityID: req.EntityID, Title: title, TargetVersion: req.TargetVersion,
		Backup: req.Backup, CanaryCount: req.CanaryCount, GateMinutes: req.GateMinutes, AIGate: req.AIGate, StartAt: req.StartAt,
		State: StateRunning, CreatedAt: now, UpdatedAt: now,
	}
	for i, id := range req.Instances {
		r.Steps = append(r.Steps, store.RolloutStep{Ord: i, InstanceID: id, Canary: i < req.CanaryCount, State: StepPending})
	}
	if err := m.store.CreateRollout(ctx, r); err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.active[r.ID] = r
	cp := clone(r)
	m.mu.Unlock()
	m.publish(cp)
	m.startExecutor(r.ID)
	return cp, nil
}

// pendingUpdate finds the install-able update task for entityID at version on an instance.
func (m *Manager) pendingUpdate(instanceID, entityID, version string) (tasks.Task, string) {
	v, ok := m.fleet.View(instanceID)
	if !ok {
		return tasks.Task{}, "instance not found"
	}
	for _, t := range v.Tasks {
		if t.Kind != tasks.KindUpdate || t.Source["entity_id"] != entityID {
			continue
		}
		if latest, _ := t.Source["latest_version"].(string); latest != version {
			return t, fmt.Sprintf("offers version %s, not %s", latest, version)
		}
		if !slices.Contains(t.Actions, tasks.ActionInstallUpdate) {
			return t, "this update cannot be installed remotely"
		}
		return t, ""
	}
	return tasks.Task{}, "no pending update for " + entityID
}

func (m *Manager) busyInstances() map[string]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	busy := map[string]bool{}
	for _, r := range m.active {
		for _, st := range r.Steps {
			busy[st.InstanceID] = true
		}
	}
	return busy
}

// ActiveCounts returns the number of running and paused rollouts (for metrics).
func (m *Manager) ActiveCounts() map[string]int {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]int{StateRunning: 0, StatePaused: 0}
	for _, r := range m.active {
		if r.State == StateRunning || r.State == StatePaused {
			out[r.State]++
		}
	}
	return out
}

// List returns recent rollouts, newest first.
func (m *Manager) List(ctx context.Context, limit int) ([]*store.Rollout, error) {
	list, err := m.store.ListRollouts(ctx, limit)
	if err != nil {
		return nil, err
	}
	// Active rollouts: memory is authoritative (the store is written right after).
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, r := range list {
		if a := m.active[r.ID]; a != nil {
			list[i] = clone(a)
		}
	}
	return list, nil
}

func (m *Manager) Get(ctx context.Context, id string) (*store.Rollout, error) {
	m.mu.Lock()
	if a := m.active[id]; a != nil {
		cp := clone(a)
		m.mu.Unlock()
		return cp, nil
	}
	m.mu.Unlock()
	r, err := m.store.GetRollout(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNotFound
	}
	return r, err
}

// Pause stops a running rollout after its current step. A step that is
// waiting for its window is stopped right away.
func (m *Manager) Pause(ctx context.Context, id string) (*store.Rollout, error) {
	return m.control(ctx, id, func(r *store.Rollout) error {
		if r.State != StateRunning {
			return ErrInvalidState
		}
		r.State, r.Message = StatePaused, "Paused by user"
		return nil
	})
}

// Resume continues a paused rollout. A failed step is retried.
func (m *Manager) Resume(ctx context.Context, id string) (*store.Rollout, error) {
	r, err := m.control(ctx, id, func(r *store.Rollout) error {
		if r.State != StatePaused {
			return ErrInvalidState
		}
		r.State, r.Message = StateRunning, ""
		for i := range r.Steps {
			if r.Steps[i].State == StepFailed {
				r.Steps[i].State, r.Steps[i].Message = StepPending, ""
				r.Steps[i].ActionID, r.Steps[i].StartedAt, r.Steps[i].FinishedAt = "", nil, nil
			}
		}
		return nil
	})
	if err == nil {
		m.startExecutor(id)
	}
	return r, err
}

// Cancel ends a rollout. Steps that have not started are skipped; an install
// that already started in Home Assistant cannot be stopped.
func (m *Manager) Cancel(ctx context.Context, id string) (*store.Rollout, error) {
	return m.control(ctx, id, func(r *store.Rollout) error {
		if r.State != StateRunning && r.State != StatePaused {
			return ErrInvalidState
		}
		r.State, r.Message = StateCancelled, "Cancelled by user"
		for i := range r.Steps {
			if s := r.Steps[i].State; s == StepPending || s == StepWaiting {
				r.Steps[i].State = StepSkipped
			}
		}
		return nil
	})
}

// Skip marks a failed or pending step of a paused rollout as skipped.
func (m *Manager) Skip(ctx context.Context, id string, ord int) (*store.Rollout, error) {
	return m.control(ctx, id, func(r *store.Rollout) error {
		if r.State != StatePaused || ord < 0 || ord >= len(r.Steps) {
			return ErrInvalidState
		}
		st := &r.Steps[ord]
		if st.State != StepFailed && st.State != StepPending {
			return ErrInvalidState
		}
		st.State, st.Message = StepSkipped, "Skipped by user"
		return nil
	})
}

// control applies a user change to an active rollout.
func (m *Manager) control(ctx context.Context, id string, fn func(*store.Rollout) error) (*store.Rollout, error) {
	m.mu.Lock()
	r := m.active[id]
	if r == nil {
		m.mu.Unlock()
		if _, err := m.store.GetRollout(ctx, id); err == nil {
			return nil, ErrInvalidState // finished rollouts cannot change
		}
		return nil, ErrNotFound
	}
	prev := clone(r)
	if err := fn(r); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	r.UpdatedAt = time.Now()
	if err := m.store.SaveRollout(ctx, r); err != nil {
		*r = *prev
		m.mu.Unlock()
		return nil, err
	}
	wake := m.running[id]
	if r.State == StateCancelled && wake == nil {
		delete(m.active, id) // otherwise the executor removes it when it stops
	}
	cp := clone(r)
	m.mu.Unlock()
	if wake != nil {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	m.publish(cp)
	return cp, nil
}

// update changes a rollout from its executor and persists it.
func (m *Manager) update(id string, fn func(*store.Rollout)) *store.Rollout {
	m.mu.Lock()
	r := m.active[id]
	if r == nil {
		m.mu.Unlock()
		return nil
	}
	fn(r)
	r.UpdatedAt = time.Now()
	if err := m.store.SaveRollout(context.Background(), r); err != nil {
		m.log.Error("save rollout", "rollout", id, "err", err)
	}
	cp := clone(r)
	m.mu.Unlock()
	m.publish(cp)
	return cp
}

func (m *Manager) snapshot(id string) *store.Rollout {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.active[id]; r != nil {
		return clone(r)
	}
	return nil
}

func (m *Manager) publish(r *store.Rollout) {
	m.fleet.Publish(fleet.Event{Type: "rollout", Data: r})
}

// startExecutor runs a rollout's steps in the background unless already running.
func (m *Manager) startExecutor(id string) {
	m.mu.Lock()
	if m.ctx == nil || m.running[id] != nil {
		m.mu.Unlock()
		return
	}
	wake := make(chan struct{}, 1)
	m.running[id] = wake
	ctx := m.ctx
	m.mu.Unlock()
	go func() {
		defer func() {
			m.mu.Lock()
			delete(m.running, id)
			if r := m.active[id]; r != nil && (r.State == StateCompleted || r.State == StateCancelled) {
				delete(m.active, id)
			}
			m.mu.Unlock()
		}()
		m.execute(ctx, id, wake)
	}()
}

func (m *Manager) execute(ctx context.Context, id string, wake <-chan struct{}) {
	for {
		r := m.snapshot(id)
		if r == nil || r.State != StateRunning || ctx.Err() != nil {
			return
		}
		next := slices.IndexFunc(r.Steps, func(s store.RolloutStep) bool {
			return s.State == StepPending || s.State == StepWaiting
		})
		if next < 0 {
			m.update(id, func(r *store.Rollout) { r.State, r.Message = StateCompleted, "" })
			return
		}
		err := m.runStep(ctx, r, next, wake)
		switch {
		case errors.Is(err, errStopped) || ctx.Err() != nil:
			// Paused or cancelled while waiting, or shutting down (Start recovers).
			return
		case err != nil:
			name := r.Steps[next].InstanceID
			if v, ok := m.fleet.View(name); ok {
				name = v.Name
			}
			m.update(id, func(r *store.Rollout) {
				st := &r.Steps[next]
				st.State, st.Message, st.FinishedAt = StepFailed, err.Error(), ptr(time.Now())
				if r.State == StateRunning || r.State == StatePaused {
					r.State, r.Message = StatePaused, fmt.Sprintf("Paused: %s: %v", name, err)
				}
			})
			m.log.Warn("rollout step failed", "rollout", id, "instance", name, "err", err)
			return
		default:
			m.update(id, func(r *store.Rollout) {
				r.Steps[next].State, r.Steps[next].FinishedAt = StepDone, ptr(time.Now())
			})
		}
	}
}

// runStep installs the update on one instance and checks its health.
func (m *Manager) runStep(ctx context.Context, r *store.Rollout, i int, wake <-chan struct{}) error {
	instID := r.Steps[i].InstanceID
	setStep := func(fn func(*store.RolloutStep)) { m.update(r.ID, func(r *store.Rollout) { fn(&r.Steps[i]) }) }

	// 1. Start time and maintenance window. Pausing here takes effect at once.
	for {
		v, ok := m.fleet.View(instID)
		if !ok {
			return errors.New("instance was removed")
		}
		win, err := ParseWindow(v.MaintWindow)
		if err != nil {
			return err
		}
		now := time.Now()
		if (r.StartAt == nil || !now.Before(*r.StartAt)) && win.Contains(now) {
			break
		}
		if r.Steps[i].State != StepWaiting {
			msg := "waiting for the start time"
			if r.StartAt == nil || !now.Before(*r.StartAt) {
				msg = "waiting for the maintenance window " + win.String()
			}
			setStep(func(s *store.RolloutStep) { s.State, s.Message = StepWaiting, msg })
			r.Steps[i].State = StepWaiting
		}
		if err := m.sleep(ctx, wake, r.ID); err != nil {
			setStep(func(s *store.RolloutStep) {
				if s.State == StepWaiting {
					s.State, s.Message = StepPending, ""
				}
			})
			return err
		}
	}

	// 2. The update must still be offered at the target version.
	v, _ := m.fleet.View(instID)
	task, reason := m.pendingUpdate(instID, r.EntityID, r.TargetVersion)
	if reason != "" {
		if task.ID == "" && v.Status.State == fleet.StateConnected && !v.Status.LastSync.IsZero() {
			setStep(func(s *store.RolloutStep) {
				s.State, s.Message, s.FinishedAt = StepSkipped, "no pending update: already installed or no longer offered", ptr(time.Now())
			})
			return nil
		}
		return errors.New(reason)
	}
	baseline := criticalTasks(v)

	// 3. Install through the regular action runner (allowlist + audit).
	setStep(func(s *store.RolloutStep) { s.State, s.Message, s.StartedAt = StepInstalling, "", ptr(time.Now()) })
	rec, err := m.runner.Submit("rollout:"+r.CreatedBy, instID, actions.Request{
		TaskID: task.ID, Action: tasks.ActionInstallUpdate, Backup: r.Backup})
	if err != nil {
		return fmt.Errorf("install: %w", err)
	}
	setStep(func(s *store.RolloutStep) { s.ActionID = rec.ID })
	rec, err = m.runner.Wait(ctx, rec.ID)
	if err != nil {
		return err
	}
	if rec.Status != actions.StatusSucceeded {
		return fmt.Errorf("install failed: %s", rec.Message)
	}

	// 4. Wait until a sync after the install shows the new version.
	setStep(func(s *store.RolloutStep) { s.State, s.Message = StepVerifying, "waiting for the new version" })
	deadline := rec.FinishedAt.Add(m.opts.VerifyTimeout)
	for {
		v, ok := m.fleet.View(instID)
		if !ok {
			return errors.New("instance was removed")
		}
		if v.Status.State == fleet.StateConnected && v.Status.LastSync.After(rec.FinishedAt) && installed(v, r.EntityID, r.TargetVersion) {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("version %s not reported within %s after the install", r.TargetVersion, m.opts.VerifyTimeout)
		}
		if err := m.sleepIgnoringPause(ctx); err != nil {
			return err
		}
	}

	// 5. Health gate: no new critical task on two checks in a row. The
	// connectivity task is critical, so an instance that stays down fails too.
	gateEnd := time.Now().Add(time.Duration(r.GateMinutes) * m.opts.GateUnit)
	if r.GateMinutes > 0 {
		setStep(func(s *store.RolloutStep) {
			s.Message = fmt.Sprintf("health check until %s", gateEnd.Format("15:04"))
		})
	}
	strikes := 0
	for {
		v, _ := m.fleet.View(instID)
		if newCrit := newCritical(baseline, v); newCrit != "" {
			if strikes++; strikes >= 2 {
				return fmt.Errorf("new critical task after the update: %s", newCrit)
			}
		} else {
			strikes = 0
		}
		if !time.Now().Before(gateEnd) && strikes == 0 {
			break
		}
		if err := m.sleepIgnoringPause(ctx); err != nil {
			return err
		}
	}
	msg := ""
	if r.AIGate {
		if m.opts.HealthCheck == nil {
			return errors.New("AI health check requested, but AI features are not configured")
		}
		setStep(func(s *store.RolloutStep) { s.Message = "AI health check" })
		verdict, findings, err := m.opts.HealthCheck(ctx, *rec)
		switch {
		case err != nil:
			return fmt.Errorf("AI health check failed: %w", err)
		case verdict == VerdictSkipped:
			msg = "AI health check skipped: AI is not enabled for this instance"
		case verdict != VerdictOK:
			detail := ""
			if len(findings) > 0 {
				detail = ": " + findings[0]
			}
			// The model can only stop a rollout, never wave a doubt through: a user decides.
			return fmt.Errorf("AI health check: %s%s", verdict, detail)
		default:
			msg = "AI health check: ok"
		}
	}
	setStep(func(s *store.RolloutStep) { s.Message = msg })
	return nil
}

// sleep waits one poll interval. It returns errStopped once the rollout is no
// longer running (woken by pause or cancel).
func (m *Manager) sleep(ctx context.Context, wake <-chan struct{}, id string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-wake:
	case <-time.After(m.opts.Poll):
	}
	if r := m.snapshot(id); r == nil || r.State != StateRunning {
		return errStopped
	}
	return nil
}

// sleepIgnoringPause waits one poll interval. Once an install has started its
// step is followed to the end even if the rollout is paused meanwhile.
func (m *Manager) sleepIgnoringPause(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(m.opts.Poll):
		return nil
	}
}

// installed reports whether the instance no longer offers the update, or
// reports the target version as installed (a newer one may be offered by now).
func installed(v fleet.InstanceView, entityID, version string) bool {
	for _, t := range v.Tasks {
		if t.Kind == tasks.KindUpdate && t.Source["entity_id"] == entityID {
			return t.Source["installed_version"] == version
		}
	}
	return true
}

func criticalTasks(v fleet.InstanceView) map[string]bool {
	out := map[string]bool{}
	for _, t := range v.Tasks {
		if t.Severity == tasks.SeverityCritical {
			out[t.ID] = true
		}
	}
	return out
}

// newCritical returns the title of a critical task that was not open before the install.
func newCritical(baseline map[string]bool, v fleet.InstanceView) string {
	for _, t := range v.Tasks {
		if t.Severity == tasks.SeverityCritical && !baseline[t.ID] {
			return t.Title
		}
	}
	return ""
}

func clone(r *store.Rollout) *store.Rollout {
	cp := *r
	cp.Steps = slices.Clone(r.Steps)
	return &cp
}

func ptr[T any](v T) *T { return &v }

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
