// Package actions executes allowlisted operations (install update, restart,
// reboot, ...) against an instance and records them in the audit log.
package actions

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/schmidtfx/home-assistant-fleet/internal/fleet"
	"github.com/schmidtfx/home-assistant-fleet/internal/hass"
	"github.com/schmidtfx/home-assistant-fleet/internal/store"
	"github.com/schmidtfx/home-assistant-fleet/internal/tasks"
)

type Request struct {
	TaskID string `json:"task_id"`
	Action string `json:"action"`
	Backup bool   `json:"backup"`
}

type Status string

const (
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
)

type Record struct {
	ID         string    `json:"id"`
	InstanceID string    `json:"instance_id"`
	TaskID     string    `json:"task_id"`
	Action     string    `json:"action"`
	Target     string    `json:"target"`
	Username   string    `json:"username"`
	Status     Status    `json:"status"`
	Message    string    `json:"message,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitzero"`
	// For install_update: the versions when the action was submitted.
	FromVersion string `json:"from_version,omitempty"`
	ToVersion   string `json:"to_version,omitempty"`
}

var (
	ErrUnknownTask      = errors.New("task not found on this instance")
	ErrActionNotAllowed = errors.New("action is not available for this task")
	ErrAlreadyRunning   = errors.New("an action is already running for this task")
)

// Timeout bounds how long a single action may run (updates can be slow).
const Timeout = 45 * time.Minute

type Runner struct {
	fleet *fleet.Manager
	store *store.Store
	log   *slog.Logger

	mu      sync.Mutex
	running map[string]*Record // by task id
	recent  []*Record
	counts  map[Count]uint64
	done    map[string]chan struct{} // by record id, closed when the action finishes
}

// Count keys the finished-action counters (for metrics).
type Count struct {
	Action string
	Status Status
}

func NewRunner(f *fleet.Manager, s *store.Store, log *slog.Logger) *Runner {
	return &Runner{fleet: f, store: s, log: log, running: map[string]*Record{}, counts: map[Count]uint64{},
		done: map[string]chan struct{}{}}
}

// Recent returns running and recently finished actions, newest first.
func (r *Runner) Recent() []Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Record, 0, len(r.recent))
	for i := len(r.recent) - 1; i >= 0; i-- {
		out = append(out, *r.recent[i])
	}
	return out
}

// Counts returns how many actions finished, by action and result, since start.
func (r *Runner) Counts() map[Count]uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[Count]uint64, len(r.counts))
	for k, v := range r.counts {
		out[k] = v
	}
	return out
}

// Get returns a running or recent action by ID.
func (r *Runner) Get(id string) (Record, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rec := range r.recent {
		if rec.ID == id {
			return *rec, true
		}
	}
	return Record{}, false
}

// ErrUnknownAction is returned by Wait for an ID that was never submitted (or
// has dropped out of the recent list).
var ErrUnknownAction = errors.New("unknown action")

