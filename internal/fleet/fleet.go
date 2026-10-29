// Package fleet maintains a live connection to every configured Home Assistant
// instance and keeps an in-memory snapshot of each instance's open tasks.
package fleet

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/schmidtfx/home-assistant-fleet/internal/hass"
	"github.com/schmidtfx/home-assistant-fleet/internal/secrets"
	"github.com/schmidtfx/home-assistant-fleet/internal/store"
	"github.com/schmidtfx/home-assistant-fleet/internal/tasks"
)

type ConnState string

const (
	StateDisabled   ConnState = "disabled"
	StateConnecting ConnState = "connecting"
	StateConnected  ConnState = "connected"
	StateError      ConnState = "error"
)

// Status describes an instance's connection as seen by the fleet manager.
type Status struct {
	State      ConnState `json:"state"`
	Error      string    `json:"error,omitempty"`
	HAVersion  string    `json:"ha_version,omitempty"`
	Location   string    `json:"location_name,omitempty"`
	Supervisor bool      `json:"supervisor"`
	IsAdmin    bool      `json:"is_admin"`
	// UserID is the HA user the token belongs to (selected in the hafleet integration).
	UserID   string   `json:"user_id,omitempty"`
	Warnings []string `json:"warnings"`
	// Integrations are the loaded integration domains (config entries + components).
	Integrations []string  `json:"integrations"`
	LastSync     time.Time `json:"last_sync,omitzero"`
	// Supervisor installs only: versions and installed add-ons (for the drift view).
	SupervisorVersion string       `json:"supervisor_version,omitempty"`
	OSVersion         string       `json:"os_version,omitempty"`
	Machine           string       `json:"machine,omitempty"`
	Addons            []hass.Addon `json:"addons,omitempty"`
}

// InstanceView is the public (token-free) representation of an instance.
type InstanceView struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	URL               string `json:"url"`
	Enabled           bool   `json:"enabled"`
	AIEnabled         bool   `json:"ai_enabled"`
	AllowInsecureHTTP bool   `json:"allow_insecure_http"`
	HasCAPEM          bool   `json:"has_ca_pem"`
	CertFingerprint   string `json:"cert_fingerprint"`
	MaintWindow       string `json:"maint_window"`
	PublishStatus     bool   `json:"publish_status"`
	// Settings are the instance's check overrides (fleet-wide settings apply where unset).
	Settings tasks.Settings `json:"settings"`
	Status   Status         `json:"status"`
	Tasks    []tasks.Task   `json:"tasks"`
}

// Event is broadcast to subscribers whenever an instance's view changes.
type Event struct {
	Type     string        `json:"type"` // instance | instance_removed | action
	Instance *InstanceView `json:"instance,omitempty"`
	ID       string        `json:"id,omitempty"`
	Action   any           `json:"action,omitempty"`
	Data     any           `json:"data,omitempty"`
}

type Options struct {
	ResyncInterval time.Duration
	BackupMaxAge   time.Duration
	PingInterval   time.Duration
	// StatusHeartbeat is how often the status event is re-sent unchanged;
	// StatusDebounce coalesces view changes before it is sent.
	StatusHeartbeat time.Duration
	StatusDebounce  time.Duration
	Health          tasks.HealthOptions
	Logger          *slog.Logger
}

type Manager struct {
	store *store.Store
	box   *secrets.Box
	opts  Options
	log   *slog.Logger

	mu      sync.RWMutex
	ctx     context.Context
	workers map[string]*worker
	views   map[string]*InstanceView
	acks    map[string]map[string]*store.Ack // instance id -> task id -> ack

	histMu sync.Mutex
	hist   map[string]map[string]histOpen // instance id -> task id -> open history entry

	subMu sync.Mutex
	subs  map[chan Event]struct{}

	setMu    sync.RWMutex
	settings tasks.Settings // fleet-wide check settings
}

