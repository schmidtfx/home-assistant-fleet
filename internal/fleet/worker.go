package fleet

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/schmidtfx/home-assistant-fleet/internal/hass"
	"github.com/schmidtfx/home-assistant-fleet/internal/store"
	"github.com/schmidtfx/home-assistant-fleet/internal/tasks"
)

type worker struct {
	m      *Manager
	inst   *store.Instance
	cancel context.CancelFunc
	done   chan struct{}
	resync chan struct{}
	// statusCh wakes the status publisher; statusReady is set once a full
	// sync finished in the current session, so incomplete views are not published.
	statusCh    chan struct{}
	statusReady atomic.Bool
	// statusMu orders status events: once statusStopped is set (the
	// "disabled" event was sent), the publisher sends nothing more.
	statusMu      sync.Mutex
	statusStopped bool

	clientMu sync.Mutex
	client   *hass.Client

	// Owned by the run goroutine.
	firstSeen map[string]time.Time
	updates   map[string]tasks.Task // update tasks keyed by entity_id
	other     []tasks.Task          // all non-update tasks from the last full sync
	// synced holds the task kinds whose source was read successfully in this
	// session, so a missing task of that kind has resolved (see recordHistory).
	synced map[tasks.Kind]bool
}

func (w *worker) currentClient() *hass.Client {
	w.clientMu.Lock()
	defer w.clientMu.Unlock()
	return w.client
}

func (w *worker) setClient(c *hass.Client) {
	w.clientMu.Lock()
	w.client = c
	w.clientMu.Unlock()
}

func (w *worker) run(ctx context.Context) {
	defer close(w.done)
	log := w.m.log.With("instance", w.inst.Name)
	backoff := time.Second
	for {
		err := w.session(ctx)
		if ctx.Err() != nil {
			return
		}
		log.Warn("instance connection lost", "err", err)
		w.firstSeen = map[string]time.Time{}
		w.updates = map[string]tasks.Task{}
		w.synced = nil
		w.other = nil
		conn := tasks.Connectivity(w.inst.ID, err)
		conn.FirstSeen = time.Now()
		w.m.setView(w, func(v *InstanceView) {
			v.Status.State = StateError
			v.Status.Error = err.Error()
			v.Tasks = []tasks.Task{conn}
			w.m.applyAcks(w.inst.ID, v.Tasks, false)
		})
		w.m.recordHistory(w.inst.ID, []tasks.Task{conn}, closeNone)
		// Authentication and configuration errors will not fix themselves quickly.
		maxBackoff := 2 * time.Minute
		if errors.Is(err, hass.ErrAuth) {
			maxBackoff = 15 * time.Minute
		}
		if errors.Is(err, errSessionHealthy) {
			backoff = time.Second
		}
		delay := backoff/2 + rand.N(backoff/2+1)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		case <-w.resync: // manual retry
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// errSessionHealthy marks a session that ran successfully before dropping, so
// reconnect backoff restarts from the minimum.
var errSessionHealthy = errors.New("connection closed")

// session connects, syncs and processes events until the connection fails.
func (w *worker) session(ctx context.Context) error {
	w.m.setView(w, func(v *InstanceView) {
		v.Status.State = StateConnecting
		v.Status.Error = ""
	})
	opts, err := w.m.dialOptions(w.inst)
	if err != nil {
		return err
	}
	c, err := hass.Dial(ctx, opts)
	if err != nil {
		return err
	}
	w.setClient(c)
	defer func() {
		w.setClient(nil)
		c.Close()
	}()

	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	cfg, err := c.GetConfig(cctx)
	cancel()
	if err != nil {
		return fmt.Errorf("get_config: %w", err)
	}
	cctx, cancel = context.WithTimeout(ctx, 10*time.Second)
	user, err := c.CurrentUser(cctx)
	cancel()
	if err != nil {
		user = &hass.User{}
	}
	isAdmin := user.IsAdmin
	w.statusReady.Store(false)
	if w.inst.PublishStatus && isAdmin {
		pctx, pcancel := context.WithCancel(ctx)
		pdone := make(chan struct{})
		go func() {
			defer close(pdone)
			w.publishStatusLoop(pctx, c)
		}()
		defer func() {
			pcancel()
			<-pdone
		}()
	}

	changes := make(chan *hass.State, 256)
	overflow := make(chan struct{}, 1)
	cctx, cancel = context.WithTimeout(ctx, 30*time.Second)
	err = c.SubscribeStateChanged(cctx, func(entityID string, st *hass.State) {
		if !strings.HasPrefix(entityID, "update.") {
			return
		}
		if st == nil {
			st = &hass.State{EntityID: entityID, State: "removed"}
		}
		select {
		case changes <- st:
		default: // falling behind: trigger a full resync instead
			select {
			case overflow <- struct{}{}:
			default:
			}
		}
	})
	cancel()
	if err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	w.m.setView(w, func(v *InstanceView) {
		v.Status.State = StateConnected
		v.Status.Error = ""
		v.Status.HAVersion = cfg.Version
		v.Status.Location = cfg.LocationName
		v.Status.Supervisor = cfg.HasComponent("hassio")
		v.Status.IsAdmin = isAdmin
		v.Status.UserID = user.ID
	})
	w.fullSync(ctx, c, cfg, isAdmin)

	resync := time.NewTicker(w.m.opts.ResyncInterval)
	defer resync.Stop()
	ping := time.NewTicker(w.m.opts.PingInterval)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.Done():
			return fmt.Errorf("%w: %v", errSessionHealthy, c.Err())
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.Ping(pctx)
			cancel()
			if err != nil {
				return fmt.Errorf("%w: ping failed: %v", errSessionHealthy, err)
			}
		case <-resync.C:
			w.fullSync(ctx, c, cfg, isAdmin)
		case <-w.resync:
			w.fullSync(ctx, c, cfg, isAdmin)
		case <-overflow:
			w.fullSync(ctx, c, cfg, isAdmin)
		case st := <-changes:
			w.applyUpdate(st)
			// Coalesce bursts before publishing.
		drain:
			for {
				select {
				case st := <-changes:
					w.applyUpdate(st)
				default:
					break drain
				}
			}
			w.publishTasks(nil, false)
		}
	}
}