// Wait blocks until the action finishes or ctx is done, and returns its final record.
func (r *Runner) Wait(ctx context.Context, id string) (*Record, error) {
	r.mu.Lock()
	ch := r.done[id]
	r.mu.Unlock()
	if ch != nil {
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rec := range r.recent {
		if rec.ID == id {
			cp := *rec
			return &cp, nil
		}
	}
	return nil, ErrUnknownAction
}

// Submit validates the request and starts the action in the background.
func (r *Runner) Submit(username, instanceID string, req Request) (*Record, error) {
	view, ok := r.fleet.View(instanceID)
	if !ok {
		return nil, ErrUnknownTask
	}
	task, ok := r.fleet.Task(instanceID, req.TaskID)
	if !ok {
		return nil, ErrUnknownTask
	}
	if !slices.Contains(task.Actions, req.Action) {
		return nil, ErrActionNotAllowed
	}
	client, err := r.fleet.Client(instanceID)
	if err != nil {
		return nil, err
	}
	target, _ := task.Source["entity_id"].(string)
	if target == "" {
		target = task.Title
	}

	rec := &Record{
		ID: newID(), InstanceID: instanceID, TaskID: task.ID, Action: req.Action, Target: target,
		Username: username, Status: StatusRunning, StartedAt: time.Now(),
	}
	if req.Action == tasks.ActionInstallUpdate {
		rec.FromVersion, _ = task.Source["installed_version"].(string)
		rec.ToVersion, _ = task.Source["latest_version"].(string)
	}
	r.mu.Lock()
	if _, busy := r.running[task.ID]; busy {
		r.mu.Unlock()
		return nil, ErrAlreadyRunning
	}
	r.running[task.ID] = rec
	r.done[rec.ID] = make(chan struct{})
	r.recent = append(r.recent, rec)
	if len(r.recent) > 100 {
		r.recent = r.recent[len(r.recent)-100:]
	}
	cp := *rec
	r.mu.Unlock()

	r.audit(view.Name, &cp, "submitted", "")
	r.fleet.Publish(fleet.Event{Type: "action", Action: cp})
	go r.execute(client, view.Name, task, req, rec)
	return &cp, nil
}

func (r *Runner) execute(c *hass.Client, instanceName string, task tasks.Task, req Request, rec *Record) {
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	msg, err := run(ctx, c, task, req)

	// Restarts, reboots and core/OS updates drop the connection mid-call; that is expected.
	if err != nil && disconnected(c) && expectsDisconnect(task, req.Action) {
		msg, err = "submitted; instance is restarting", nil
	}

	r.mu.Lock()
	rec.FinishedAt = time.Now()
	if err != nil {
		rec.Status, rec.Message = StatusFailed, err.Error()
	} else {
		rec.Status, rec.Message = StatusSucceeded, msg
	}
	delete(r.running, task.ID)
	r.counts[Count{rec.Action, rec.Status}]++
	close(r.done[rec.ID])
	delete(r.done, rec.ID)
	cp := *rec
	r.mu.Unlock()

	r.audit(instanceName, &cp, string(cp.Status), cp.Message)
	r.fleet.Publish(fleet.Event{Type: "action", Action: cp})
	r.fleet.Resync(rec.InstanceID)
}

func run(ctx context.Context, c *hass.Client, task tasks.Task, req Request) (string, error) {
	entityID, _ := task.Source["entity_id"].(string)
	switch req.Action {
	case tasks.ActionInstallUpdate:
		data := map[string]any{"entity_id": entityID}
		if req.Backup && task.Source["supports_backup"] == true {
			data["backup"] = true
		}
		return "update installed", c.CallService(ctx, "update", "install", data)
	case tasks.ActionSkipUpdate:
		return "update skipped", c.CallService(ctx, "update", "skip", map[string]any{"entity_id": entityID})
	case tasks.ActionRestartCore:
		if err := checkConfig(ctx, c); err != nil {
			return "", err
		}
		return "restart requested", c.CallService(ctx, "homeassistant", "restart", nil)
	case tasks.ActionRebootHost:
		return "reboot requested", c.CallService(ctx, "hassio", "host_reboot", nil)
	case tasks.ActionCreateBackup:
		return "backup started", c.Call(ctx, map[string]any{"type": "backup/generate_with_automatic_settings"}, nil)
	}
	return "", ErrActionNotAllowed
}

// checkConfig validates configuration before a restart when the Supervisor is available.
func checkConfig(ctx context.Context, c *hass.Client) error {
	cfg, err := c.GetConfig(ctx)
	if err != nil || !cfg.HasComponent("hassio") {
		return nil // no Supervisor: Core performs its own check on restart
	}
	if err := c.SupervisorAPI(ctx, "post", "/core/check", nil, nil); err != nil {
		return fmt.Errorf("configuration check failed, not restarting: %w", err)
	}
	return nil
}

func disconnected(c *hass.Client) bool {
	select {
	case <-c.Done():
		return true
	case <-time.After(2 * time.Second):
		return false
	}
}

func expectsDisconnect(task tasks.Task, action string) bool {
	switch action {
	case tasks.ActionRestartCore, tasks.ActionRebootHost:
		return true
	case tasks.ActionInstallUpdate:
		id, _ := task.Source["entity_id"].(string)
		return id == "update.home_assistant_core_update" ||
			id == "update.home_assistant_operating_system_update" ||
			id == "update.home_assistant_supervisor_update"
	}
	return false
}

func (r *Runner) audit(instanceName string, rec *Record, result, msg string) {
	err := r.store.AddAudit(context.Background(), &store.AuditEntry{
		Username: rec.Username, InstanceID: rec.InstanceID, InstanceName: instanceName,
		Action: rec.Action, Target: rec.Target, Result: result, Message: msg,
	})
	if err != nil {
		r.log.Error("write audit entry", "err", err)
	}
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
