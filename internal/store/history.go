package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"
)

// HistoryEntry is one period during which a task was open. A task that
// resolves and later comes back gets a new entry.
type HistoryEntry struct {
	InstanceID string
	TaskID     string
	Kind       string
	Severity   string
	Title      string
	OpenedAt   time.Time
	ClosedAt   *time.Time
}

// HistoryChange is applied to one instance's history in a single transaction.
// Opened entries are inserted, Updated entries change the severity and title
// of the open entry with the same task ID, and Closed task IDs get ClosedAt.
type HistoryChange struct {
	Opened   []HistoryEntry
	Updated  []HistoryEntry
	Closed   []string
	ClosedAt time.Time
}

func (s *Store) ApplyHistory(ctx context.Context, instanceID string, c HistoryChange) error {
	if len(c.Opened)+len(c.Updated)+len(c.Closed) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, e := range c.Opened {
		if _, err := tx.ExecContext(ctx, `INSERT INTO task_history (instance_id, task_id, kind, severity, title, opened_at)
			VALUES (?, ?, ?, ?, ?, ?)`, instanceID, e.TaskID, e.Kind, e.Severity, e.Title, e.OpenedAt.Unix()); err != nil {
			return fmt.Errorf("open history entry: %w", err)
		}
	}
	for _, e := range c.Updated {
		if _, err := tx.ExecContext(ctx, `UPDATE task_history SET severity = ?, title = ?
			WHERE instance_id = ? AND task_id = ? AND closed_at IS NULL`, e.Severity, e.Title, instanceID, e.TaskID); err != nil {
			return fmt.Errorf("update history entry: %w", err)
		}
	}
	for _, id := range c.Closed {
		if _, err := tx.ExecContext(ctx, `UPDATE task_history SET closed_at = ?
			WHERE instance_id = ? AND task_id = ? AND closed_at IS NULL`, c.ClosedAt.Unix(), instanceID, id); err != nil {
			return fmt.Errorf("close history entry: %w", err)
		}
	}
	return tx.Commit()
}

// OpenHistory returns all entries that are still open.
func (s *Store) OpenHistory(ctx context.Context) ([]HistoryEntry, error) {
	return s.queryHistory(ctx, `WHERE closed_at IS NULL`)
}

// ListHistory returns entries that were open at some point since the given
// time, optionally for one instance only.
func (s *Store) ListHistory(ctx context.Context, since time.Time, instanceID string) ([]HistoryEntry, error) {
	where := `WHERE (closed_at IS NULL OR closed_at >= ?)`
	args := []any{since.Unix()}
	if instanceID != "" {
		where += ` AND instance_id = ?`
		args = append(args, instanceID)
	}
	return s.queryHistory(ctx, where+` ORDER BY opened_at`, args...)
}

func (s *Store) queryHistory(ctx context.Context, where string, args ...any) ([]HistoryEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT instance_id, task_id, kind, severity, title, opened_at, closed_at
		FROM task_history `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HistoryEntry
	for rows.Next() {
		var e HistoryEntry
		var opened int64
		var closed sql.NullInt64
		if err := rows.Scan(&e.InstanceID, &e.TaskID, &e.Kind, &e.Severity, &e.Title, &opened, &closed); err != nil {
			return nil, err
		}
		e.OpenedAt = time.Unix(opened, 0)
		if closed.Valid {
			t := time.Unix(closed.Int64, 0)
			e.ClosedAt = &t
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// PruneHistory deletes entries that closed more than maxAge ago.
func (s *Store) PruneHistory(ctx context.Context, maxAge time.Duration) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM task_history WHERE closed_at < ?`, time.Now().Add(-maxAge).Unix())
	return err
}

// ---- summary ----

// HistorySummary is a trend view over task history.
type HistorySummary struct {
	Since     time.Time       `json:"since"`
	Days      []HistoryDay    `json:"days"`
	Kinds     []KindStats     `json:"kinds"`
	Recurring []RecurringTask `json:"recurring"`
}

// HistoryDay counts tasks for one calendar day (server local time). Open is
// the number of tasks still open at the end of the day (or now, for today).
type HistoryDay struct {
	Date           string         `json:"date"` // YYYY-MM-DD
	Opened         int            `json:"opened"`
	Closed         int            `json:"closed"`
	Open           int            `json:"open"`
	OpenBySeverity map[string]int `json:"open_by_severity"`
}

