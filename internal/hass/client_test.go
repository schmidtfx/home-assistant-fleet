package hass_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/schmidtfx/home-assistant-fleet/internal/hass"
	"github.com/schmidtfx/home-assistant-fleet/internal/hass/hasstest"
)

func dial(t *testing.T, s *hasstest.Server, token string) (*hass.Client, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return hass.Dial(ctx, hass.Options{URL: s.URL, Token: token, AllowInsecureHTTP: true})
}

func TestAuthAndCall(t *testing.T) {
	s := hasstest.New("tok")
	defer s.Close()
	s.Result("get_states", []map[string]any{{"entity_id": "update.core", "state": "on", "attributes": map[string]any{}}})

	c, err := dial(t, s, "tok")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.HAVersion != s.Version {
		t.Fatalf("version %q", c.HAVersion)
	}
	ctx := context.Background()
	states, err := c.GetStates(ctx)
	if err != nil || len(states) != 1 || states[0].EntityID != "update.core" {
		t.Fatalf("states %+v %v", states, err)
	}
	if err := c.Ping(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestAuthInvalid(t *testing.T) {
	s := hasstest.New("tok")
	defer s.Close()
	if _, err := dial(t, s, "wrong"); !errors.Is(err, hass.ErrAuth) {
		t.Fatalf("expected ErrAuth, got %v", err)
	}
}

func TestCommandError(t *testing.T) {
	s := hasstest.New("tok")
	defer s.Close()
	s.Handle("repairs/list_issues", func(map[string]any) (any, string) { return nil, "unauthorized" })
	c, err := dial(t, s, "tok")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var herr *hass.Error
	if _, err := c.ListRepairIssues(context.Background()); !errors.As(err, &herr) || herr.Code != "unauthorized" {
		t.Fatalf("expected hass.Error, got %v", err)
	}
}

func TestConcurrentCalls(t *testing.T) {
	s := hasstest.New("tok")
	defer s.Close()
	s.Handle("echo", func(cmd map[string]any) (any, string) { return cmd["v"], "" })
	c, err := dial(t, s, "tok")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var got float64
			if err := c.Call(context.Background(), map[string]any{"type": "echo", "v": i}, &got); err != nil || int(got) != i {
				t.Errorf("call %d: got %v err %v", i, got, err)
			}
		}(i)
	}
	wg.Wait()
}

func TestSubscribeAndDisconnect(t *testing.T) {
	s := hasstest.New("tok")
	defer s.Close()
	c, err := dial(t, s, "tok")
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 1)
	if err := c.SubscribeStateChanged(context.Background(), func(id string, _ *hass.State) { got <- id }); err != nil {
		t.Fatal(err)
	}
	s.FireEvent(map[string]any{"event_type": "state_changed", "data": map[string]any{
		"entity_id": "update.x", "new_state": map[string]any{"entity_id": "update.x", "state": "off"}}})
	select {
	case id := <-got:
		if id != "update.x" {
			t.Fatalf("got %q", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event")
	}

	s.DropConnections()
	select {
	case <-c.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("client did not notice disconnect")
	}
	if err := c.Ping(context.Background()); err == nil {
		t.Fatal("expected error after disconnect")
	}
}

func TestWebSocketURL(t *testing.T) {
	cases := []struct {
		in, want string
		insecure bool
		wantErr  bool
	}{
		{in: "https://ha.local:8123", want: "wss://ha.local:8123/api/websocket"},
		{in: "https://ha.local/sub/", want: "wss://ha.local/sub/api/websocket"},
		{in: "http://ha.local:8123", wantErr: true},
		{in: "http://ha.local:8123", insecure: true, want: "ws://ha.local:8123/api/websocket"},
		{in: "ftp://x", wantErr: true},
	}
	for _, c := range cases {
		got, err := hass.WebSocketURL(c.in, c.insecure)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("%s: got %q err %v", c.in, got, err)
		}
	}
}

func TestCertExpiry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv := hasstest.NewTLS("tok")
	defer srv.Close()
	sum := sha256.Sum256(srv.Certificate().Raw)
	c, err := hass.Dial(ctx, hass.Options{URL: srv.URL, Token: "tok", CertFingerprint: hex.EncodeToString(sum[:])})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if !c.CertNotAfter.Equal(srv.Certificate().NotAfter) {
		t.Fatalf("CertNotAfter = %v, want %v", c.CertNotAfter, srv.Certificate().NotAfter)
	}

	plain := hasstest.New("tok")
	defer plain.Close()
	c2, err := hass.Dial(ctx, hass.Options{URL: plain.URL, Token: "tok", AllowInsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	if !c2.CertNotAfter.IsZero() {
		t.Fatalf("plain http: %v", c2.CertNotAfter)
	}
}

func TestFingerprintPin(t *testing.T) {
	// A TLS server with a self-signed cert: system roots would reject it, the pin accepts it.
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	sum := sha256.Sum256(srv.Certificate().Raw)
	fp := hex.EncodeToString(sum[:])

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Correct pin: TLS succeeds, then the websocket upgrade fails with 404 (not a TLS error).
	_, err := hass.Dial(ctx, hass.Options{URL: srv.URL, Token: "x", CertFingerprint: fp})
	if err == nil || strings.Contains(err.Error(), "certificate") {
		t.Fatalf("expected non-TLS error with correct pin, got %v", err)
	}
	// Wrong pin.
	_, err = hass.Dial(ctx, hass.Options{URL: srv.URL, Token: "x", CertFingerprint: strings.Repeat("ab", 32)})
	if err == nil || !strings.Contains(err.Error(), "fingerprint mismatch") {
		t.Fatalf("expected fingerprint mismatch, got %v", err)
	}
	// No pin, no CA: self-signed cert rejected.
	_, err = hass.Dial(ctx, hass.Options{URL: srv.URL, Token: "x"})
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("expected certificate error, got %v", err)
	}
}
