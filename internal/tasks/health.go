package tasks

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/schmidtfx/home-assistant-fleet/internal/hass"
)

// Health checks: disk space, certificate expiry, unavailable entities, low
// batteries and repeated log errors. None of them has an action.
//
// Titles and details are sent to AI features (redacted), so they only contain
// counts and integration domains. Entity IDs, names and log messages go under
// Source keys that internal/ai does not send ("entities", "messages").

const (
	KindDiskLow             Kind = "disk_low"
	KindCertExpiring        Kind = "cert_expiring"
	KindEntitiesUnavailable Kind = "entities_unavailable"
	KindBatteryLow          Kind = "battery_low"
	KindLogErrors           Kind = "log_errors"
)

// HealthOptions are the thresholds for the health checks.
type HealthOptions struct {
	DiskFreeMinPercent float64 // warn below this much free disk (percent)
	BatteryMinPercent  float64 // report batteries below this level
}

const (
	diskCriticalPercent = 5
	diskCriticalGB      = 2
	certWarnDays        = 21
	certCriticalDays    = 7
	// An entity counts as unavailable after this long, so restarts and short
	// outages do not create tasks.
	unavailableAfter = time.Hour
	// Report an integration when at least this many of its entities are unavailable.
	unavailableMin     = 3
	unavailableWarning = 10
	// Report an integration when it logged this many errors recently.
	logErrorsMin    = 5
	logErrorsWindow = time.Hour
	// At most this many entity IDs / log messages are kept on a task.
	maxListed = 50
)

// FromHostInfo flags low disk space (Supervisor installs only).
func FromHostInfo(instanceID string, h *hass.HostInfo, o HealthOptions) []Task {
	if h == nil || h.DiskTotal <= 0 {
		return nil
	}
	pct := h.DiskFree / h.DiskTotal * 100
	if pct >= o.DiskFreeMinPercent {
		return nil
	}
	sev := SeverityWarning
	if pct < diskCriticalPercent || h.DiskFree < diskCriticalGB {
		sev = SeverityCritical
	}
	t := newTask(instanceID, KindDiskLow, "disk", sev, "Low disk space")
	t.Detail = fmt.Sprintf("%.1f GB free of %.1f GB (%.0f%%). Remove old backups or unused add-ons.", h.DiskFree, h.DiskTotal, pct)
	t.Source = map[string]any{"free_percent": round1(pct), "free_gb": round1(h.DiskFree), "total_gb": round1(h.DiskTotal)}
	return []Task{t}
}

// FromCertExpiry flags a TLS certificate that expires soon or has expired
// (a pinned certificate still connects after it expired).
func FromCertExpiry(instanceID string, notAfter, now time.Time) []Task {
	if notAfter.IsZero() {
		return nil
	}
	left := notAfter.Sub(now)
	days := int(math.Floor(left.Hours() / 24))
	if days >= certWarnDays {
		return nil
	}
	var t Task
	switch {
	case left <= 0:
		t = newTask(instanceID, KindCertExpiring, "cert", SeverityCritical, "TLS certificate expired")
		t.Detail = fmt.Sprintf("The certificate expired on %s.", notAfter.Format("2006-01-02"))
	case days < certCriticalDays:
		t = newTask(instanceID, KindCertExpiring, "cert", SeverityCritical, "TLS certificate expires soon")
	default:
		t = newTask(instanceID, KindCertExpiring, "cert", SeverityWarning, "TLS certificate expires soon")
	}
	if left > 0 {
		t.Detail = fmt.Sprintf("The certificate expires on %s (in %d days). Renew it, then update the pinned fingerprint if you use one.",
			notAfter.Format("2006-01-02"), days)
	}
	t.Source = map[string]any{"days_left": days, "expires": notAfter.UTC()}
	return []Task{t}
}

