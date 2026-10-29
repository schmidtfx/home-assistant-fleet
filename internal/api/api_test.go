package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/schmidtfx/home-assistant-fleet/internal/actions"
	"github.com/schmidtfx/home-assistant-fleet/internal/auth"
	"github.com/schmidtfx/home-assistant-fleet/internal/fleet"
	"github.com/schmidtfx/home-assistant-fleet/internal/hass/hasstest"
	"github.com/schmidtfx/home-assistant-fleet/internal/rollout"
	"github.com/schmidtfx/home-assistant-fleet/internal/secrets"
	"github.com/schmidtfx/home-assistant-fleet/internal/store"
)

type client struct {
	t    *testing.T
	base string
	http *http.Client
	csrf string
}

func (c *client) do(method, path string, body any) (*http.Response, []byte) {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, r)
	if c.csrf != "" {
		req.Header.Set(auth.CSRFHeader, c.csrf)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, data
}

func setup(t *testing.T) (*client, *hasstest.Server) {
	ha := hasstest.New("ha-token")
	t.Cleanup(ha.Close)
	ha.Result("get_config", map[string]any{"version": "2026.10.1", "location_name": "Cabin", "components": []string{}})
	ha.Result("auth/current_user", map[string]any{"is_admin": true})
	ha.Result("get_states", []any{map[string]any{"entity_id": "update.demo", "state": "on", "attributes": map[string]any{
		"title": "Demo", "installed_version": "1", "latest_version": "2", "supported_features": 1}}})
	ha.Result("repairs/list_issues", map[string]any{"issues": []any{}})
	ha.Result("config_entries/get", []any{})
	ha.Result("config_entries/flow/progress", []any{})
	ha.Result("config/entity_registry/list", []any{})
	ha.Result("system_log/list", []any{})
	ha.Result("call_service", map[string]any{})

	st, _ := store.OpenMemory()
	t.Cleanup(func() { st.Close() })
	h, _ := auth.HashPassword("correct horse battery")
	st.CreateUser(context.Background(), "admin", h)
	for _, role := range []string{"viewer", "operator"} {
		st.CreateUser(context.Background(), role, h) // username = role
		st.SetUserRole(context.Background(), role, role)
	}
	box, _ := secrets.New(bytes.Repeat([]byte{9}, 32))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	fm := fleet.New(st, box, fleet.Options{Logger: log})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	fm.Start(ctx)

	runner := actions.NewRunner(fm, st, log)
	rm := rollout.New(st, fm, runner, rollout.Options{Logger: log})
	rm.Start(ctx)
	srv := &Server{
		Store: st, Box: box, Auth: auth.NewManager(st, false, nil), Fleet: fm,
		Actions: runner, Rollouts: rm, Log: log, MetricsToken: testMetricsToken,
		Static: fstest.MapFS{"index.html": {Data: []byte("<html>app</html>")}},
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	return &client{t: t, base: ts.URL, http: &http.Client{Jar: jar}}, ha
}

func (c *client) login() { c.loginAs("admin") }

func (c *client) loginAs(username string) {
	resp, _ := c.do("POST", "/api/auth/login", map[string]string{"username": username, "password": "correct horse battery"})
	if resp.StatusCode != 200 {
		c.t.Fatalf("login: %d", resp.StatusCode)
	}
	for _, ck := range resp.Cookies() {
		if ck.Name == auth.CSRFCookie {
			c.csrf = ck.Value
		}
	}
}

func TestEndToEnd(t *testing.T) {
	c, ha := setup(t)

	if resp, _ := c.do("GET", "/api/instances", nil); resp.StatusCode != 401 {
		t.Fatalf("unauthenticated: %d", resp.StatusCode)
	}
	if resp, _ := c.do("POST", "/api/auth/login", map[string]string{"username": "admin", "password": "nope"}); resp.StatusCode != 401 {
		t.Fatalf("bad password: %d", resp.StatusCode)
	}
	c.login()

	// CSRF required for writes.
	csrf := c.csrf
	c.csrf = ""
	if resp, _ := c.do("POST", "/api/instances", map[string]any{}); resp.StatusCode != 403 {
		t.Fatalf("missing csrf: %d", resp.StatusCode)
	}
	c.csrf = csrf

	// Validation: http without opt-in is rejected.
	resp, body := c.do("POST", "/api/instances", map[string]any{"name": "Cabin", "url": ha.URL, "token": "ha-token"})
	if resp.StatusCode != 400 || !strings.Contains(string(body), "plain http") {
		t.Fatalf("expected http rejection: %d %s", resp.StatusCode, body)
	}

	// Test connection.
	resp, body = c.do("POST", "/api/instances/test", map[string]any{"name": "Cabin", "url": ha.URL, "token": "ha-token", "allow_insecure_http": true})
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("test: %d %s", resp.StatusCode, body)
	}

	resp, body = c.do("POST", "/api/instances", map[string]any{"name": "Cabin", "url": ha.URL, "token": "ha-token", "allow_insecure_http": true})
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	if strings.Contains(string(body), "ha-token") {
		t.Fatal("token leaked in response")
	}
	var inst fleet.InstanceView
	json.Unmarshal(body, &inst)

	var tasks []fleetTask
	for i := 0; i < 250 && len(tasks) == 0; i++ {
		_, body = c.do("GET", "/api/tasks?kind=update", nil)
		json.Unmarshal(body, &tasks)
		time.Sleep(20 * time.Millisecond)
	}
	if len(tasks) != 1 || tasks[0].InstanceName != "Cabin" {
		t.Fatalf("tasks: %s", body)
	}

	resp, body = c.do("POST", "/api/instances/"+inst.ID+"/actions", map[string]any{"task_id": tasks[0].ID, "action": "reboot_host"})
	if resp.StatusCode != 400 {
		t.Fatalf("disallowed action: %d %s", resp.StatusCode, body)
	}
	resp, body = c.do("POST", "/api/instances/"+inst.ID+"/actions", map[string]any{"task_id": tasks[0].ID, "action": "install_update"})
	if resp.StatusCode != 202 {
		t.Fatalf("action: %d %s", resp.StatusCode, body)
	}

	// Audit: the instance creation, then the action when submitted and when finished.
	var audit, acts []store.AuditEntry
	created := false
	for i := 0; i < 250 && len(acts) < 2; i++ {
		_, body = c.do("GET", "/api/audit", nil)
		audit = nil
		json.Unmarshal(body, &audit)
		acts, created = nil, false
		for _, e := range audit {
			switch e.Action {
			case "install_update":
				acts = append(acts, e)
			case "instance_create":
				created = e.InstanceID == inst.ID && e.Target == "Cabin"
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(acts) != 2 || acts[0].Username != "admin" || acts[0].Target != "update.demo" || !created {
		t.Fatalf("audit: %s", body)
	}
	if strings.Contains(string(body), "ha-token") || strings.Contains(string(body), ha.URL) {
		t.Fatal("instance settings leaked into the audit log")
	}

	// History: the update task is open; bad parameters are rejected.
	resp, body = c.do("GET", "/api/history?days=7&instance="+inst.ID, nil)
	var hist store.HistorySummary
	json.Unmarshal(body, &hist)
	if resp.StatusCode != 200 || len(hist.Days) != 7 || hist.Days[6].Open != 1 {
		t.Fatalf("history: %d %s", resp.StatusCode, body)
	}
	if resp, _ := c.do("GET", "/api/history?days=0", nil); resp.StatusCode != 400 {
		t.Fatalf("history days=0: %d", resp.StatusCode)
	}

	// SPA fallback and security headers.
	resp, body = c.do("GET", "/tasks", nil)
	if resp.StatusCode != 200 || string(body) != "<html>app</html>" || resp.Header.Get("Content-Security-Policy") == "" {
		t.Fatalf("spa: %d %s %v", resp.StatusCode, body, resp.Header)
	}

	if resp, _ := c.do("DELETE", "/api/instances/"+inst.ID, nil); resp.StatusCode != 204 {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	if resp, _ := c.do("POST", "/api/auth/logout", nil); resp.StatusCode != 204 {
		t.Fatalf("logout: %d", resp.StatusCode)
	}
	if resp, _ := c.do("GET", "/api/instances", nil); resp.StatusCode != 401 {
		t.Fatalf("after logout: %d", resp.StatusCode)
	}
}

func TestEventsSnapshot(t *testing.T) {
	c, _ := setup(t)
	c.login()
	req, _ := http.NewRequest("GET", c.base+"/api/events", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := c.http.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	line, _ := bufio.NewReader(resp.Body).ReadString('\n')
	if line != "event: snapshot\n" {
		t.Fatalf("first line %q", line)
	}
}

func TestAckEndpoints(t *testing.T) {
	c, ha := setup(t)
	c.login()
	_, body := c.do("POST", "/api/instances", map[string]any{"name": "Cabin", "url": ha.URL, "token": "ha-token", "allow_insecure_http": true})
	var inst fleet.InstanceView
	json.Unmarshal(body, &inst)
	var ts []fleetTask
	for i := 0; i < 250 && len(ts) == 0; i++ {
		_, body = c.do("GET", "/api/tasks", nil)
		json.Unmarshal(body, &ts)
		time.Sleep(20 * time.Millisecond)
	}
	path := "/api/instances/" + inst.ID + "/tasks/" + ts[0].ID + "/ack"

	if resp, _ := c.do("POST", "/api/instances/"+inst.ID+"/tasks/nope/ack", map[string]any{}); resp.StatusCode != 404 {
		t.Fatalf("unknown task: %d", resp.StatusCode)
	}
	if resp, _ := c.do("POST", path, map[string]any{"duration_hours": -1}); resp.StatusCode != 400 {
		t.Fatalf("bad duration: %d", resp.StatusCode)
	}
	if resp, body := c.do("POST", path, map[string]any{"note": "waiting for vendor fix", "duration_hours": 168}); resp.StatusCode != 204 {
		t.Fatalf("ack: %d %s", resp.StatusCode, body)
	}
	_, body = c.do("GET", "/api/tasks", nil)
	json.Unmarshal(body, &ts)
	if ts[0].Ack == nil || ts[0].Ack.Note != "waiting for vendor fix" || ts[0].Ack.ExpiresAt == nil {
		t.Fatalf("task not acked: %s", body)
	}
	if resp, _ := c.do("DELETE", path, nil); resp.StatusCode != 204 {
		t.Fatalf("unack: %d", resp.StatusCode)
	}
	var audit []store.AuditEntry
	_, body = c.do("GET", "/api/audit", nil)
	json.Unmarshal(body, &audit)
	if len(audit) != 3 || audit[2].Action != "instance_create" || audit[0].Action != "unacknowledge" || audit[1].Action != "acknowledge" || audit[1].Message != "waiting for vendor fix" {
		t.Fatalf("audit: %s", body)
	}
}
