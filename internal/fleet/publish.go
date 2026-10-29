package fleet

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"time"

	"github.com/schmidtfx/home-assistant-fleet/internal/hass"
	"github.com/schmidtfx/home-assistant-fleet/internal/tasks"
)

// Status publishing: with PublishStatus on, hafleet fires a hafleet_status
// event on the instance whenever its view changes (and as a heartbeat). The
// hafleet custom integration (home-assistant-fleet-hacs) turns the event into
// entities. An instance only ever receives its own view.

const (
	statusSchema = 1
	// Caps keep the event small: HA keeps event data in its recorder.
	statusMaxTasks    = 20
	statusMaxTitleLen = 200
)

// statusEvent builds the hafleet_status event data for an instance view.
// Counts only include unacknowledged tasks; acknowledged ones are still listed.
func statusEvent(v InstanceView, heartbeat time.Duration) map[string]any {
	open := map[string]int{"total": 0, "critical": 0, "warning": 0, "info": 0, "acknowledged": 0}
	type kindData struct {
		Count       int              `json:"count"`
		MaxSeverity *tasks.Severity  `json:"max_severity"`
		Tasks       []map[string]any `json:"tasks"`
	}
	kinds := map[tasks.Kind]*kindData{}
	for _, k := range tasks.Kinds {
		if k != tasks.KindConnectivity { // never published: hafleet cannot reach the instance then
			kinds[k] = &kindData{Tasks: []map[string]any{}}
		}
	}
	for _, t := range v.Tasks { // sorted by severity, so the first unacknowledged task has the max
		k := kinds[t.Kind]
		if k == nil {
			continue
		}
		acked := t.Ack != nil
		if acked {
			open["acknowledged"]++
		} else {
			open["total"]++
			open[string(t.Severity)]++
			k.Count++
			if k.MaxSeverity == nil {
				sev := t.Severity
				k.MaxSeverity = &sev
			}
		}
		if len(k.Tasks) < statusMaxTasks {
			title := t.Title
			if r := []rune(title); len(r) > statusMaxTitleLen {
				title = string(r[:statusMaxTitleLen-1]) + "…"
			}
			k.Tasks = append(k.Tasks, map[string]any{"title": title, "severity": t.Severity, "acknowledged": acked})
		}
	}
	var lastSync any
	if !v.Status.LastSync.IsZero() {
		lastSync = v.Status.LastSync.UTC().Format(time.RFC3339)
	}
	warnings := v.Status.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	return map[string]any{
		"schema":            statusSchema,
		"enabled":           true,
		"instance_id":       v.ID,
		"instance_name":     v.Name,
		"state":             v.Status.State,
		"warnings":          warnings,
		"last_sync":         lastSync,
		"heartbeat_seconds": int(heartbeat.Seconds()),
		"open":              open,
		"kinds":             kinds,
	}
}

// statusDisabledEvent tells the integration that publishing stopped.
func statusDisabledEvent() map[string]any {
	return map[string]any{"schema": statusSchema, "enabled": false}
}

// statusChanged wakes the instance's status publisher, if any.
func (m *Manager) statusChanged(id string) {
	m.mu.RLock()
	w := m.workers[id]
	m.mu.RUnlock()
	if w != nil {
		select {
		case w.statusCh <- struct{}{}:
		default:
		}
	}
}

// publishStatusLoop fires the status event when the view changed (debounced)
// and at least every heartbeat. It runs for one session and stops with ctx.
func (w *worker) publishStatusLoop(ctx context.Context, c *hass.Client) {
	log := w.m.log.With("instance", w.inst.Name)
	heartbeat := w.m.opts.StatusHeartbeat
	tick := time.NewTicker(heartbeat)
	defer tick.Stop()
	var last [sha256.Size]byte
	var lastSent time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-w.statusCh:
			// A sync changes the view several times; publish the result once.
			select {
			case <-ctx.Done():
				return
			case <-time.After(w.m.opts.StatusDebounce):
			}
		}
		if !w.statusReady.Load() {
			continue // wait for the first full sync of this session
		}
		v, ok := w.m.View(w.inst.ID)
		if !ok {
			continue
		}
		data := statusEvent(v, heartbeat)
		b, err := json.Marshal(data)
		if err != nil {
			log.Warn("encode status event", "err", err)
			continue
		}
		h := sha256.Sum256(b)
		if h == last && time.Since(lastSent) < heartbeat/2 {
			continue
		}
		w.statusMu.Lock()
		if w.statusStopped {
			w.statusMu.Unlock()
			return
		}
		fctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err = c.FireStatusEvent(fctx, data)
		cancel()
		w.statusMu.Unlock()
		if err != nil {
			log.Warn("publish status", "err", err) // retried on the next change or heartbeat
			continue
		}
		last, lastSent = h, time.Now()
	}
}

// unpublishStatus tells the integration that publishing stopped, through the
// still-connected client of a worker that is about to be stopped.
func (w *worker) unpublishStatus() {
	w.statusMu.Lock()
	defer w.statusMu.Unlock()
	w.statusStopped = true
	c := w.currentClient()
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.FireStatusEvent(ctx, statusDisabledEvent()); err != nil {
		w.m.log.Warn("unpublish status", "instance", w.inst.Name, "err", err)
	}
}
