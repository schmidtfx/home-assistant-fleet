package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/schmidtfx/home-assistant-fleet/internal/fleet"
	"github.com/schmidtfx/home-assistant-fleet/internal/store"
)

func TestRolloutAPI(t *testing.T) {
	c, ha := setup(t)
	c.login()

	// Maintenance windows are validated and normalized.
	resp, body := c.do("POST", "/api/instances", map[string]any{"name": "Cabin", "url": ha.URL, "token": "ha-token",
		"allow_insecure_http": true, "maint_window": "25:00-01:00"})
	if resp.StatusCode != 400 || !strings.Contains(string(body), "maintenance window") {
		t.Fatalf("bad window: %d %s", resp.StatusCode, body)
	}
	resp, body = c.do("POST", "/api/instances", map[string]any{"name": "Cabin", "url": ha.URL, "token": "ha-token",
		"allow_insecure_http": true, "maint_window": " 2:00 - 5:00 "})
	var inst fleet.InstanceView
	json.Unmarshal(body, &inst)
	if resp.StatusCode != 201 || inst.MaintWindow != "02:00-05:00" {
		t.Fatalf("create with window: %d %s", resp.StatusCode, body)
	}

	for i := 0; i < 250; i++ {
		if v, _ := c.view(inst.ID); len(v.Tasks) == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Wrong version: per-instance reasons.
	resp, body = c.do("POST", "/api/rollouts", map[string]any{"entity_id": "update.demo", "target_version": "9",
		"instances": []string{inst.ID}})
	if resp.StatusCode != 400 || !strings.Contains(string(body), `"instances":{"`+inst.ID+`":"offers version 2, not 9"}`) {
		t.Fatalf("validation: %d %s", resp.StatusCode, body)
	}

	start := time.Now().Add(time.Hour)
	resp, body = c.do("POST", "/api/rollouts", map[string]any{"entity_id": "update.demo", "target_version": "2",
		"instances": []string{inst.ID}, "canary_count": 1, "gate_minutes": 10, "start_at": start})
	var ro store.Rollout
	json.Unmarshal(body, &ro)
	if resp.StatusCode != 201 || ro.State != "running" || ro.Title != "Update Demo to 2" || len(ro.Steps) != 1 {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	if resp, body = c.do("POST", "/api/rollouts/"+ro.ID+"/resume", nil); resp.StatusCode != 409 {
		t.Fatalf("resume running: %d %s", resp.StatusCode, body)
	}
	if resp, body = c.do("POST", "/api/rollouts/"+ro.ID+"/cancel", nil); resp.StatusCode != 200 || !strings.Contains(string(body), `"state":"cancelled"`) {
		t.Fatalf("cancel: %d %s", resp.StatusCode, body)
	}
	resp, body = c.do("GET", "/api/rollouts", nil)
	var list []store.Rollout
	json.Unmarshal(body, &list)
	if resp.StatusCode != 200 || len(list) != 1 || list[0].State != "cancelled" {
		t.Fatalf("list: %s", body)
	}

	var audit []store.AuditEntry
	_, body = c.do("GET", "/api/audit", nil)
	json.Unmarshal(body, &audit)
	got := map[string]bool{}
	for _, e := range audit {
		got[e.Action] = e.Target == "Update Demo to 2" && e.Username == "admin"
	}
	if !got["rollout_create"] || !got["rollout_cancel"] {
		t.Fatalf("audit: %s", body)
	}
}

func (c *client) view(id string) (fleet.InstanceView, bool) {
	_, body := c.do("GET", "/api/instances/"+id, nil)
	var v fleet.InstanceView
	return v, json.Unmarshal(body, &v) == nil
}