func (w *worker) applyUpdate(st *hass.State) {
	if t, ok := tasks.FromUpdateEntity(w.inst.ID, st); ok {
		w.updates[st.EntityID] = t
	} else {
		delete(w.updates, st.EntityID)
	}
}

// fullSync queries every data source. Each source is best-effort: a failure
// becomes a status warning rather than breaking the whole sync. Sources that
// only feed checks switched off in the settings are not queried.
func (w *worker) fullSync(ctx context.Context, c *hass.Client, cfg *hass.HAConfig, isAdmin bool) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	id := w.inst.ID
	var warnings []string
	warn := func(source string, err error) {
		warnings = append(warnings, fmt.Sprintf("%s: %v", source, err))
	}
	if !isAdmin {
		warnings = append(warnings, "token does not belong to an admin user; repairs, supervisor data and actions are unavailable")
		if w.inst.PublishStatus {
			warnings = append(warnings, "publishing status to Home Assistant needs an admin token")
		}
	}
	synced := map[tasks.Kind]bool{}
	ok := func(kinds ...tasks.Kind) {
		for _, k := range kinds {
			synced[k] = true
		}
	}

	var other []tasks.Task
	now := time.Now()
	eff := w.m.effective(w.inst)
	on := func(kinds ...tasks.Kind) bool { return slices.ContainsFunc(kinds, eff.Enabled) }
	// A switched-off check has no tasks, so its open history entries close.
	for k, off := range eff.Disabled {
		if off {
			ok(k)
		}
	}

	states, err := c.GetStates(ctx)
	if err != nil {
		warn("states", err)
	} else {
		w.updates = map[string]tasks.Task{}
		for i := range states {
			w.applyUpdate(&states[i])
		}
		ok(tasks.KindUpdate)
		if on(tasks.KindBatteryLow) {
			other = append(other, tasks.FromBatteries(id, states, eff.Health)...)
			ok(tasks.KindBatteryLow)
		}
		if on(tasks.KindEntitiesUnavailable) { // the registry is only needed for this check
			if reg, err := c.EntityRegistry(ctx); err != nil {
				warn("entity registry", err)
			} else {
				other = append(other, tasks.FromUnavailableEntities(id, states, reg, now)...)
				ok(tasks.KindEntitiesUnavailable)
			}
		}
	}

	// From the TLS handshake of this connection.
	if on(tasks.KindCertExpiring) {
		other = append(other, tasks.FromCertExpiry(id, c.CertNotAfter, now)...)
		ok(tasks.KindCertExpiring)
	}

	if on(tasks.KindLogErrors) {
		if !isAdmin {
			ok(tasks.KindLogErrors) // system_log needs admin; the warning above covers it
		} else if logs, err := c.SystemLog(ctx); err != nil {
			warn("system log", err)
		} else {
			other = append(other, tasks.FromSystemLog(id, logs, now)...)
			ok(tasks.KindLogErrors)
		}
	}

	if on(tasks.KindRepair, tasks.KindRestartRequired) {
		if issues, err := c.ListRepairIssues(ctx); err != nil {
			warn("repairs", err)
		} else {
			other = append(other, tasks.FromRepairs(id, issues, w.issueTranslations(ctx, c, issues))...)
			ok(tasks.KindRepair, tasks.KindRestartRequired)
		}
	}

	// Always fetched: the integration list (drift view) comes from the entries too.
	entries, err := c.ConfigEntries(ctx)
	if err != nil {
		warn("config entries", err)
	} else {
		ok(tasks.KindSetupFailed)
	}
	var flows []hass.FlowProgress
	if on(tasks.KindReauth) {
		if flows, err = c.FlowsInProgress(ctx); err != nil {
			warn("config flows", err)
		} else {
			ok(tasks.KindReauth)
		}
	}
	other = append(other, tasks.FromConfigEntries(id, entries, flows)...)

	// Without the component the source does not exist, so neither can its tasks.
	if on(tasks.KindBackupStale) {
		if !cfg.HasComponent("backup") {
			ok(tasks.KindBackupStale)
		} else if b, err := c.BackupInfo(ctx); err != nil {
			warn("backups", err)
		} else {
			other = append(other, tasks.FromBackups(id, b, eff.BackupMaxAge, now)...)
			ok(tasks.KindBackupStale)
		}
	}

	// Versions and add-ons for the drift view. On failure the previous values are kept.
	var supInfo *hass.SupervisorInfo
	var addons []hass.Addon
	if cfg.HasComponent("hassio") {
		if info, err := c.SupervisorInfo(ctx); err != nil {
			warn("supervisor info", err)
		} else {
			supInfo = info
		}
		if on(tasks.KindDiskLow) {
			if h, err := c.HostInfo(ctx); err != nil {
				warn("host info", err)
			} else {
				other = append(other, tasks.FromHostInfo(id, h, eff.Health)...)
				ok(tasks.KindDiskLow)
			}
		}
		if list, err := c.Addons(ctx); err != nil {
			warn("add-ons", err)
		} else {
			addons = list
			if addons == nil {
				addons = []hass.Addon{}
			}
			slices.SortFunc(addons, func(a, b hass.Addon) int { return strings.Compare(a.Slug, b.Slug) })
		}
	}

	resolutionKinds := []tasks.Kind{tasks.KindRebootRequired, tasks.KindUnhealthy, tasks.KindUnsupported}
	if !cfg.HasComponent("hassio") {
		ok(resolutionKinds...)
		ok(tasks.KindDiskLow)
	} else if on(resolutionKinds...) {
		if r, err := c.ResolutionInfo(ctx); err != nil {
			warn("supervisor", err)
		} else {
			other = append(other, tasks.FromResolution(id, r)...)
			ok(resolutionKinds...)
		}
	}

	// Sources such as repairs feed several kinds; drop the switched-off ones.
	other = slices.DeleteFunc(other, func(t tasks.Task) bool { return !eff.Enabled(t.Kind) })
	w.other = other
	w.synced = synced
	pruneAcks := len(warnings) == 0
	if warnings == nil {
		warnings = []string{}
	}
	integrations := integrationDomains(cfg, entries)
	w.statusReady.Store(true)
	w.publishTasks(func(s *Status) {
		s.LastSync = time.Now()
		s.Warnings = warnings
		s.Integrations = integrations
		if supInfo != nil {
			s.SupervisorVersion, s.OSVersion, s.Machine = supInfo.Supervisor, supInfo.HassOS, supInfo.Machine
		}
		if addons != nil {
			s.Addons = addons
		}
	}, pruneAcks)
}

