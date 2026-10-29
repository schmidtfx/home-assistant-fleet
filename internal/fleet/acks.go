package fleet

import (
	"context"
	"errors"
	"time"

	"github.com/schmidtfx/home-assistant-fleet/internal/store"
	"github.com/schmidtfx/home-assistant-fleet/internal/tasks"
)

// Acknowledgements hide a task until it resolves, changes, or the optional
// snooze expires. They are kept in memory (m.acks, guarded by m.mu) and in SQLite.

var ErrTaskNotFound = errors.New("task not found")

func (m *Manager) loadAcks(ctx context.Context) error {
	list, err := m.store.ListAcks(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range list {
		if m.acks[a.InstanceID] == nil {
			m.acks[a.InstanceID] = map[string]*store.Ack{}
		}
		m.acks[a.InstanceID][a.TaskID] = a
	}
	return nil
}

// applyAcks sets Ack on tasks with a valid acknowledgement and forgets stale
// ones. With prune (after a successful full sync), acks for tasks that no
// longer exist are dropped too: the problem was resolved.
// Must be called with m.mu held.
func (m *Manager) applyAcks(instanceID string, ts []tasks.Task, prune bool) {
	acks := m.acks[instanceID]
	if len(acks) == 0 {
		return
	}
	now := time.Now()
	present := make(map[string]bool, len(ts))
	for i := range ts {
		t := &ts[i]
		present[t.ID] = true
		a := acks[t.ID]
		if a == nil {
			continue
		}
		if a.Fingerprint != tasks.Fingerprint(*t) || (a.ExpiresAt != nil && now.After(*a.ExpiresAt)) {
			m.dropAck(instanceID, t.ID)
			continue
		}
		t.Ack = &tasks.AckInfo{By: a.Username, Note: a.Note, At: a.CreatedAt, ExpiresAt: a.ExpiresAt}
	}
	if prune {
		for id := range acks {
			if !present[id] {
				m.dropAck(instanceID, id)
			}
		}
	}
}

// dropAck removes an ack from memory and (asynchronously) from the store. Must hold m.mu.
func (m *Manager) dropAck(instanceID, taskID string) {
	delete(m.acks[instanceID], taskID)
	go func() {
		if err := m.store.DeleteAck(context.Background(), instanceID, taskID); err != nil {
			m.log.Warn("delete ack", "err", err)
		}
	}()
}

// Acknowledge hides a task. A zero duration means "until it changes or resolves".
func (m *Manager) Acknowledge(ctx context.Context, instanceID, taskID, user, note string, d time.Duration) (tasks.Task, error) {
	t, ok := m.Task(instanceID, taskID)
	if !ok {
		return tasks.Task{}, ErrTaskNotFound
	}
	a := &store.Ack{InstanceID: instanceID, TaskID: taskID, Fingerprint: tasks.Fingerprint(t),
		Username: user, Note: note, CreatedAt: time.Now()}
	if d > 0 {
		exp := a.CreatedAt.Add(d)
		a.ExpiresAt = &exp
	}
	if err := m.store.PutAck(ctx, a); err != nil {
		return tasks.Task{}, err
	}
	m.mu.Lock()
	if m.acks[instanceID] == nil {
		m.acks[instanceID] = map[string]*store.Ack{}
	}
	m.acks[instanceID][taskID] = a
	m.mu.Unlock()
	m.refreshAcks(instanceID)
	return t, nil
}

// Unacknowledge shows a hidden task again.
func (m *Manager) Unacknowledge(ctx context.Context, instanceID, taskID string) (tasks.Task, error) {
	t, ok := m.Task(instanceID, taskID)
	if !ok {
		return tasks.Task{}, ErrTaskNotFound
	}
	if err := m.store.DeleteAck(ctx, instanceID, taskID); err != nil {
		return tasks.Task{}, err
	}
	m.mu.Lock()
	delete(m.acks[instanceID], taskID)
	m.mu.Unlock()
	m.refreshAcks(instanceID)
	return t, nil
}

// refreshAcks re-applies acknowledgements to an instance's current tasks and broadcasts the view.
func (m *Manager) refreshAcks(instanceID string) {
	m.mu.Lock()
	v, ok := m.views[instanceID]
	if !ok {
		m.mu.Unlock()
		return
	}
	ts := make([]tasks.Task, len(v.Tasks))
	copy(ts, v.Tasks)
	for i := range ts {
		ts[i].Ack = nil
	}
	m.applyAcks(instanceID, ts, false)
	v.Tasks = ts
	cp := *v
	m.mu.Unlock()
	m.publish(Event{Type: "instance", Instance: &cp})
}

// expireAcksLoop re-publishes views when a snooze runs out, so hidden tasks reappear on time.
func (m *Manager) expireAcksLoop(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now := time.Now()
		var due []string
		m.mu.RLock()
		for inst, acks := range m.acks {
			for _, a := range acks {
				if a.ExpiresAt != nil && now.After(*a.ExpiresAt) {
					due = append(due, inst)
					break
				}
			}
		}
		m.mu.RUnlock()
		for _, inst := range due {
			m.refreshAcks(inst)
		}
	}
}
