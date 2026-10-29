package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/schmidtfx/home-assistant-fleet/internal/tasks"
)

func TestRedact(t *testing.T) {
	in := "Cannot reach http://192.168.1.20:8123/api from ha.local or mqtt.home (10.0.0.5), mail me@example.com, " +
		"see https://www.home-assistant.io/integrations/mqtt and fe80:0:0:0:200:f8ff:fe21:67cf. At Secret Cabin."
	got := Redact(in, "Secret Cabin")
	for _, leak := range []string{"192.168", "10.0.0.5", "ha.local", "mqtt.home", "me@example.com", "Secret Cabin", "fe80"} {
		if strings.Contains(got, leak) {
			t.Errorf("leaked %q: %s", leak, got)
		}
	}
	if !strings.Contains(got, "https://www.home-assistant.io/integrations/mqtt") {
		t.Errorf("public docs URL removed: %s", got)
	}
	if !strings.Contains(Redact("update.home_assistant_core_update 2026.10.2"), "2026.10.2") {
		t.Error("versions must survive redaction")
	}
}

func TestSafeTaskHealthKinds(t *testing.T) {
	// Entity IDs and log lines are shown in the UI but never sent to a model.
	battery := tasks.Task{Kind: tasks.KindBatteryLow, Title: "2–5 low batteries", Source: map[string]any{
		"count": 2, "entities": []string{"sensor.kids_room_battery", "sensor.front_door_battery"}}}
	logs := tasks.Task{Kind: tasks.KindLogErrors, Title: "Repeated errors: zha", Source: map[string]any{
		"domain": "zha", "count": 5, "messages": []string{"Failed to reach 192.168.1.20"}}}
	disk := tasks.Task{Kind: tasks.KindDiskLow, Title: "Low disk space", Source: map[string]any{
		"free_percent": 4.2, "free_gb": 1.2, "total_gb": 28.6}}
	for _, task := range []tasks.Task{battery, logs, disk} {
		st := safeTask(task, nil)
		b, _ := json.Marshal(st)
		for _, leak := range []string{"entities", "messages", "kids_room", "192.168"} {
			if strings.Contains(string(b), leak) {
				t.Errorf("%s: leaked %q: %s", task.Kind, leak, b)
			}
		}
	}
	if st := safeTask(disk, nil); st.Info["free_percent"] != 4.2 {
		t.Errorf("numeric facts must be kept: %+v", st.Info)
	}
	if st := safeTask(logs, nil); st.Info["count"] != 5 || st.Info["domain"] != "zha" {
		t.Errorf("count and domain must be kept: %+v", st.Info)
	}
}

func TestBreakingSection(t *testing.T) {
	text := "# Release 2026.10\nintro\n## Backward-incompatible changes\n### MQTT\nstate_topic removed\n### Hue\nnew bridge\n## All changes\nlots"
	got, ok := BreakingSection(text)
	if !ok || !strings.Contains(got, "state_topic removed") || !strings.Contains(got, "Hue") || strings.Contains(got, "All changes") {
		t.Fatalf("got %q", got)
	}
	if _, ok := BreakingSection("no headings here"); ok {
		t.Fatal("expected no section")
	}
}

func TestHTMLToText(t *testing.T) {
	got := HTMLToText(`<html><head><script>evil()</script></head><body><nav>menu</nav>
		<h2>Backward-incompatible changes</h2><h3>MQTT</h3><p>The <b>state_topic</b> option was removed.</p>
		<ul><li>one</li><li>two</li></ul></body></html>`)
	for _, want := range []string{"## Backward-incompatible changes", "### MQTT", "The state_topic option was removed.", "- one"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "evil") || strings.Contains(got, "menu") {
		t.Errorf("script/nav not stripped: %q", got)
	}
}

func TestNotesFetcherAllowlist(t *testing.T) {
	f := NewNotesFetcher()
	for _, u := range []string{"http://www.home-assistant.io/blog", "https://192.168.1.1/x", "https://evil.example/notes", "file:///etc/passwd"} {
		if _, err := f.Fetch(context.Background(), u); err == nil || !strings.Contains(err.Error(), "not allowed") {
			t.Errorf("%s: expected rejection, got %v", u, err)
		}
	}
}

func TestNotesFetcherSizeCap(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(strings.Repeat("x", 2048)))
	}))
	defer srv.Close()
	f := NewNotesFetcher()
	f.Hosts = []string{strings.TrimPrefix(srv.URL, "https://")}
	f.MaxBytes = 1024
	f.client.Transport = srv.Client().Transport
	if _, err := f.Fetch(context.Background(), srv.URL); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("expected size error, got %v", err)
	}
	f.MaxBytes = 4096
	if text, err := f.Fetch(context.Background(), srv.URL); err != nil || len(text) != 2048 {
		t.Fatalf("fetch: %d %v", len(text), err)
	}
}