// FromUnavailableEntities reports integrations with several entities that
// have been unavailable for a while. registry maps entity IDs to integrations;
// entities without a registry entry or that are disabled are skipped.
func FromUnavailableEntities(instanceID string, states []hass.State, registry []hass.RegistryEntry, now time.Time) []Task {
	platform := make(map[string]string, len(registry))
	for _, e := range registry {
		if e.DisabledBy == nil {
			platform[e.EntityID] = e.Platform
		}
	}
	byPlatform := map[string][]string{}
	for _, st := range states {
		p := platform[st.EntityID]
		if st.State != "unavailable" || p == "" {
			continue
		}
		if changed, err := time.Parse(time.RFC3339Nano, st.LastChanged); err != nil || now.Sub(changed) < unavailableAfter {
			continue
		}
		byPlatform[p] = append(byPlatform[p], st.EntityID)
	}
	var out []Task
	for p, ids := range byPlatform {
		if len(ids) < unavailableMin {
			continue
		}
		sort.Strings(ids)
		sev := SeverityInfo
		if len(ids) >= unavailableWarning {
			sev = SeverityWarning
		}
		// The count is in the detail, not the title, so an acknowledgement survives small changes.
		t := newTask(instanceID, KindEntitiesUnavailable, p, sev, "Entities unavailable: "+p)
		t.Detail = fmt.Sprintf("%d entities of the %s integration have been unavailable for more than an hour.", len(ids), p)
		t.Source = map[string]any{"domain": p, "count": len(ids), "entities": capList(ids)}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Title < out[j].Title })
	return out
}

// FromBatteries reports battery sensors below the configured level, as one task per instance.
func FromBatteries(instanceID string, states []hass.State, o HealthOptions) []Task {
	var low []string
	for _, st := range states {
		a := st.Attributes
		if str(a["device_class"]) != "battery" || str(a["unit_of_measurement"]) != "%" {
			continue
		}
		v, err := strconv.ParseFloat(st.State, 64)
		if err != nil || v >= o.BatteryMinPercent {
			continue
		}
		low = append(low, st.EntityID)
	}
	if len(low) == 0 {
		return nil
	}
	sort.Strings(low)
	// Bucketed so an acknowledgement does not reopen for every battery that changes.
	var title string
	switch n := len(low); {
	case n == 1:
		title = "1 low battery"
	case n <= 5:
		title = "2–5 low batteries"
	default:
		title = "6 or more low batteries"
	}
	t := newTask(instanceID, KindBatteryLow, "batteries", SeverityInfo, title)
	t.Detail = fmt.Sprintf("%d battery sensors are below %.0f%%.", len(low), o.BatteryMinPercent)
	t.Source = map[string]any{"count": len(low), "entities": capList(low)}
	return []Task{t}
}

// FromSystemLog reports integrations that logged several errors recently.
func FromSystemLog(instanceID string, entries []hass.LogEntry, now time.Time) []Task {
	type group struct {
		count    int
		critical bool
		last     float64
		messages []string
	}
	groups := map[string]*group{}
	since := float64(now.Add(-logErrorsWindow).Unix())
	for _, e := range entries {
		if (e.Level != "ERROR" && e.Level != "CRITICAL") || e.Timestamp < since {
			continue
		}
		d := loggerDomain(e.Name)
		g := groups[d]
		if g == nil {
			g = &group{}
			groups[d] = g
		}
		// Count is the total since Home Assistant started; for messages that
		// first occurred inside the window that is all recent. Older ones
		// recurred at least once recently, which counts as one.
		if e.FirstOccurred >= since {
			g.count += max(e.Count, 1)
		} else {
			g.count++
		}
		g.critical = g.critical || e.Level == "CRITICAL"
		g.last = max(g.last, e.Timestamp)
		if len(e.Message) > 0 {
			g.messages = append(g.messages, e.Message[0])
		}
	}
	var out []Task
	for d, g := range groups {
		if g.count < logErrorsMin {
			continue
		}
		sev := SeverityInfo
		if g.critical {
			sev = SeverityWarning
		}
		t := newTask(instanceID, KindLogErrors, d, sev, "Repeated errors: "+d)
		t.Detail = fmt.Sprintf("%d errors logged in the last hour, the last at %s.", g.count,
			time.Unix(int64(g.last), 0).UTC().Format("15:04 UTC"))
		t.Source = map[string]any{"domain": d, "count": g.count, "messages": capList(g.messages)}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Title < out[j].Title })
	return out
}

// loggerDomain maps a logger name to the integration it belongs to.
func loggerDomain(name string) string {
	for _, prefix := range []string{"homeassistant.components.", "custom_components."} {
		if rest, ok := strings.CutPrefix(name, prefix); ok {
			d, _, _ := strings.Cut(rest, ".")
			return d
		}
	}
	if name == "" {
		return "unknown"
	}
	return name
}

func capList(s []string) []string {
	if len(s) > maxListed {
		return s[:maxListed]
	}
	return s
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }
