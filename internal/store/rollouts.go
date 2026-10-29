package store

import (
	"context"
	"database/sql"

	"fmt"
	"time"
)

// Rollout installs one update on several instances, one after another.
type Rollout struct {
	ID            string        `json:"id"`
	CreatedBy     string        `json:"created_by"`
	EntityID      string        `json:"entity_id"`
	Title         string        `json:"title"` // task title when created, for display
	TargetVersion string        `json:"target_version"`
	Backup        bool          `json:"backup"`
	CanaryCount   int           `json:"canary_count"`
	GateMinutes   int           `json:"gate_minutes"`
	AIGate        bool          `json:"ai_gate"` // also ask the AI health check after each install
	StartAt       *time.Time    `json:"start_at,omitempty"`
	State         string        `json:"state"`
	Message       string        `json:"message,omitempty"`
	CreatedAt     time.Time     `json:"created_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
	Steps         []RolloutStep `json:"steps"`
}

// RolloutStep is one instance of a rollout.
type RolloutStep struct {
	Ord        int        `json:"ord"`
	InstanceID string     `json:"instance_id"`
	Canary     bool       `json:"canary"`
	State      string     `json:"state"`
	ActionID   string     `json:"action_id,omitempty"`
	Message    string     `json:"message,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

func (s *Store) CreateRollout(ctx context.Context, r *Rollout) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO rollouts (id, created_by, entity_id, title, target_version, backup,
		canary_count, gate_minutes, ai_gate, start_at, state, message, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.CreatedBy, r.EntityID, r.Title, r.TargetVersion, r.Backup, r.CanaryCount, r.GateMinutes, r.AIGate,
		unixOrNil(r.StartAt), r.State, r.Message, r.CreatedAt.Unix(), r.UpdatedAt.Unix())
	if err != nil {
		return fmt.Errorf("insert rollout: %w", err)
	}
	for _, st := range r.Steps {
		if _, err := tx.ExecContext(ctx, `INSERT INTO rollout_steps (rollout_id, ord, instance_id, canary, state)
			VALUES (?, ?, ?, ?, ?)`, r.ID, st.Ord, st.InstanceID, st.Canary, st.State); err != nil {
			return fmt.Errorf("insert rollout step: %w", err)
		}
	}
	return tx.Commit()
}

// SaveRollout writes the rollout's state and all of its steps.
func (s *Store) SaveRollout(ctx context.Context, r *Rollout) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE rollouts SET state = ?, message = ?, updated_at = ? WHERE id = ?`,
		r.State, r.Message, r.UpdatedAt.Unix(), r.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	for _, st := range r.Steps {
		if _, err := tx.ExecContext(ctx, `UPDATE rollout_steps SET state = ?, action_id = ?, message = ?,
			started_at = ?, finished_at = ? WHERE rollout_id = ? AND ord = ?`,
			st.State, st.ActionID, st.Message, unixOrNil(st.StartedAt), unixOrNil(st.FinishedAt), r.ID, st.Ord); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const rolloutCols = `id, created_by, entity_id, title, target_version, backup, canary_count, gate_minutes, ai_gate, start_at,
	state, message, created_at, updated_at`

// ListRollouts returns rollouts newest first, with their steps.
func (s *Store) ListRollouts(ctx context.Context, limit int) ([]*Rollout, error) {
	return s.queryRollouts(ctx, `ORDER BY created_at DESC, id LIMIT ?`, limit)
}

// RolloutsInState returns rollouts in any of the given states, oldest first.
func (s *Store) RolloutsInState(ctx context.Context, states ...string) ([]*Rollout, error) {
	var out []*Rollout
	for _, st := range states {
		list, err := s.queryRollouts(ctx, `WHERE state = ? ORDER BY created_at, id`, st)
		if err != nil {
			return nil, err
		}
		out = append(out, list...)
	}
	return out, nil
}

func (s *Store) GetRollout(ctx context.Context, id string) (*Rollout, error) {
	list, err := s.queryRollouts(ctx, `WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, ErrNotFound
	}
	return list[0], nil
}

func (s *Store) queryRollouts(ctx context.Context, tail string, args ...any) ([]*Rollout, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+rolloutCols+` FROM rollouts `+tail, args...)
	if err != nil {
		return nil, err
	}
	var out []*Rollout
	for rows.Next() {
		var r Rollout
		var start sql.NullInt64
		var created, updated int64
		if err := rows.Scan(&r.ID, &r.CreatedBy, &r.EntityID, &r.Title, &r.TargetVersion, &r.Backup, &r.CanaryCount,
			&r.GateMinutes, &r.AIGate, &start, &r.State, &r.Message, &created, &updated); err != nil {
			rows.Close()
			return nil, err
		}
		r.StartAt = timeOrNil(start)
		r.CreatedAt, r.UpdatedAt = time.Unix(created, 0), time.Unix(updated, 0)
		out = append(out, &r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Steps are loaded after the rollouts query is closed (one connection).
	for _, r := range out {
		if r.Steps, err = s.rolloutSteps(ctx, r.ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) rolloutSteps(ctx context.Context, id string) ([]RolloutStep, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ord, instance_id, canary, state, action_id, message, started_at, finished_at
		FROM rollout_steps WHERE rollout_id = ? ORDER BY ord`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RolloutStep
	for rows.Next() {
		var st RolloutStep
		var started, finished sql.NullInt64
		if err := rows.Scan(&st.Ord, &st.InstanceID, &st.Canary, &st.State, &st.ActionID, &st.Message, &started, &finished); err != nil {
			return nil, err
		}
		st.StartedAt, st.FinishedAt = timeOrNil(started), timeOrNil(finished)
		out = append(out, st)
	}
	return out, rows.Err()
}

func unixOrNil(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Unix()
}

func timeOrNil(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.Unix(v.Int64, 0)
	return &t
}
