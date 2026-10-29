// Package tasks normalizes Home Assistant data into fleet "open tasks".
package tasks

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/schmidtfx/home-assistant-fleet/internal/hass"
)

type Kind string

const (
	KindUpdate          Kind = "update"
	KindRepair          Kind = "repair"
	KindRebootRequired  Kind = "reboot_required"
	KindRestartRequired Kind = "restart_required"
	KindUnhealthy       Kind = "unhealthy"
	KindUnsupported     Kind = "unsupported"
	KindReauth          Kind = "reauth"
	KindSetupFailed     Kind = "setup_failed"
	KindBackupStale     Kind = "backup_stale"
	KindConnectivity    Kind = "connectivity"
)

// Kinds lists every task kind. Keep it in sync when adding a kind.
var Kinds = []Kind{
	KindUpdate, KindRepair, KindRebootRequired, KindRestartRequired, KindUnhealthy, KindUnsupported,
	KindReauth, KindSetupFailed, KindBackupStale, KindConnectivity,
	KindDiskLow, KindCertExpiring, KindEntitiesUnavailable, KindBatteryLow, KindLogErrors,
}

type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// Action identifiers. These are the only operations the API will execute.
const (
	ActionInstallUpdate = "install_update"
	ActionSkipUpdate    = "skip_update"
	ActionRestartCore   = "restart_core"
	ActionRebootHost    = "reboot_host"
	ActionCreateBackup  = "create_backup"
)

type Task struct {
	ID         string         `json:"id"`
	InstanceID string         `json:"instance_id"`
	Kind       Kind           `json:"kind"`
	Severity   Severity       `json:"severity"`
	Title      string         `json:"title"`
	Detail     string         `json:"detail,omitempty"`
	Source     map[string]any `json:"source,omitempty"`
	Actions    []string       `json:"actions"`
	FirstSeen  time.Time      `json:"first_seen"`
	// Ack is set when a user acknowledged (hid) this task.
	Ack *AckInfo `json:"ack,omitempty"`
}

