package ai

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/schmidtfx/home-assistant-fleet/internal/actions"
	"github.com/schmidtfx/home-assistant-fleet/internal/tasks"
)

func TestHealthCheck(t *testing.T) {
	verdict := "suspect"
	e := setup(t, true, func(Request) (*Response, error) {
		return JSONReply(map[string]any{"verdict": verdict, "findings": []string{"MQTT failed after the update"},
			"steps": []string{"Restore the backup"}, "docs_url": "https://evil.example/x"}), nil
	})
	// History stores seconds: let the ZHA failure (already open) be clearly older than the install.
	time.Sleep(1100 * time.Millisecond)
	started := time.Now()

	// After the update: MQTT fails to set up and ZHA logs errors mentioning an address and the location.
	e.ha.Result("config_entries/get", []any{
		map[string]any{"entry_id": "e1", "domain": "zha", "title": "ZHA", "state": "setup_retry", "reason": "Cannot connect to 192.168.1.50"},
		map[string]any{"entry_id": "e2", "domain": "mqtt", "title": "MQTT", "state": "setup_error", "reason": "Unsupported option state_topic"},
	})
	now := float64(time.Now().Unix())
	e.ha.Result("system_log/list", []any{map[string]any{"name": "homeassistant.components.zha", "level": "ERROR",
		"message":   []string{"Lost coordinator at 192.168.1.50 (" + location + ") " + strings.Repeat("x", 600)},
		"timestamp": now, "first_occurred": now, "count": 9}})
	e.fleet.Resync("i1")
	for i := 0; i < 250; i++ {
		if v, _ := e.fleet.View("i1"); len(v.Tasks) == 4 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	rec := actions.Record{ID: "a1", InstanceID: "i1", Action: tasks.ActionInstallUpdate, Status: actions.StatusSucceeded,
		Target: "update.home_assistant_core_update", FromVersion: "2026.10.1", ToVersion: "2026.11.0",
		StartedAt: started, FinishedAt: started}
	h, err := e.svc.HealthCheck(context.Background(), "admin", rec, false)
	if err != nil {
		t.Fatal(err)
	}
	if h.Verdict != "suspect" || h.DocsURL != "" || h.ActionID != "a1" {
		t.Fatalf("health: %+v", h)
	}

	reqs := e.fake.Requests()
	text := reqs[len(reqs)-1].Messages[0].Text
	var in struct {
		Update   map[string]any `json:"update"`
		NewTasks []SafeTask     `json:"new_tasks_since_update"`
		Logs     []struct {
			Integration string   `json:"integration"`
			New         bool     `json:"new_since_update"`
			Messages    []string `json:"messages"`
		} `json:"recent_log_errors"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(text, "Installation after the update:\n")), &in); err != nil {
		t.Fatalf("input: %v\n%s", err, text)
	}
	var titles []string
	for _, nt := range in.NewTasks {
		titles = append(titles, nt.Title)
	}
	joined := strings.Join(titles, "|")
	if !strings.Contains(joined, "MQTT") || strings.Contains(joined, "ZHA") {
		t.Fatalf("new tasks should be MQTT (and the log task), not the older ZHA failure: %v", titles)
	}
	if in.Update["to_version"] != "2026.11.0" || len(in.Logs) != 1 || !in.Logs[0].New || in.Logs[0].Integration != "zha" {
		t.Fatalf("update/logs: %+v %+v", in.Update, in.Logs)
	}
	if n := len([]rune(in.Logs[0].Messages[0])); n > maxLogMessageRunes+1 {
		t.Fatalf("log message not truncated: %d runes", n)
	}
	e.assertNoLeaks(t) // the address and the location in the log line are redacted

	// Unknown verdicts are rejected rather than treated as ok.
	verdict = "fine"
	if _, err := e.svc.HealthCheck(context.Background(), "admin", rec, true); err == nil {
		t.Fatal("unknown verdict accepted")
	}
	running := rec
	running.Status = actions.StatusRunning
	if _, err := e.svc.HealthCheck(context.Background(), "admin", running, false); !errors.Is(err, ErrNotAnUpdate) {
		t.Fatalf("running action: %v", err)
	}
}

func TestHealthCheckDisabledInstance(t *testing.T) {
	e := setup(t, false, nil)
	rec := actions.Record{ID: "a1", InstanceID: "i1", Action: tasks.ActionInstallUpdate, Status: actions.StatusSucceeded}
	if _, err := e.svc.HealthCheck(context.Background(), "admin", rec, false); !errors.Is(err, ErrDisabled) {
		t.Fatalf("expected ErrDisabled, got %v", err)
	}
	if len(e.fake.Requests()) != 0 {
		t.Fatal("a disabled instance was sent to the model")
	}
}
