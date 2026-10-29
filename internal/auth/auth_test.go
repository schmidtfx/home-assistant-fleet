package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/schmidtfx/home-assistant-fleet/internal/store"
)

func TestPasswordHash(t *testing.T) {
	h, err := HashPassword("s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := VerifyPassword("s3cret", h); !ok {
		t.Fatal("expected match")
	}
	if ok, _ := VerifyPassword("wrong", h); ok {
		t.Fatal("expected mismatch")
	}
}

func TestRateLimiter(t *testing.T) {
	now := time.Unix(1000, 0)
	l := NewRateLimiter(2, time.Minute)
	l.now = func() time.Time { return now }
	l.Fail("k")
	l.Fail("k")
	if l.Allow("k") {
		t.Fatal("expected block after 2 failures")
	}
	now = now.Add(2 * time.Minute)
	if !l.Allow("k") {
		t.Fatal("expected allow after window")
	}
}

func setup(t *testing.T) (*Manager, *store.Store) {
	s, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	h, _ := HashPassword("pw")
	if err := s.CreateUser(context.Background(), "admin", h); err != nil {
		t.Fatal(err)
	}
	return NewManager(s, true, nil), s
}

func login(t *testing.T, m *Manager) (session, csrf *http.Cookie) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/auth/login", nil)
	if _, err := m.Login(w, r, "admin", "pw"); err != nil {
		t.Fatal(err)
	}
	for _, c := range w.Result().Cookies() {
		switch c.Name {
		case SessionCookie:
			session = c
		case CSRFCookie:
			csrf = c
		}
	}
	if session == nil || csrf == nil || !session.HttpOnly || !session.Secure {
		t.Fatalf("bad cookies: %+v %+v", session, csrf)
	}
	return
}

func TestRequire(t *testing.T) {
	m, _ := setup(t)
	sess, csrf := login(t, m)
	h := m.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if SessionFrom(r.Context()).Username != "admin" {
			t.Error("missing session in context")
		}
	}))

	cases := []struct {
		name   string
		method string
		cookie bool
		csrf   string
		origin string
		want   int
	}{
		{"no session", "GET", false, "", "", 401},
		{"get ok", "GET", true, "", "", 200},
		{"post without csrf", "POST", true, "", "", 403},
		{"post wrong csrf", "POST", true, "nope", "", 403},
		{"post ok", "POST", true, csrf.Value, "http://example.com", 200},
		{"post cross origin", "POST", true, csrf.Value, "http://evil.com", 403},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(c.method, "http://example.com/api/x", nil)
			if c.cookie {
				r.AddCookie(sess)
			}
			if c.csrf != "" {
				r.Header.Set(CSRFHeader, c.csrf)
			}
			if c.origin != "" {
				r.Header.Set("Origin", c.origin)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != c.want {
				t.Fatalf("got %d want %d", w.Code, c.want)
			}
		})
	}
}

func TestLoginRateLimit(t *testing.T) {
	m, _ := setup(t)
	r := httptest.NewRequest("POST", "/api/auth/login", nil)
	for i := 0; i < 5; i++ {
		if _, err := m.Login(httptest.NewRecorder(), r, "admin", "bad"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if _, err := m.Login(httptest.NewRecorder(), r, "admin", "pw"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected rate limit, got %v", err)
	}
}

func TestLogoutInvalidatesSession(t *testing.T) {
	m, _ := setup(t)
	sess, _ := login(t, m)
	r := httptest.NewRequest("POST", "/api/auth/logout", nil)
	r.AddCookie(sess)
	m.Logout(httptest.NewRecorder(), r)

	r = httptest.NewRequest("GET", "/api/x", nil)
	r.AddCookie(sess)
	w := httptest.NewRecorder()
	m.Require(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("expected 401 after logout, got %d", w.Code)
	}
}
