package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/schmidtfx/home-assistant-fleet/internal/actions"
	"github.com/schmidtfx/home-assistant-fleet/internal/fleet"
	"github.com/schmidtfx/home-assistant-fleet/internal/rollout"
)

// metrics serves Prometheus text exposition. It is off unless a metrics token
// is configured, and needs "Authorization: Bearer <token>": it exposes instance
// names and versions, so it is not public, but scrapers cannot do session login.
func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	if s.MetricsToken == "" {
		http.NotFound(w, r)
		return
	}
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	// Compare digests so the comparison does not depend on the token's length.
	want, have := sha256.Sum256([]byte(s.MetricsToken)), sha256.Sum256([]byte(got))
	if !ok || subtle.ConstantTimeCompare(want[:], have[:]) != 1 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="hafleet metrics"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	s.writeMetrics(w)
}

func (s *Server) writeMetrics(w io.Writer) {
	views := s.Fleet.Views()
	m := &metricWriter{w: w}

	m.family("hafleet_instance_up", "gauge", "Whether hafleet is connected to the instance.")
	for _, v := range views {
		up := 0.0
		if v.Status.State == fleet.StateConnected {
			up = 1
		}
		m.sample("hafleet_instance_up", up, "instance_id", v.ID, "instance", v.Name)
	}
	m.family("hafleet_instance_info", "gauge", "Home Assistant version of the instance (always 1).")
	for _, v := range views {
		if v.Status.HAVersion != "" {
			m.sample("hafleet_instance_info", 1, "instance_id", v.ID, "instance", v.Name, "ha_version", v.Status.HAVersion)
		}
	}
	m.family("hafleet_last_sync_timestamp_seconds", "gauge", "Unix time of the last full sync.")
	for _, v := range views {
		if !v.Status.LastSync.IsZero() {
			m.sample("hafleet_last_sync_timestamp_seconds", float64(v.Status.LastSync.Unix()), "instance_id", v.ID, "instance", v.Name)
		}
	}

	m.family("hafleet_tasks", "gauge", "Open tasks by kind and severity.")
	for _, v := range views {
		type key struct {
			kind, sev string
			acked     bool
		}
		counts := map[key]int{}
		for _, t := range v.Tasks {
			counts[key{string(t.Kind), string(t.Severity), t.Ack != nil}]++
		}
		keys := make([]key, 0, len(counts))
		for k := range counts {
			keys = append(keys, k)
		}
		slices.SortFunc(keys, func(a, b key) int { return strings.Compare(fmt.Sprint(a), fmt.Sprint(b)) })
		for _, k := range keys {
			m.sample("hafleet_tasks", float64(counts[k]), "instance_id", v.ID, "instance", v.Name,
				"kind", k.kind, "severity", k.sev, "acknowledged", fmt.Sprint(k.acked))
		}
	}

	m.family("hafleet_actions_total", "counter", "Finished actions by action and result since start.")
	counts := s.Actions.Counts()
	actionKeys := make([]actions.Count, 0, len(counts))
	for k := range counts {
		actionKeys = append(actionKeys, k)
	}
	slices.SortFunc(actionKeys, func(a, b actions.Count) int {
		return strings.Compare(a.Action+"\x00"+string(a.Status), b.Action+"\x00"+string(b.Status))
	})
	for _, k := range actionKeys {
		m.sample("hafleet_actions_total", float64(counts[k]), "action", k.Action, "result", string(k.Status))
	}

	if s.Rollouts != nil {
		m.family("hafleet_rollouts", "gauge", "Active rollouts by state.")
		counts := s.Rollouts.ActiveCounts()
		for _, st := range []string{rollout.StateRunning, rollout.StatePaused} {
			m.sample("hafleet_rollouts", float64(counts[st]), "state", st)
		}
	}

	if usage := s.AI.UsageTotals(); usage != nil { // nil when AI is disabled
		features := make([]string, 0, len(usage))
		for f := range usage {
			features = append(features, f)
		}
		slices.Sort(features)
		m.family("hafleet_ai_requests_total", "counter", "Model calls by feature since start.")
		for _, f := range features {
			m.sample("hafleet_ai_requests_total", float64(usage[f].Requests), "feature", f)
		}
		m.family("hafleet_ai_tokens_total", "counter", "Model tokens by feature and type since start.")
		for _, f := range features {
			u := usage[f]
			m.sample("hafleet_ai_tokens_total", float64(u.InputTokens), "feature", f, "type", "input")
			m.sample("hafleet_ai_tokens_total", float64(u.OutputTokens), "feature", f, "type", "output")
			m.sample("hafleet_ai_tokens_total", float64(u.CacheReadTokens), "feature", f, "type", "cache_read")
			m.sample("hafleet_ai_tokens_total", float64(u.CacheWriteTokens), "feature", f, "type", "cache_write")
		}
	}
}

type metricWriter struct{ w io.Writer }

func (m *metricWriter) family(name, typ, help string) {
	fmt.Fprintf(m.w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
}

// sample writes one line; labels are name/value pairs.
func (m *metricWriter) sample(name string, value float64, labels ...string) {
	var b strings.Builder
	b.WriteString(name)
	if len(labels) > 0 {
		b.WriteByte('{')
		for i := 0; i+1 < len(labels); i += 2 {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(labels[i])
			b.WriteString(`="`)
			b.WriteString(labelEscaper.Replace(labels[i+1]))
			b.WriteByte('"')
		}
		b.WriteByte('}')
	}
	fmt.Fprintf(m.w, "%s %s\n", b.String(), strconv.FormatFloat(value, 'f', -1, 64))
}

// Label values may contain user-chosen text (instance names).
var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
