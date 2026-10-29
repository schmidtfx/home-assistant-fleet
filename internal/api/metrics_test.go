package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/schmidtfx/home-assistant-fleet/internal/fleet"
)

const testMetricsToken = "test-metrics-token-0123456789"

func TestMetrics(t *testing.T) {
	c, ha := setup(t)
	c.login()
	resp, body := c.do("POST", "/api/instances", map[string]any{
		"name": `Cabin "north"`, "url": ha.URL, "token": "ha-token", "allow_insecure_http": true})
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	var inst fleet.InstanceView
	json.Unmarshal(body, &inst)

	scrape := func(auth string) (int, string) {
		req, _ := http.NewRequest("GET", c.base+"/metrics", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req) // no session cookie
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var b bytes.Buffer
		b.ReadFrom(resp.Body)
		return resp.StatusCode, b.String()
	}
	for _, auth := range []string{"", "Bearer wrong", "Basic " + testMetricsToken, testMetricsToken} {
		if code, _ := scrape(auth); code != 401 {
			t.Errorf("auth %q: %d, want 401", auth, code)
		}
	}

	var out string
	for i := 0; i < 250 && !strings.Contains(out, `kind="update"`); i++ {
		_, out = scrape("Bearer " + testMetricsToken)
		time.Sleep(20 * time.Millisecond)
	}
	for _, want := range []string{
		`hafleet_instance_up{instance_id="` + inst.ID + `",instance="Cabin \"north\""} 1`,
		`hafleet_instance_info{instance_id="` + inst.ID + `",instance="Cabin \"north\"",ha_version="2026.10.1"} 1`,
		`hafleet_tasks{instance_id="` + inst.ID + `",instance="Cabin \"north\"",kind="update",severity="info",acknowledged="false"} 1`,
		"# TYPE hafleet_actions_total counter",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in:\n%s", want, out)
		}
	}
	// Every line is a comment or "name{labels} value".
	line := regexp.MustCompile(`^(# (HELP|TYPE) .+|[a-z_]+(\{([a-z_]+="([^"\\]|\\.)*",?)+\})? [0-9.e+-]+)$`)
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if !line.MatchString(l) {
			t.Errorf("malformed line: %q", l)
		}
	}
	if strings.Contains(out, "ha-token") || strings.Contains(out, ha.URL) {
		t.Fatal("metrics leak instance settings")
	}
}

func TestMetricsDisabled(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	req.Header.Set("Authorization", "Bearer ")
	s.metrics(rec, req)
	if rec.Code != 404 {
		t.Fatalf("disabled metrics: %d", rec.Code)
	}
}
