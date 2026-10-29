package tasks

import (
	"testing"
	"time"
)

func ptr(f float64) *float64 { return &f }

func TestResolveSettings(t *testing.T) {
	defaults := Effective{Health: HealthOptions{DiskFreeMinPercent: 15, BatteryMinPercent: 15}, BackupMaxAge: 7 * 24 * time.Hour}

	e := Resolve(defaults, Settings{}, Settings{})
	if !e.Enabled(KindEntitiesUnavailable) || e.Health != defaults.Health || e.BackupMaxAge != defaults.BackupMaxAge {
		t.Fatalf("no settings should keep defaults: %+v", e)
	}

	fleet := Settings{
		Checks:     map[Kind]bool{KindEntitiesUnavailable: false, KindLogErrors: false},
		BatteryMin: ptr(30), BackupMaxAgeHours: ptr(48),
	}
	inst := Settings{Checks: map[Kind]bool{KindLogErrors: true}, BatteryMin: ptr(10)}
	e = Resolve(defaults, fleet, inst)
	if e.Enabled(KindEntitiesUnavailable) {
		t.Error("fleet-wide off should apply")
	}
	if !e.Enabled(KindLogErrors) {
		t.Error("instance on should override fleet off")
	}
	if e.Health.BatteryMinPercent != 10 || e.Health.DiskFreeMinPercent != 15 || e.BackupMaxAge != 48*time.Hour {
		t.Errorf("thresholds: %+v %v", e.Health, e.BackupMaxAge)
	}
	if fleet.Checks[KindLogErrors] {
		t.Error("Resolve must not modify its inputs")
	}
}

func TestValidateSettings(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    Settings
		ok   bool
	}{
		{"empty", Settings{}, true},
		{"switchable", Settings{Checks: map[Kind]bool{KindBatteryLow: false}, DiskFreeMin: ptr(20), BackupMaxAgeHours: ptr(24)}, true},
		{"update", Settings{Checks: map[Kind]bool{KindUpdate: false}}, false},
		{"connectivity", Settings{Checks: map[Kind]bool{KindConnectivity: false}}, false},
		{"unknown", Settings{Checks: map[Kind]bool{"nope": false}}, false},
		{"battery 0", Settings{BatteryMin: ptr(0)}, false},
		{"disk 100", Settings{DiskFreeMin: ptr(100)}, false},
		{"backup negative", Settings{BackupMaxAgeHours: ptr(-1)}, false},
	} {
		if err := tc.s.Validate(); (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want ok=%v", tc.name, err, tc.ok)
		}
	}
}
