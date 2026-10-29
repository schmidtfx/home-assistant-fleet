package ai

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/schmidtfx/home-assistant-fleet/internal/fleet"
	"github.com/schmidtfx/home-assistant-fleet/internal/tasks"
)

// Everything sent to a model goes through this file. Instance URLs, location
// names, tokens, IPs, private hostnames and non-public URLs are removed.

var (
	reURL      = regexp.MustCompile(`(?i)\b(?:https?|wss?)://[^\s"'<>)\]]+`)
	reIPv4     = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}(?::\d+)?\b`)
	reIPv6     = regexp.MustCompile(`\b(?:[0-9a-fA-F]{1,4}:){3,7}[0-9a-fA-F]{1,4}\b`)
	reEmail    = regexp.MustCompile(`\b[\w.+-]+@[\w-]+\.[\w.-]+\b`)
	rePrivHost = regexp.MustCompile(`(?i)\b[\w-]+(?:\.[\w-]+)*\.(?:local|lan|home|internal|localdomain|duckdns\.org|nabu\.casa)([^\w.-]|$)`)
)

// publicDocHosts are URL hosts that may be passed to (and returned from) a model.
var publicDocHosts = []string{"home-assistant.io", "github.com", "hacs.xyz"}

func isPublicDocURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return false
	}
	h := strings.ToLower(u.Hostname())
	for _, p := range publicDocHosts {
		if h == p || strings.HasSuffix(h, "."+p) {
			return true
		}
	}
	return false
}

// Redact removes private identifiers from free text. extra lists additional
// literal strings (e.g. the instance URL host or location name) to remove.
func Redact(s string, extra ...string) string {
	for _, e := range extra {
		if e = strings.TrimSpace(e); len(e) >= 3 {
			s = strings.ReplaceAll(s, e, "[redacted]")
		}
	}
	// Public documentation URLs are kept verbatim: swap them for placeholders
	// so the patterns below cannot touch them, then restore.
	var kept []string
	s = reURL.ReplaceAllStringFunc(s, func(u string) string {
		if isPublicDocURL(u) {
			kept = append(kept, u)
			return fmt.Sprintf("\x00%d\x00", len(kept)-1)
		}
		return "[url]"
	})
	s = reEmail.ReplaceAllString(s, "[email]")
	s = rePrivHost.ReplaceAllString(s, "[host]$1")
	s = reIPv6.ReplaceAllString(s, "[ip]")
	s = reIPv4.ReplaceAllString(s, "[ip]")
	for i, u := range kept {
		s = strings.Replace(s, fmt.Sprintf("\x00%d\x00", i), u, 1)
	}
	return s
}

// instanceSecrets are literals that must never leave hafleet for an instance.
func instanceSecrets(v fleet.InstanceView) []string {
	out := []string{v.URL, v.Status.Location}
	if u, err := url.Parse(v.URL); err == nil {
		out = append(out, u.Host, u.Hostname())
	}
	return out
}

// SafeTask is the model-facing representation of a task.
type SafeTask struct {
	ID        string         `json:"id"`
	Kind      string         `json:"kind"`
	Severity  string         `json:"severity"`
	Title     string         `json:"title"`
	Detail    string         `json:"detail,omitempty"`
	Info      map[string]any `json:"info,omitempty"`
	Actions   []string       `json:"actions"`
	FirstSeen string         `json:"first_seen"`
	// Acknowledged tasks were deliberately hidden by the user.
	Acknowledged bool `json:"acknowledged,omitempty"`
}

// SafeInstance is the model-facing representation of an instance.
type SafeInstance struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	State        string     `json:"state"`
	HAVersion    string     `json:"ha_version,omitempty"`
	Supervisor   bool       `json:"supervisor"`
	Integrations []string   `json:"integrations,omitempty"`
	Tasks        []SafeTask `json:"tasks,omitempty"`
}

// safeSourceKeys are task source fields that carry no private data. Health
// tasks keep entity IDs ("entities") and log text ("messages") out of this list:
// entity names can reveal rooms and people, and log lines hosts and addresses.
var safeSourceKeys = []string{"entity_id", "installed_version", "latest_version", "in_progress", "progress",
	"domain", "issue_id", "is_fixable", "state", "reason", "supports_backup", "learn_more_url", "release_url",
	"free_percent", "free_gb", "total_gb", "days_left", "count"}

func safeTask(t tasks.Task, secrets []string) SafeTask {
	st := SafeTask{
		ID: t.ID, Kind: string(t.Kind), Severity: string(t.Severity),
		Title: Redact(t.Title, secrets...), Detail: Redact(t.Detail, secrets...),
		Actions: t.Actions, FirstSeen: t.FirstSeen.UTC().Format(time.RFC3339), Acknowledged: t.Ack != nil,
	}
	for _, k := range safeSourceKeys {
		v, ok := t.Source[k]
		if !ok || v == nil || v == "" {
			continue
		}
		if s, isStr := v.(string); isStr {
			if strings.HasSuffix(k, "_url") && !isPublicDocURL(s) {
				continue
			}
			v = Redact(s, secrets...)
		}
		if st.Info == nil {
			st.Info = map[string]any{}
		}
		st.Info[k] = v
	}
	return st
}

// SafeInstanceView converts an instance view; withTasks controls whether tasks are included.
func SafeInstanceView(v fleet.InstanceView, withTasks bool) SafeInstance {
	secrets := instanceSecrets(v)
	si := SafeInstance{
		ID: v.ID, Name: Redact(v.Name, secrets...), State: string(v.Status.State),
		HAVersion: v.Status.HAVersion, Supervisor: v.Status.Supervisor, Integrations: v.Status.Integrations,
	}
	if withTasks {
		for _, t := range v.Tasks {
			si.Tasks = append(si.Tasks, safeTask(t, secrets))
		}
	}
	return si
}