// issueTranslations fetches English titles for repair issues (best-effort).
func (w *worker) issueTranslations(ctx context.Context, c *hass.Client, issues []hass.RepairIssue) map[string]string {
	var domains []string
	for _, is := range issues {
		if !slices.Contains(domains, is.Domain) {
			domains = append(domains, is.Domain)
		}
	}
	if len(domains) == 0 {
		return nil
	}
	var out struct {
		Resources map[string]string `json:"resources"`
	}
	err := c.Call(ctx, map[string]any{
		"type": "frontend/get_translations", "language": "en", "category": "issues", "integration": domains,
	}, &out)
	if err != nil {
		return nil
	}
	return out.Resources
}

// publishTasks merges update and other tasks, stamps first-seen times and broadcasts.
// pruneAcks drops acknowledgements of tasks that disappeared; only pass true
// after a full sync in which every source succeeded.
func (w *worker) publishTasks(updateStatus func(*Status), pruneAcks bool) {
	all := make([]tasks.Task, 0, len(w.updates)+len(w.other))
	for _, t := range w.updates {
		all = append(all, t)
	}
	all = append(all, w.other...)
	now := time.Now()
	seen := make(map[string]time.Time, len(all))
	for i := range all {
		fs, ok := w.firstSeen[all[i].ID]
		if !ok {
			fs = now
		}
		all[i].FirstSeen = fs
		seen[all[i].ID] = fs
	}
	w.firstSeen = seen
	tasks.Sort(all)
	w.m.setView(w, func(v *InstanceView) {
		w.m.applyAcks(w.inst.ID, all, pruneAcks)
		v.Tasks = all
		if updateStatus != nil {
			updateStatus(&v.Status)
		}
	})
	w.m.recordHistory(w.inst.ID, all, func(k tasks.Kind) bool {
		return k == tasks.KindConnectivity || w.synced[k] // connected, so it is reachable again
	})
}

// integrationDomains returns the sorted, de-duplicated integration domains of
// an instance (top-level components plus config entry domains).
func integrationDomains(cfg *hass.HAConfig, entries []hass.ConfigEntry) []string {
	set := map[string]bool{}
	for _, c := range cfg.Components {
		if !strings.Contains(c, ".") { // skip platform entries like "sensor.mqtt"
			set[c] = true
		}
	}
	for _, e := range entries {
		if e.DisabledBy == nil {
			set[e.Domain] = true
		}
	}
	out := make([]string, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	slices.Sort(out)
	return out
}