// AckInfo describes an acknowledgement shown with a task.
type AckInfo struct {
	By        string     `json:"by"`
	Note      string     `json:"note,omitempty"`
	At        time.Time  `json:"at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// Fingerprint identifies the current content of a task. An acknowledgement
// only applies while the fingerprint is unchanged, so a new update version or
// a different failure state brings the task back.
func Fingerprint(t Task) string {
	h := sha256.Sum256([]byte(string(t.Severity) + "|" + t.Title))
	return hex.EncodeToString(h[:8])
}

// ID returns a stable task id so the same issue keeps its identity across syncs.
func ID(instanceID string, kind Kind, key string) string {
	h := sha256.Sum256([]byte(instanceID + "|" + string(kind) + "|" + key))
	return hex.EncodeToString(h[:8])
}

func newTask(instanceID string, kind Kind, key string, sev Severity, title string) Task {
	return Task{ID: ID(instanceID, kind, key), InstanceID: instanceID, Kind: kind, Severity: sev, Title: title, Actions: []string{}}
}

// update entity supported_features bits (homeassistant.components.update.UpdateEntityFeature).
const (
	updateFeatureInstall      = 1
	updateFeatureProgress     = 4
	updateFeatureBackup       = 8
	updateFeatureReleaseNotes = 16
)

var platformUpdates = map[string]bool{
	"update.home_assistant_core_update":             true,
	"update.home_assistant_operating_system_update": true,
	"update.home_assistant_supervisor_update":       true,
}

// FromUpdateEntity returns a task if the update entity has an update pending.
func FromUpdateEntity(instanceID string, st *hass.State) (Task, bool) {
	if st == nil || !strings.HasPrefix(st.EntityID, "update.") || st.State != "on" {
		return Task{}, false
	}
	a := st.Attributes
	name := str(a["title"])
	if name == "" {
		name = str(a["friendly_name"])
	}
	if name == "" {
		name = st.EntityID
	}
	installed, latest := str(a["installed_version"]), str(a["latest_version"])
	sev := SeverityInfo
	if platformUpdates[st.EntityID] {
		sev = SeverityWarning
	}
	t := newTask(instanceID, KindUpdate, st.EntityID, sev, fmt.Sprintf("Update %s to %s", name, latest))
	t.Detail = fmt.Sprintf("%s → %s", installed, latest)
	features := int(num(a["supported_features"]))
	inProgress := false
	switch v := a["in_progress"].(type) {
	case bool:
		inProgress = v
	case float64:
		inProgress = true
	}
	t.Source = map[string]any{
		"entity_id":              st.EntityID,
		"installed_version":      installed,
		"latest_version":         latest,
		"release_url":            str(a["release_url"]),
		"in_progress":            inProgress,
		"supports_backup":        features&updateFeatureBackup != 0,
		"supports_release_notes": features&updateFeatureReleaseNotes != 0,
	}
	if p, ok := a["update_percentage"].(float64); ok {
		t.Source["progress"] = p
	} else if p, ok := a["in_progress"].(float64); ok && features&updateFeatureProgress != 0 {
		t.Source["progress"] = p
	}
	if features&updateFeatureInstall != 0 {
		t.Actions = append(t.Actions, ActionInstallUpdate)
	}
	t.Actions = append(t.Actions, ActionSkipUpdate)
	return t, true
}

// FromRepairs converts active (non-ignored) repair issues. translations maps
// "component.<domain>.issues.<key>.title" to text (may be nil).
func FromRepairs(instanceID string, issues []hass.RepairIssue, translations map[string]string) []Task {
	var out []Task
	for _, is := range issues {
		if is.Ignored || is.DismissedVersion != nil {
			continue
		}
		kind := KindRepair
		if strings.Contains(is.TranslationKey, "restart_required") || strings.Contains(is.IssueID, "restart_required") {
			kind = KindRestartRequired
		}
		sev := SeverityWarning
		if is.Severity == "critical" {
			sev = SeverityCritical
		}
		title := translate(translations, "component."+is.Domain+".issues."+is.TranslationKey+".title", is.TranslationPlaceholders)
		if title == "" {
			title = fmt.Sprintf("%s: %s", is.Domain, humanize(is.TranslationKey))
		}
		t := newTask(instanceID, kind, is.Domain+"/"+is.IssueID, sev, title)
		if is.Breaks != "" {
			t.Detail = "Breaks in Home Assistant " + is.Breaks
		}
		t.Source = map[string]any{
			"domain":         is.Domain,
			"issue_id":       is.IssueID,
			"learn_more_url": is.LearnMoreURL,
			"is_fixable":     is.IsFixable,
		}
		if kind == KindRestartRequired {
			t.Actions = append(t.Actions, ActionRestartCore)
		}
		out = append(out, t)
	}
	return out
}

// FromResolution converts Supervisor resolution center data.
func FromResolution(instanceID string, r *hass.ResolutionInfo) []Task {
	if r == nil {
		return nil
	}
	var out []Task
	reboot := false
	for _, s := range r.Suggestions {
		if s.Type == "execute_reboot" {
			reboot = true
		}
	}
	for _, is := range r.Issues {
		if is.Type == "reboot_required" {
			reboot = true
		}
	}
	if reboot {
		t := newTask(instanceID, KindRebootRequired, "host", SeverityWarning, "Host reboot required")
		t.Detail = "The Supervisor reports that the host must be rebooted to apply changes."
		t.Actions = append(t.Actions, ActionRebootHost)
		out = append(out, t)
	}
	for _, u := range r.Unhealthy {
		t := newTask(instanceID, KindUnhealthy, u, SeverityCritical, "System unhealthy: "+humanize(u))
		t.Source = map[string]any{"reason": u}
		out = append(out, t)
	}
	for _, u := range r.Unsupported {
		t := newTask(instanceID, KindUnsupported, u, SeverityInfo, "Unsupported system: "+humanize(u))
		t.Source = map[string]any{"reason": u}
		out = append(out, t)
	}
	return out
}

var failedEntryStates = map[string]bool{"setup_error": true, "setup_retry": true, "migration_error": true, "failed_unload": true}

// FromConfigEntries reports integrations that failed to set up and pending re-authentication flows.
func FromConfigEntries(instanceID string, entries []hass.ConfigEntry, flows []hass.FlowProgress) []Task {
	var out []Task
	titles := map[string]string{}
	for _, e := range entries {
		titles[e.EntryID] = e.Title
		if e.DisabledBy != nil || !failedEntryStates[e.State] {
			continue
		}
		t := newTask(instanceID, KindSetupFailed, e.EntryID, SeverityWarning,
			fmt.Sprintf("Integration %s (%s) failed: %s", e.Title, e.Domain, humanize(e.State)))
		t.Detail = e.Reason
		t.Source = map[string]any{"entry_id": e.EntryID, "domain": e.Domain, "state": e.State}
		out = append(out, t)
	}
	for _, f := range flows {
		if str(f.Context["source"]) != "reauth" {
			continue
		}
		entryID := str(f.Context["entry_id"])
		name := titles[entryID]
		if name == "" {
			name = f.Handler
		}
		t := newTask(instanceID, KindReauth, f.FlowID, SeverityWarning, fmt.Sprintf("Re-authentication required: %s", name))
		t.Source = map[string]any{"flow_id": f.FlowID, "domain": f.Handler, "entry_id": entryID}
		out = append(out, t)
	}
	return out
}

// FromBackups flags an instance whose newest backup is older than maxAge (or missing).
func FromBackups(instanceID string, b *hass.BackupInfo, maxAge time.Duration, now time.Time) []Task {
	if b == nil {
		return nil
	}
	var newest time.Time
	for _, bk := range b.Backups {
		if ts, err := time.Parse(time.RFC3339Nano, bk.Date); err == nil && ts.After(newest) {
			newest = ts
		}
	}
	if !newest.IsZero() && now.Sub(newest) <= maxAge {
		return nil
	}
	t := newTask(instanceID, KindBackupStale, "backup", SeverityWarning, "No recent backup")
	if newest.IsZero() {
		t.Detail = "No backups found."
	} else {
		t.Detail = fmt.Sprintf("Newest backup is from %s (%d days ago).", newest.Format("2006-01-02"), int(now.Sub(newest).Hours()/24))
		t.Source = map[string]any{"newest_backup": newest}
	}
	t.Actions = append(t.Actions, ActionCreateBackup)
	return []Task{t}
}

// Connectivity builds the task shown when the fleet manager cannot reach an instance.
func Connectivity(instanceID string, err error) Task {
	t := newTask(instanceID, KindConnectivity, "conn", SeverityCritical, "Instance unreachable")
	t.Detail = err.Error()
	return t
}

var severityRank = map[Severity]int{SeverityCritical: 0, SeverityWarning: 1, SeverityInfo: 2}

// Sort orders tasks by severity, then kind, then title.
func Sort(ts []Task) {
	sort.SliceStable(ts, func(i, j int) bool {
		a, b := ts[i], ts[j]
		if severityRank[a.Severity] != severityRank[b.Severity] {
			return severityRank[a.Severity] < severityRank[b.Severity]
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Title < b.Title
	})
}

func translate(tr map[string]string, key string, ph map[string]string) string {
	s := tr[key]
	for k, v := range ph {
		s = strings.ReplaceAll(s, "{"+k+"}", v)
	}
	return s
}

func humanize(s string) string {
	s = strings.ReplaceAll(s, "_", " ")
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func num(v any) float64 {
	f, _ := v.(float64)
	return f
}
