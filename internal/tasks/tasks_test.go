package tasks

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/schmidtfx/home-assistant-fleet/internal/hass"
)

func load(t *testing.T, name string, v any) {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

func TestUpdates(t *testing.T) {
	var states []hass.State
	load(t, "get_states.json", &states)
	var got []Task
	for i := range states {
		if task, ok := FromUpdateEntity("i1", &states[i]); ok {
			got = append(got, task)
		}
	}
	if len(got) != 2 {
		t.Fatalf("want 2 update tasks, got %d: %+v", len(got), got)
	}
	core := got[0]
	if core.Severity != SeverityWarning || core.Title != "Update Home Assistant Core to 2026.10.2" {
		t.Errorf("core task: %+v", core)
	}
	if !slices.Contains(core.Actions, ActionInstallUpdate) || core.Source["supports_backup"] != true {
		t.Errorf("core actions/source: %+v", core)
	}
	addon := got[1]
	if addon.Severity != SeverityInfo || slices.Contains(addon.Actions, ActionInstallUpdate) {
		t.Errorf("addon without INSTALL feature should not be installable: %+v", addon)
	}
	if core.ID != ID("i1", KindUpdate, "update.home_assistant_core_update") {
		t.Error("task id not stable")
	}
}

func TestRepairs(t *testing.T) {
	var resp struct {
		Issues []hass.RepairIssue `json:"issues"`
	}
	load(t, "repairs.json", &resp)
	tr := map[string]string{"component.hacs.issues.restart_required.title": "Restart required for {name}"}
	got := FromRepairs("i1", resp.Issues, tr)
	if len(got) != 2 {
		t.Fatalf("want 2 (ignored skipped), got %d: %+v", len(got), got)
	}
	if got[0].Kind != KindRepair || got[0].Severity != SeverityCritical || got[0].Title != "homeassistant: Deprecated yaml" {
		t.Errorf("repair: %+v", got[0])
	}
	if got[1].Kind != KindRestartRequired || got[1].Title != "Restart required for Mushroom" ||
		!slices.Contains(got[1].Actions, ActionRestartCore) {
		t.Errorf("restart: %+v", got[1])
	}
}

func TestResolution(t *testing.T) {
	var r hass.ResolutionInfo
	load(t, "resolution.json", &r)
	got := FromResolution("i1", &r)
	kinds := map[Kind]int{}
	for _, x := range got {
		kinds[x.Kind]++
	}
	if kinds[KindRebootRequired] != 1 || kinds[KindUnhealthy] != 1 || kinds[KindUnsupported] != 1 {
		t.Fatalf("got %+v", kinds)
	}
}

func TestConfigEntries(t *testing.T) {
	disabled := "user"
	entries := []hass.ConfigEntry{
		{EntryID: "e1", Domain: "hue", Title: "Hue", State: "loaded"},
		{EntryID: "e2", Domain: "mqtt", Title: "MQTT", State: "setup_retry", Reason: "timeout"},
		{EntryID: "e3", Domain: "x", Title: "X", State: "setup_error", DisabledBy: &disabled},
		{EntryID: "e4", Domain: "nest", Title: "Nest", State: "loaded"},
	}
	flows := []hass.FlowProgress{
		{FlowID: "f1", Handler: "nest", Context: map[string]any{"source": "reauth", "entry_id": "e4"}},
		{FlowID: "f2", Handler: "cast", Context: map[string]any{"source": "zeroconf"}},
	}
	got := FromConfigEntries("i1", entries, flows)
	if len(got) != 2 || got[0].Kind != KindSetupFailed || got[1].Kind != KindReauth || got[1].Title != "Re-authentication required: Nest" {
		t.Fatalf("got %+v", got)
	}
}

func TestBackups(t *testing.T) {
	now := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	fresh := &hass.BackupInfo{Backups: []hass.Backup{{Date: "2026-10-27T03:00:00.000Z"}}}
	if got := FromBackups("i1", fresh, 7*24*time.Hour, now); len(got) != 0 {
		t.Fatalf("fresh backup flagged: %+v", got)
	}
	if got := FromBackups("i1", fresh, 24*time.Hour, now); len(got) != 1 {
		t.Fatalf("stale backup not flagged")
	}
	if got := FromBackups("i1", &hass.BackupInfo{}, 24*time.Hour, now); len(got) != 1 || got[0].Detail != "No backups found." {
		t.Fatalf("missing backups not flagged: %+v", got)
	}
}

func TestSort(t *testing.T) {
	ts := []Task{
		{Severity: SeverityInfo, Kind: KindUpdate, Title: "b"},
		{Severity: SeverityCritical, Kind: KindUnhealthy},
		{Severity: SeverityInfo, Kind: KindUpdate, Title: "a"},
	}
	Sort(ts)
	if ts[0].Severity != SeverityCritical || ts[1].Title != "a" {
		t.Fatalf("bad order: %+v", ts)
	}
}
