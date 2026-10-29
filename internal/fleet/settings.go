package fleet

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/schmidtfx/home-assistant-fleet/internal/store"
	"github.com/schmidtfx/home-assistant-fleet/internal/tasks"
)

// settingsKey is the store key of the fleet-wide check settings.
const settingsKey = "checks"

func (m *Manager) loadSettings(ctx context.Context) error {
	raw, err := m.store.GetSetting(ctx, settingsKey)
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}
	var s tasks.Settings
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			return fmt.Errorf("decode settings: %w", err)
		}
	}
	m.setMu.Lock()
	m.settings = s
	m.setMu.Unlock()
	return nil
}

// Settings returns the fleet-wide check settings.
func (m *Manager) Settings() tasks.Settings {
	m.setMu.RLock()
	defer m.setMu.RUnlock()
	return m.settings
}

// Defaults returns the configuration that applies when nothing is overridden
// (from command-line flags).
func (m *Manager) Defaults() tasks.Effective {
	return tasks.Effective{Health: m.opts.Health, BackupMaxAge: m.opts.BackupMaxAge}
}

// SetSettings validates, stores and applies the fleet-wide check settings.
// Every connected instance resyncs so the change shows up right away.
func (m *Manager) SetSettings(ctx context.Context, s tasks.Settings) error {
	if err := s.Validate(); err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := m.store.PutSetting(ctx, settingsKey, string(b)); err != nil {
		return fmt.Errorf("save settings: %w", err)
	}
	m.setMu.Lock()
	m.settings = s
	m.setMu.Unlock()
	m.mu.RLock()
	ids := make([]string, 0, len(m.workers))
	for id := range m.workers {
		ids = append(ids, id)
	}
	m.mu.RUnlock()
	for _, id := range ids {
		m.Resync(id)
	}
	return nil
}

// effective resolves the settings that apply to an instance.
func (m *Manager) effective(in *store.Instance) tasks.Effective {
	return tasks.Resolve(m.Defaults(), m.Settings(), m.instanceSettings(in))
}

// instanceSettings decodes an instance's overrides. The API validates them
// before they are stored, so a decode error only means a damaged row.
func (m *Manager) instanceSettings(in *store.Instance) tasks.Settings {
	var s tasks.Settings
	if in.Settings != "" {
		if err := json.Unmarshal([]byte(in.Settings), &s); err != nil {
			m.log.Warn("ignoring invalid instance settings", "instance", in.Name, "err", err)
			return tasks.Settings{}
		}
	}
	return s
}
