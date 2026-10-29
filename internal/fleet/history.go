package fleet

import (
	"context"
	"time"

	"github.com/schmidtfx/home-assistant-fleet/internal/store"
	"github.com/schmidtfx/home-assistant-fleet/internal/tasks"
)

// Task history records when each task opened and closed. The open entries are
// mirrored in memory (m.hist, guarded by m.histMu) so each publish only writes
// the difference.

// HistoryRetention is how long closed history entries are kept.
const HistoryRetention = 365 * 24 * time.Hour

// A missing task only means it resolved if the source that reports its kind
// was read successfully. closable says which kinds that is true for.
type closable func(tasks.Kind) bool

var (
	// closeNone is used while an instance is unreachable: missing tasks say nothing.
	closeNone closable = func(tasks.Kind) bool { return false }
	// closeAll is used when an instance has no tasks at all (disabled).
	closeAll closable = func(tasks.Kind) bool { return true }
)

type histOpen struct {
	kind     tasks.Kind
	severity tasks.Severity
	title    string
}

func (m *Manager) loadHistory(ctx context.Context) error {
	list, err := m.store.OpenHistory(ctx)
	if err != nil {
		return err
	}
	m.histMu.Lock()
	defer m.histMu.Unlock()
	for _, e := range list {
		if m.hist[e.InstanceID] == nil {
			m.hist[e.InstanceID] = map[string]histOpen{}
		}
		m.hist[e.InstanceID][e.TaskID] = histOpen{tasks.Kind(e.Kind), tasks.Severity(e.Severity), e.Title}
	}
	return nil
}

// recordHistory opens entries for new tasks, updates changed ones and closes
// missing ones whose kind is closable.
func (m *Manager) recordHistory(instanceID string, ts []tasks.Task, canClose closable) {
	m.histMu.Lock()
	defer m.histMu.Unlock()
	open := m.hist[instanceID]
	now := time.Now()
	ch := store.HistoryChange{ClosedAt: now}
	present := make(map[string]bool, len(ts))
	for _, t := range ts {
		present[t.ID] = true
		e := store.HistoryEntry{TaskID: t.ID, Kind: string(t.Kind), Severity: string(t.Severity), Title: t.Title, OpenedAt: now}
		if h, ok := open[t.ID]; !ok {
			ch.Opened = append(ch.Opened, e)
		} else if h.severity != t.Severity || h.title != t.Title {
			ch.Updated = append(ch.Updated, e)
		}
	}
	for id, h := range open {
		if !present[id] && canClose(h.kind) {
			ch.Closed = append(ch.Closed, id)
		}
	}
	if err := m.store.ApplyHistory(context.Background(), instanceID, ch); err != nil {
		// Memory is left unchanged, so the next publish retries the same change.
		m.log.Warn("record task history", "instance", instanceID, "err", err)
		return
	}
	if open == nil {
		open = map[string]histOpen{}
		m.hist[instanceID] = open
	}
	for _, e := range append(ch.Opened, ch.Updated...) {
		open[e.TaskID] = histOpen{tasks.Kind(e.Kind), tasks.Severity(e.Severity), e.Title}
	}
	for _, id := range ch.Closed {
		delete(open, id)
	}
}

// forgetHistory drops the in-memory state of a deleted instance (its rows are
// removed by ON DELETE CASCADE).
func (m *Manager) forgetHistory(instanceID string) {
	m.histMu.Lock()
	delete(m.hist, instanceID)
	m.histMu.Unlock()
}

// History returns a trend summary over the last days, optionally for one instance.
func (m *Manager) History(ctx context.Context, days int, instanceID string) (*store.HistorySummary, error) {
	now := time.Now()
	since := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -(days - 1))
	entries, err := m.store.ListHistory(ctx, since, instanceID)
	if err != nil {
		return nil, err
	}
	return store.Summarize(entries, days, now), nil
}

// pruneHistoryLoop deletes old closed entries once a day.
func (m *Manager) pruneHistoryLoop(ctx context.Context) {
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		if err := m.store.PruneHistory(ctx, HistoryRetention); err != nil && ctx.Err() == nil {
			m.log.Warn("prune task history", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
