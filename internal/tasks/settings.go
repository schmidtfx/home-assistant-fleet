package tasks

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

// Settings switches checks off and overrides thresholds. The same shape is
// used fleet-wide and per instance; unset fields inherit (instance > fleet >
// command-line defaults).
type Settings struct {
	Checks            map[Kind]bool `json:"checks,omitempty"` // kind -> enabled
	DiskFreeMin       *float64      `json:"disk_free_min,omitempty"`
	BatteryMin        *float64      `json:"battery_min,omitempty"`
	BackupMaxAgeHours *float64      `json:"backup_max_age_hours,omitempty"`
}

// Switchable are the task kinds that can be turned off. Updates stay on
// (rollouts, drift and actions depend on them), connectivity is not a check.
var Switchable = []Kind{
	KindRepair, KindRestartRequired, KindReauth, KindSetupFailed, KindBackupStale,
	KindRebootRequired, KindUnhealthy, KindUnsupported,
	KindDiskLow, KindCertExpiring, KindEntitiesUnavailable, KindBatteryLow, KindLogErrors,
}

// Validate rejects unknown or non-switchable kinds and out-of-range thresholds.
func (s Settings) Validate() error {
	for k := range s.Checks {
		if !slices.Contains(Switchable, k) {
			return fmt.Errorf("check %q cannot be switched off", k)
		}
	}
	for _, p := range []*float64{s.DiskFreeMin, s.BatteryMin} {
		if p != nil && (*p <= 0 || *p >= 100) {
			return errors.New("disk and battery thresholds must be between 0 and 100")
		}
	}
	if p := s.BackupMaxAgeHours; p != nil && (*p <= 0 || *p > 24*365) {
		return errors.New("backup max age must be between 0 and 8760 hours")
	}
	return nil
}

// Effective is the resolved configuration for one instance.
type Effective struct {
	Disabled     map[Kind]bool
	Health       HealthOptions
	BackupMaxAge time.Duration
}

// Enabled reports whether tasks of kind k are produced.
func (e Effective) Enabled(k Kind) bool { return !e.Disabled[k] }

// Resolve applies the fleet-wide and then the instance settings on top of defaults.
func Resolve(defaults Effective, fleet, instance Settings) Effective {
	e := Effective{Disabled: map[Kind]bool{}, Health: defaults.Health, BackupMaxAge: defaults.BackupMaxAge}
	for k, v := range defaults.Disabled {
		e.Disabled[k] = v
	}
	for _, s := range []Settings{fleet, instance} {
		for k, on := range s.Checks {
			e.Disabled[k] = !on
		}
		if s.DiskFreeMin != nil {
			e.Health.DiskFreeMinPercent = *s.DiskFreeMin
		}
		if s.BatteryMin != nil {
			e.Health.BatteryMinPercent = *s.BatteryMin
		}
		if s.BackupMaxAgeHours != nil {
			e.BackupMaxAge = time.Duration(*s.BackupMaxAgeHours * float64(time.Hour))
		}
	}
	return e
}
