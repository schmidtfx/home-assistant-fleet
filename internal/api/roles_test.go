package api

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"testing"

	"github.com/schmidtfx/home-assistant-fleet/internal/fleet"
)

// TestRoles checks the minimum role of every write route. A 403 means the role
// check rejected the request; any other status means it got through.
func TestRoles(t *testing.T) {
	c, ha := setup(t)
	c.login()
	resp, body := c.do("POST", "/api/instances", map[string]any{
		"name": "Cabin", "url": ha.URL, "token": "ha-token", "allow_insecure_http": true})
	if resp.StatusCode != 201 {
		t.Fatalf("create instance: %d %s", resp.StatusCode, body)
	}
	var inst fleet.InstanceView
	json.Unmarshal(body, &inst)
	id := inst.ID

	routes := []struct {
		method, path string
		body         any
		min          string
	}{
		{"GET", "/api/instances", nil, "viewer"},
		{"GET", "/api/tasks", nil, "viewer"},
		{"GET", "/api/audit", nil, "viewer"},
		{"GET", "/api/history?days=7", nil, "viewer"},
		{"GET", "/api/drift", nil, "viewer"},
		{"GET", "/api/auth/me", nil, "viewer"},
		{"GET", "/api/settings", nil, "viewer"},
		{"PUT", "/api/settings", map[string]any{}, "admin"},
		{"POST", "/api/instances/" + id + "/resync", nil, "operator"},
		{"POST", "/api/instances/" + id + "/actions", map[string]any{"task_id": "nope", "action": "skip_update"}, "operator"},
		{"POST", "/api/instances/" + id + "/tasks/nope/ack", map[string]any{}, "operator"},
		{"DELETE", "/api/instances/" + id + "/tasks/nope/ack", nil, "operator"},
		{"POST", "/api/ai/chat", map[string]any{}, "operator"},
		{"POST", "/api/instances/" + id + "/actions/nope/ai/health", nil, "operator"},
		{"GET", "/api/rollouts", nil, "viewer"},
		{"POST", "/api/rollouts", map[string]any{}, "operator"},
		{"POST", "/api/rollouts/nope/pause", nil, "operator"},
		{"POST", "/api/rollouts/nope/steps/0/skip", nil, "operator"},
		{"POST", "/api/instances/test", map[string]any{}, "admin"},
		{"PATCH", "/api/instances/" + id, map[string]any{}, "admin"},
		{"POST", "/api/instances", map[string]any{}, "admin"},
		{"DELETE", "/api/instances/does-not-exist", nil, "admin"},
	}
	rank := map[string]int{"viewer": 1, "operator": 2, "admin": 3}
	for _, user := range []string{"viewer", "operator", "admin"} {
		jar, _ := cookiejar.New(nil)
		uc := &client{t: t, base: c.base, http: &http.Client{Jar: jar}}
		uc.loginAs(user)
		for _, rt := range routes {
			resp, body := uc.do(rt.method, rt.path, rt.body)
			allowed := rank[user] >= rank[rt.min]
			if got := resp.StatusCode != http.StatusForbidden; got != allowed {
				t.Errorf("%s %s %s: status %d, want allowed=%v (%s)", user, rt.method, rt.path, resp.StatusCode, allowed, body)
			}
		}
	}
}
