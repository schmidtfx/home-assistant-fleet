package ai

import (
	"context"
	"math"
	"time"

	"github.com/schmidtfx/home-assistant-fleet/internal/fleet"
	"github.com/schmidtfx/home-assistant-fleet/internal/store"
)

// Trends is the task history of AI-enabled instances, reduced to what a model
// needs: per-kind counts and resolution times, and recurring tasks (redacted).
type Trends struct {
	Days      int              `json:"days"`
	Kinds     []TrendKind      `json:"kinds"`
	Recurring []TrendRecurring `json:"recurring"`
}

type TrendKind struct {
	Kind               string  `json:"kind"`
	Opened             int     `json:"opened"`
	Resolved           int     `json:"resolved"`
	MedianHoursToClose float64 `json:"median_hours_to_resolve,omitempty"`
}

type TrendRecurring struct {
	InstanceID string `json:"instance_id"`
	Kind       string `json:"kind"`
	Title      string `json:"title"`
	TimesOpen  int    `json:"times_opened"`
	LastOpened string `json:"last_opened"`
}

// trends summarizes the last days of history for AI-enabled instances only
// (optionally one of them). It returns nil when there is no history.
func (s *Service) trends(ctx context.Context, days int, instanceID string) *Trends {
	views := map[string]fleet.InstanceView{}
	for _, v := range s.aiViews() {
		if instanceID == "" || v.ID == instanceID {
			views[v.ID] = v
		}
	}
	now := time.Now()
	since := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -(days - 1))
	all, err := s.store.ListHistory(ctx, since, "")
	if err != nil {
		return nil
	}
	var entries []store.HistoryEntry
	for _, e := range all {
		if _, ok := views[e.InstanceID]; ok {
			entries = append(entries, e)
		}
	}
	if len(entries) == 0 {
		return nil
	}
	sum := store.Summarize(entries, days, now)
	t := &Trends{Days: days, Kinds: []TrendKind{}, Recurring: []TrendRecurring{}}
	for _, k := range sum.Kinds {
		tk := TrendKind{Kind: k.Kind, Opened: k.Opened, Resolved: k.Resolved}
		if k.Resolved > 0 {
			tk.MedianHoursToClose = math.Round(k.MedianSeconds/360) / 10
		}
		t.Kinds = append(t.Kinds, tk)
	}
	for _, r := range sum.Recurring {
		t.Recurring = append(t.Recurring, TrendRecurring{
			InstanceID: r.InstanceID, Kind: r.Kind, Title: Redact(r.Title, instanceSecrets(views[r.InstanceID])...),
			TimesOpen: r.Count, LastOpened: r.LastOpened.UTC().Format(time.RFC3339),
		})
	}
	return t
}

// safeRollouts lists recent rollouts with only the steps of AI-enabled
// instances; rollouts without any are left out. Who started a rollout is not
// included.
func (s *Service) safeRollouts(ctx context.Context, limit int) []map[string]any {
	views := map[string]fleet.InstanceView{}
	for _, v := range s.aiViews() {
		views[v.ID] = v
	}
	list, err := s.store.ListRollouts(ctx, limit)
	if err != nil {
		return nil
	}
	out := []map[string]any{}
	for _, r := range list {
		var steps []map[string]any
		for _, st := range r.Steps {
			v, ok := views[st.InstanceID]
			if !ok {
				continue
			}
			steps = append(steps, map[string]any{"instance_id": v.ID, "instance": Redact(v.Name, instanceSecrets(v)...),
				"canary": st.Canary, "state": st.State, "message": Redact(st.Message, instanceSecrets(v)...)})
		}
		if steps == nil {
			continue
		}
		out = append(out, map[string]any{"id": r.ID, "update": Redact(r.Title), "entity_id": r.EntityID,
			"target_version": r.TargetVersion, "state": r.State, "created_at": r.CreatedAt.UTC().Format(time.RFC3339),
			"steps": steps})
	}
	return out
}
