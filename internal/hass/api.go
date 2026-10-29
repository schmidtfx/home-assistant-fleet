package hass

import (
	"context"
	"encoding/json"
)

// Typed helpers for the HA WebSocket commands the fleet manager uses.

type State struct {
	EntityID    string         `json:"entity_id"`
	State       string         `json:"state"`
	Attributes  map[string]any `json:"attributes"`
	LastChanged string         `json:"last_changed"`
}

type HAConfig struct {
	Version      string   `json:"version"`
	LocationName string   `json:"location_name"`
	Components   []string `json:"components"`
	InternalURL  string   `json:"internal_url"`
	ExternalURL  string   `json:"external_url"`
}

func (c HAConfig) HasComponent(name string) bool {
	for _, x := range c.Components {
		if x == name {
			return true
		}
	}
	return false
}

func (c *Client) GetConfig(ctx context.Context) (*HAConfig, error) {
	var out HAConfig
	return &out, c.Call(ctx, map[string]any{"type": "get_config"}, &out)
}

func (c *Client) GetStates(ctx context.Context) ([]State, error) {
	var out []State
	return out, c.Call(ctx, map[string]any{"type": "get_states"}, &out)
}

type RepairIssue struct {
	Domain                  string            `json:"domain"`
	IssueID                 string            `json:"issue_id"`
	Severity                string            `json:"severity"` // critical|error|warning
	TranslationKey          string            `json:"translation_key"`
	TranslationPlaceholders map[string]string `json:"translation_placeholders"`
	IsFixable               bool              `json:"is_fixable"`
	Ignored                 bool              `json:"ignored"`
	DismissedVersion        *string           `json:"dismissed_version"`
	Created                 string            `json:"created"`
	LearnMoreURL            string            `json:"learn_more_url"`
	Breaks                  string            `json:"breaks_in_ha_version"`
}

func (c *Client) ListRepairIssues(ctx context.Context) ([]RepairIssue, error) {
	var out struct {
		Issues []RepairIssue `json:"issues"`
	}
	return out.Issues, c.Call(ctx, map[string]any{"type": "repairs/list_issues"}, &out)
}

type ConfigEntry struct {
	EntryID    string  `json:"entry_id"`
	Domain     string  `json:"domain"`
	Title      string  `json:"title"`
	State      string  `json:"state"` // loaded|setup_error|setup_retry|migration_error|not_loaded|failed_unload
	Reason     string  `json:"reason"`
	DisabledBy *string `json:"disabled_by"`
}

func (c *Client) ConfigEntries(ctx context.Context) ([]ConfigEntry, error) {
	var out []ConfigEntry
	return out, c.Call(ctx, map[string]any{"type": "config_entries/get"}, &out)
}

type FlowProgress struct {
	FlowID  string         `json:"flow_id"`
	Handler string         `json:"handler"`
	Context map[string]any `json:"context"`
	StepID  string         `json:"step_id"`
}

func (c *Client) FlowsInProgress(ctx context.Context) ([]FlowProgress, error) {
	var out []FlowProgress
	return out, c.Call(ctx, map[string]any{"type": "config_entries/flow/progress"}, &out)
}

// SupervisorAPI proxies a request to the Supervisor REST API (HAOS/Supervised only).
func (c *Client) SupervisorAPI(ctx context.Context, method, endpoint string, data any, out any) error {
	cmd := map[string]any{"type": "supervisor/api", "endpoint": endpoint, "method": method}
	if data != nil {
		cmd["data"] = data
	}
	return c.Call(ctx, cmd, out)
}

type ResolutionInfo struct {
	Unsupported []string `json:"unsupported"`
	Unhealthy   []string `json:"unhealthy"`
	Suggestions []struct {
		UUID      string `json:"uuid"`
		Type      string `json:"type"`    // e.g. execute_reboot
		Context   string `json:"context"` // system|core|addon|...
		Reference string `json:"reference"`
		AutoRun   bool   `json:"auto"`
	} `json:"suggestions"`
	Issues []struct {
		UUID      string `json:"uuid"`
		Type      string `json:"type"` // e.g. reboot_required
		Context   string `json:"context"`
		Reference string `json:"reference"`
	} `json:"issues"`
}

func (c *Client) ResolutionInfo(ctx context.Context) (*ResolutionInfo, error) {
	var out ResolutionInfo
	return &out, c.SupervisorAPI(ctx, "get", "/resolution/info", nil, &out)
}

// SupervisorInfo is the part of Supervisor /info that hafleet uses. The
// endpoint also returns the hostname, which is deliberately not decoded.
type SupervisorInfo struct {
	Supervisor string `json:"supervisor"` // Supervisor version
	HassOS     string `json:"hassos"`     // Home Assistant OS version; empty on Supervised installs
	Machine    string `json:"machine"`    // board type, e.g. raspberrypi4-64
}

func (c *Client) SupervisorInfo(ctx context.Context) (*SupervisorInfo, error) {
	var out SupervisorInfo
	return &out, c.SupervisorAPI(ctx, "get", "/info", nil, &out)
}