func New(s *store.Store, box *secrets.Box, opts Options) *Manager {
	if opts.ResyncInterval == 0 {
		opts.ResyncInterval = 10 * time.Minute
	}
	if opts.BackupMaxAge == 0 {
		opts.BackupMaxAge = 7 * 24 * time.Hour
	}
	if opts.PingInterval == 0 {
		opts.PingInterval = 30 * time.Second
	}
	if opts.StatusHeartbeat == 0 {
		opts.StatusHeartbeat = 5 * time.Minute
	}
	if opts.StatusDebounce == 0 {
		opts.StatusDebounce = time.Second
	}
	if opts.Health.DiskFreeMinPercent == 0 {
		opts.Health.DiskFreeMinPercent = 15
	}
	if opts.Health.BatteryMinPercent == 0 {
		opts.Health.BatteryMinPercent = 15
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Manager{
		store: s, box: box, opts: opts, log: opts.Logger,
		workers: map[string]*worker{}, views: map[string]*InstanceView{}, acks: map[string]map[string]*store.Ack{}, hist: map[string]map[string]histOpen{}, subs: map[chan Event]struct{}{},
	}
}

// Start launches workers for all stored instances. Workers stop when ctx is done.
func (m *Manager) Start(ctx context.Context) error {
	if err := m.loadAcks(ctx); err != nil {
		return err
	}
	if err := m.loadHistory(ctx); err != nil {
		return err
	}
	if err := m.loadSettings(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	m.ctx = ctx
	m.mu.Unlock()
	go m.expireAcksLoop(ctx)
	go m.pruneHistoryLoop(ctx)
	list, err := m.store.ListInstances(ctx)
	if err != nil {
		return err
	}
	for _, in := range list {
		m.start(in)
	}
	return nil
}

// Reload (re)starts the worker for an instance after it was created or changed.
func (m *Manager) Reload(ctx context.Context, id string) error {
	in, err := m.store.GetInstance(ctx, id)
	if err != nil {
		return err
	}
	m.mu.RLock()
	old := m.workers[id]
	m.mu.RUnlock()
	if old != nil && old.inst.PublishStatus && !(in.PublishStatus && in.Enabled && in.URL == old.inst.URL) {
		old.unpublishStatus()
	}
	m.stop(id)
	m.start(in)
	return nil
}

// Remove stops the worker and forgets the instance.
func (m *Manager) Remove(id string) {
	m.mu.RLock()
	old := m.workers[id]
	m.mu.RUnlock()
	if old != nil && old.inst.PublishStatus {
		old.unpublishStatus()
	}
	m.stop(id)
	m.mu.Lock()
	delete(m.views, id)
	delete(m.acks, id) // rows are removed by ON DELETE CASCADE
	m.mu.Unlock()
	m.forgetHistory(id)
	m.publish(Event{Type: "instance_removed", ID: id})
}

func (m *Manager) start(in *store.Instance) {
	view := &InstanceView{
		ID: in.ID, Name: in.Name, URL: in.URL, Enabled: in.Enabled, AIEnabled: in.AIEnabled,
		AllowInsecureHTTP: in.AllowInsecureHTTP, HasCAPEM: in.CAPEM != "", CertFingerprint: in.CertFingerprint, MaintWindow: in.MaintWindow,
		PublishStatus: in.PublishStatus, Settings: m.instanceSettings(in),
		Status: Status{State: StateDisabled, Warnings: []string{}, Integrations: []string{}}, Tasks: []tasks.Task{},
	}
	m.mu.Lock()
	m.views[in.ID] = view
	if !in.Enabled || m.ctx == nil {
		m.mu.Unlock()
		if !in.Enabled {
			m.recordHistory(in.ID, nil, closeAll) // a disabled instance has no open tasks
		}
		m.publish(Event{Type: "instance", Instance: view})
		return
	}
	view.Status.State = StateConnecting
	ctx, cancel := context.WithCancel(m.ctx)
	w := &worker{m: m, inst: in, cancel: cancel, done: make(chan struct{}), resync: make(chan struct{}, 1), statusCh: make(chan struct{}, 1),
		firstSeen: map[string]time.Time{}, updates: map[string]tasks.Task{}}
	m.workers[in.ID] = w
	m.mu.Unlock()
	m.publish(Event{Type: "instance", Instance: view})
	go w.run(ctx)
}

func (m *Manager) stop(id string) {
	m.mu.Lock()
	w := m.workers[id]
	delete(m.workers, id)
	m.mu.Unlock()
	if w != nil {
		w.cancel()
		<-w.done
	}
}

// Views returns a copy of all instance views sorted by name.
func (m *Manager) Views() []InstanceView {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]InstanceView, 0, len(m.views))
	for _, v := range m.views {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (m *Manager) View(id string) (InstanceView, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.views[id]
	if !ok {
		return InstanceView{}, false
	}
	return *v, true
}

// Task finds a task by id on an instance.
func (m *Manager) Task(instanceID, taskID string) (tasks.Task, bool) {
	v, ok := m.View(instanceID)
	if !ok {
		return tasks.Task{}, false
	}
	for _, t := range v.Tasks {
		if t.ID == taskID {
			return t, true
		}
	}
	return tasks.Task{}, false
}

var ErrNotConnected = errors.New("instance is not connected")

// Client returns the live client for an instance.
func (m *Manager) Client(id string) (*hass.Client, error) {
	m.mu.RLock()
	w := m.workers[id]
	m.mu.RUnlock()
	if w == nil {
		return nil, ErrNotConnected
	}
	c := w.currentClient()
	if c == nil {
		return nil, ErrNotConnected
	}
	return c, nil
}

// Resync asks an instance's worker to refresh all tasks soon.
func (m *Manager) Resync(id string) {
	m.mu.RLock()
	w := m.workers[id]
	m.mu.RUnlock()
	if w != nil {
		select {
		case w.resync <- struct{}{}:
		default:
		}
	}
}

// Subscribe returns a channel of events. Slow subscribers drop events; call cancel when done.
func (m *Manager) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 64)
	m.subMu.Lock()
	m.subs[ch] = struct{}{}
	m.subMu.Unlock()
	return ch, func() {
		m.subMu.Lock()
		if _, ok := m.subs[ch]; ok {
			delete(m.subs, ch)
			close(ch)
		}
		m.subMu.Unlock()
	}
}