// KindStats describes how often tasks of a kind appeared and how long they
// took to resolve. Durations only count tasks resolved in the window.
type KindStats struct {
	Kind          string  `json:"kind"`
	Opened        int     `json:"opened"`
	Resolved      int     `json:"resolved"`
	MeanSeconds   float64 `json:"mean_seconds"`
	MedianSeconds float64 `json:"median_seconds"`
}

// RecurringTask is a task that opened more than once in the window.
type RecurringTask struct {
	InstanceID string    `json:"instance_id"`
	TaskID     string    `json:"task_id"`
	Kind       string    `json:"kind"`
	Title      string    `json:"title"`
	Count      int       `json:"count"`
	LastOpened time.Time `json:"last_opened"`
}

const maxRecurring = 10

// Summarize builds a HistorySummary for the given number of days, ending now.
// entries must come from ListHistory with the same start.
func Summarize(entries []HistoryEntry, days int, now time.Time) *HistorySummary {
	loc := now.Location()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -(days - 1))
	sum := &HistorySummary{Since: start, Days: []HistoryDay{}, Kinds: []KindStats{}, Recurring: []RecurringTask{}}

	for d := 0; d < days; d++ {
		dayStart := start.AddDate(0, 0, d)
		dayEnd := dayStart.AddDate(0, 0, 1)
		at := dayEnd
		if at.After(now) {
			at = now
		}
		day := HistoryDay{Date: dayStart.Format("2006-01-02"), OpenBySeverity: map[string]int{}}
		for _, e := range entries {
			if !e.OpenedAt.Before(dayStart) && e.OpenedAt.Before(dayEnd) {
				day.Opened++
			}
			if e.ClosedAt != nil && !e.ClosedAt.Before(dayStart) && e.ClosedAt.Before(dayEnd) {
				day.Closed++
			}
			if !e.OpenedAt.After(at) && (e.ClosedAt == nil || e.ClosedAt.After(at)) {
				day.Open++
				day.OpenBySeverity[e.Severity]++
			}
		}
		sum.Days = append(sum.Days, day)
	}

	kinds := map[string]*KindStats{}
	durations := map[string][]float64{}
	type key struct{ inst, task string }
	recurring := map[key]*RecurringTask{}
	for _, e := range entries {
		k := kinds[e.Kind]
		if k == nil {
			k = &KindStats{Kind: e.Kind}
			kinds[e.Kind] = k
		}
		if !e.OpenedAt.Before(start) {
			k.Opened++
			r := recurring[key{e.InstanceID, e.TaskID}]
			if r == nil {
				r = &RecurringTask{InstanceID: e.InstanceID, TaskID: e.TaskID, Kind: e.Kind}
				recurring[key{e.InstanceID, e.TaskID}] = r
			}
			r.Count++
			if !e.OpenedAt.Before(r.LastOpened) {
				r.LastOpened, r.Title, r.Kind = e.OpenedAt, e.Title, e.Kind
			}
		}
		if e.ClosedAt != nil && !e.ClosedAt.Before(start) {
			k.Resolved++
			durations[e.Kind] = append(durations[e.Kind], e.ClosedAt.Sub(e.OpenedAt).Seconds())
		}
	}
	for _, k := range kinds {
		if ds := durations[k.Kind]; len(ds) > 0 {
			sort.Float64s(ds)
			var total float64
			for _, d := range ds {
				total += d
			}
			k.MeanSeconds = total / float64(len(ds))
			if n := len(ds); n%2 == 1 {
				k.MedianSeconds = ds[n/2]
			} else {
				k.MedianSeconds = (ds[n/2-1] + ds[n/2]) / 2
			}
		}
		sum.Kinds = append(sum.Kinds, *k)
	}
	sort.Slice(sum.Kinds, func(i, j int) bool { return sum.Kinds[i].Kind < sum.Kinds[j].Kind })

	for _, r := range recurring {
		if r.Count > 1 {
			sum.Recurring = append(sum.Recurring, *r)
		}
	}
	sort.Slice(sum.Recurring, func(i, j int) bool {
		a, b := sum.Recurring[i], sum.Recurring[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		return a.LastOpened.After(b.LastOpened)
	})
	if len(sum.Recurring) > maxRecurring {
		sum.Recurring = sum.Recurring[:maxRecurring]
	}
	return sum
}