type Addon struct {
	Slug            string `json:"slug"`
	Name            string `json:"name"`
	Version         string `json:"version"`
	VersionLatest   string `json:"version_latest"`
	UpdateAvailable bool   `json:"update_available"`
}

func (c *Client) Addons(ctx context.Context) ([]Addon, error) {
	var out struct {
		Addons []Addon `json:"addons"`
	}
	return out.Addons, c.SupervisorAPI(ctx, "get", "/addons", nil, &out)
}

// HostInfo is the disk part of Supervisor /host/info (sizes in GB).
type HostInfo struct {
	DiskTotal float64 `json:"disk_total"`
	DiskUsed  float64 `json:"disk_used"`
	DiskFree  float64 `json:"disk_free"`
}

func (c *Client) HostInfo(ctx context.Context) (*HostInfo, error) {
	var out HostInfo
	return &out, c.SupervisorAPI(ctx, "get", "/host/info", nil, &out)
}

// RegistryEntry is an entity registry entry (only the fields hafleet uses).
type RegistryEntry struct {
	EntityID   string  `json:"entity_id"`
	Platform   string  `json:"platform"` // integration domain that provides the entity
	DisabledBy *string `json:"disabled_by"`
}

func (c *Client) EntityRegistry(ctx context.Context) ([]RegistryEntry, error) {
	var out []RegistryEntry
	return out, c.Call(ctx, map[string]any{"type": "config/entity_registry/list"}, &out)
}

// LogEntry is a system_log entry: one per distinct message, with a count.
type LogEntry struct {
	Name          string   `json:"name"` // logger, e.g. homeassistant.components.zha.core
	Message       []string `json:"message"`
	Level         string   `json:"level"`
	Timestamp     float64  `json:"timestamp"` // last occurrence, Unix seconds
	FirstOccurred float64  `json:"first_occurred"`
	Count         int      `json:"count"`
}

func (c *Client) SystemLog(ctx context.Context) ([]LogEntry, error) {
	var out []LogEntry
	return out, c.Call(ctx, map[string]any{"type": "system_log/list"}, &out)
}

type Backup struct {
	BackupID string `json:"backup_id"`
	Name     string `json:"name"`
	Date     string `json:"date"`
}

type BackupInfo struct {
	Backups []Backup `json:"backups"`
}

func (c *Client) BackupInfo(ctx context.Context) (*BackupInfo, error) {
	var out BackupInfo
	return &out, c.Call(ctx, map[string]any{"type": "backup/info"}, &out)
}

func (c *Client) CallService(ctx context.Context, domain, service string, data map[string]any) error {
	cmd := map[string]any{"type": "call_service", "domain": domain, "service": service}
	if data != nil {
		cmd["service_data"] = data
	}
	return c.Call(ctx, cmd, nil)
}

func (c *Client) ReleaseNotes(ctx context.Context, entityID string) (string, error) {
	var out *string
	if err := c.Call(ctx, map[string]any{"type": "update/release_notes", "entity_id": entityID}, &out); err != nil {
		return "", err
	}
	if out == nil {
		return "", nil
	}
	return *out, nil
}

// User is the Home Assistant user a token belongs to.
type User struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	IsAdmin bool   `json:"is_admin"`
}

// CurrentUser returns the user the token belongs to.
func (c *Client) CurrentUser(ctx context.Context) (*User, error) {
	var out User
	if err := c.Call(ctx, map[string]any{"type": "auth/current_user"}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// IsAdmin reports whether the token belongs to an admin user.
func (c *Client) IsAdmin(ctx context.Context) (bool, error) {
	u, err := c.CurrentUser(ctx)
	if err != nil {
		return false, err
	}
	return u.IsAdmin, nil
}

// StatusEventType is the event hafleet fires to publish its view of an
// instance; the hafleet custom integration turns it into entities.
const StatusEventType = "hafleet_status"

// FireStatusEvent fires a hafleet_status event (needs an admin token). The
// event type is fixed: this is not a general-purpose event passthrough.
func (c *Client) FireStatusEvent(ctx context.Context, data map[string]any) error {
	return c.Call(ctx, map[string]any{"type": "fire_event", "event_type": StatusEventType, "event_data": data}, nil)
}

// SubscribeStateChanged subscribes to state_changed events and calls fn with
// the new state (nil when the entity was removed).
func (c *Client) SubscribeStateChanged(ctx context.Context, fn func(entityID string, newState *State)) error {
	return c.Subscribe(ctx, map[string]any{"type": "subscribe_events", "event_type": "state_changed"},
		func(raw json.RawMessage) {
			var ev struct {
				Data struct {
					EntityID string `json:"entity_id"`
					NewState *State `json:"new_state"`
				} `json:"data"`
			}
			if json.Unmarshal(raw, &ev) == nil {
				fn(ev.Data.EntityID, ev.Data.NewState)
			}
		})
}