func (m *Manager) publish(ev Event) {
	if ev.Type == "instance" && ev.Instance != nil {
		m.statusChanged(ev.Instance.ID)
	}
	m.subMu.Lock()
	defer m.subMu.Unlock()
	for ch := range m.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// Publish broadcasts an arbitrary event (used for action progress).
func (m *Manager) Publish(ev Event) { m.publish(ev) }

// setView updates the stored view for an instance via fn and broadcasts it.
// It is a no-op if the worker has been replaced.
func (m *Manager) setView(w *worker, fn func(v *InstanceView)) {
	m.mu.Lock()
	if m.workers[w.inst.ID] != w {
		m.mu.Unlock()
		return
	}
	v := m.views[w.inst.ID]
	fn(v)
	cp := *v
	m.mu.Unlock()
	m.publish(Event{Type: "instance", Instance: &cp})
}

// Options for dialing a stored instance (decrypts the token).
func (m *Manager) dialOptions(in *store.Instance) (hass.Options, error) {
	tok, err := m.box.Open(in.TokenEnc, []byte(in.ID))
	if err != nil {
		return hass.Options{}, fmt.Errorf("decrypt token: %w", err)
	}
	return hass.Options{
		URL: in.URL, Token: string(tok), CAPEM: in.CAPEM,
		CertFingerprint: in.CertFingerprint, AllowInsecureHTTP: in.AllowInsecureHTTP,
	}, nil
}
