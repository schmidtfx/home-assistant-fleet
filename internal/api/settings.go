package api

import (
	"net/http"

	"github.com/schmidtfx/home-assistant-fleet/internal/auth"
	"github.com/schmidtfx/home-assistant-fleet/internal/store"
	"github.com/schmidtfx/home-assistant-fleet/internal/tasks"
)

type settingsDefaults struct {
	DiskFreeMin       float64 `json:"disk_free_min"`
	BatteryMin        float64 `json:"battery_min"`
	BackupMaxAgeHours float64 `json:"backup_max_age_hours"`
}

type settingsResponse struct {
	Settings tasks.Settings `json:"settings"`
	// Defaults are the command-line values that apply when a threshold is unset.
	Defaults   settingsDefaults `json:"defaults"`
	Switchable []tasks.Kind     `json:"switchable"`
}

func (s *Server) settingsBody() settingsResponse {
	d := s.Fleet.Defaults()
	return settingsResponse{
		Settings: s.Fleet.Settings(),
		Defaults: settingsDefaults{
			DiskFreeMin: d.Health.DiskFreeMinPercent, BatteryMin: d.Health.BatteryMinPercent,
			BackupMaxAgeHours: d.BackupMaxAge.Hours(),
		},
		Switchable: tasks.Switchable,
	}
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.settingsBody())
}

// putSettings replaces the fleet-wide check settings.
func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var body tasks.Settings
	if !decode(w, r, &body) {
		return
	}
	if err := body.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.Fleet.SetSettings(r.Context(), body); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	user := auth.SessionFrom(r.Context()).Username
	s.Log.Info("settings changed", "user", user)
	if err := s.Store.AddAudit(r.Context(), &store.AuditEntry{
		Username: user, Action: "settings_update", Target: "fleet", Result: "ok",
	}); err != nil {
		s.Log.Error("write audit entry", "err", err)
	}
	writeJSON(w, http.StatusOK, s.settingsBody())
}
