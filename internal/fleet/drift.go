package fleet

import (
	"slices"
	"strings"
)

// The drift view compares versions, add-ons and integrations across instances.

type DriftInstance struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Known is false until the instance has synced once; its cells are then
	// unknown and do not count towards drift.
	Known bool `json:"known"`
}

type DriftRow struct {
	Category string `json:"category"` // version | addon | integration
	Key      string `json:"key"`
	Label    string `json:"label"`
	// Values by instance ID; a missing key means "not installed" (or unknown, see DriftInstance.Known).
	Values map[string]string `json:"values"`
	// Majority is the value most known instances share ("" = most do not have
	// it). It is only set when one value is strictly the most common; on a tie
	// (for example two instances that differ) no instance is the odd one out.
	Majority    string `json:"majority"`
	HasMajority bool   `json:"has_majority"`
	Drift       bool   `json:"drift"`
}

type Drift struct {
	Instances []DriftInstance `json:"instances"`
	Rows      []DriftRow      `json:"rows"`
}

// DriftInstalled is the value of an integration row on instances that have it.
const DriftInstalled = "installed"

// BuildDrift compares versions, add-ons and integrations across instances.
func BuildDrift(views []InstanceView) Drift {
	out := Drift{Instances: []DriftInstance{}, Rows: []DriftRow{}}
	rows := map[string]*DriftRow{}
	row := func(cat, key, label string) *DriftRow {
		k := cat + "\x00" + key
		if rows[k] == nil {
			rows[k] = &DriftRow{Category: cat, Key: key, Label: label, Values: map[string]string{}}
		}
		return rows[k]
	}
	var known []string
	for _, v := range views {
		ok := !v.Status.LastSync.IsZero()
		out.Instances = append(out.Instances, DriftInstance{ID: v.ID, Name: v.Name, Known: ok})
		if !ok {
			continue
		}
		known = append(known, v.ID)
		st := v.Status
		for _, ver := range []struct{ key, label, value string }{
			{"core", "Home Assistant", st.HAVersion},
			{"supervisor", "Supervisor", st.SupervisorVersion},
			{"os", "Operating system", st.OSVersion},
		} {
			if ver.value != "" {
				row("version", ver.key, ver.label).Values[v.ID] = ver.value
			}
		}
		for _, a := range st.Addons {
			row("addon", a.Slug, a.Name).Values[v.ID] = a.Version
		}
		for _, d := range st.Integrations {
			row("integration", d, d).Values[v.ID] = DriftInstalled
		}
	}

	for _, r := range rows {
		counts := map[string]int{}
		for _, id := range known {
			counts[r.Values[id]]++ // "" = not installed
		}
		best, tied := -1, false
		for val, n := range counts {
			switch {
			case n > best:
				r.Majority, best, tied = val, n, false
			case n == best:
				tied = true
			}
		}
		r.HasMajority = !tied
		if tied {
			r.Majority = ""
		}
		r.Drift = len(counts) > 1
		out.Rows = append(out.Rows, *r)
	}
	order := map[string]int{"version": 0, "addon": 1, "integration": 2}
	slices.SortFunc(out.Rows, func(a, b DriftRow) int {
		if a.Category != b.Category {
			return order[a.Category] - order[b.Category]
		}
		if a.Category == "version" {
			return slices.Index([]string{"core", "supervisor", "os"}, a.Key) - slices.Index([]string{"core", "supervisor", "os"}, b.Key)
		}
		return strings.Compare(strings.ToLower(a.Label), strings.ToLower(b.Label))
	})
	return out
}
