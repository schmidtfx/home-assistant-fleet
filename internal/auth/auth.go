// Package auth implements local admin login, server-side sessions, CSRF
// protection and login rate limiting.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/schmidtfx/home-assistant-fleet/internal/store"
)

const (
	SessionCookie = "hafleet_session"
	CSRFCookie    = "hafleet_csrf"
	CSRFHeader    = "X-CSRF-Token"
	SessionTTL    = 12 * time.Hour
)

var ErrInvalidCredentials = errors.New("invalid username or password")
var ErrRateLimited = errors.New("too many failed login attempts, try again later")

type Manager struct {
	store          *store.Store
	secureCookies  bool
	allowedOrigins map[string]bool
	limiter        *RateLimiter
}

func NewManager(s *store.Store, secureCookies bool, allowedOrigins []string) *Manager {
	ao := map[string]bool{}
	for _, o := range allowedOrigins {
		ao[strings.TrimRight(o, "/")] = true
	}
	return &Manager{
		store:          s,
		secureCookies:  secureCookies,
		allowedOrigins: ao,
		limiter:        NewRateLimiter(5, 15*time.Minute),
	}
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashToken(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

// Login verifies credentials and, on success, sets session and CSRF cookies.
func (m *Manager) Login(w http.ResponseWriter, r *http.Request, username, password string) (*store.Session, error) {
	key := clientIP(r) + "|" + strings.ToLower(username)
	if !m.limiter.Allow(key) {
		return nil, ErrRateLimited
	}
	u, err := m.store.UserByName(r.Context(), username)
	hash := dummyHash
	if err == nil {
		hash = u.PasswordHash
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	ok, verr := VerifyPassword(password, hash)
	if err != nil || verr != nil || !ok {
		m.limiter.Fail(key)
		return nil, ErrInvalidCredentials
	}
	m.limiter.Reset(key)

	tok := randomToken()
	csrf := randomToken()
	exp := time.Now().Add(SessionTTL)
	if err := m.store.CreateSession(r.Context(), hashToken(tok), u.ID, csrf, exp); err != nil {
		return nil, err
	}
	m.setCookies(w, tok, csrf, exp)
	return &store.Session{UserID: u.ID, Username: u.Username, Role: u.Role, CSRFToken: csrf, ExpiresAt: exp}, nil
}

func (m *Manager) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(SessionCookie); err == nil {
		_ = m.store.DeleteSession(r.Context(), hashToken(c.Value))
	}
	m.setCookies(w, "", "", time.Unix(0, 0))
}

func (m *Manager) setCookies(w http.ResponseWriter, tok, csrf string, exp time.Time) {
	maxAge := int(time.Until(exp).Seconds())
	if tok == "" {
		maxAge = -1
	}
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: tok, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: m.secureCookies, SameSite: http.SameSiteStrictMode,
	})
	// Readable by JS so the SPA can echo it in the X-CSRF-Token header.
	http.SetCookie(w, &http.Cookie{
		Name: CSRFCookie, Value: csrf, Path: "/", MaxAge: maxAge,
		HttpOnly: false, Secure: m.secureCookies, SameSite: http.SameSiteStrictMode,
	})
}

type ctxKey struct{}

// SessionFrom returns the authenticated session stored on the request context.
func SessionFrom(ctx context.Context) *store.Session {
	s, _ := ctx.Value(ctxKey{}).(*store.Session)
	return s
}

func (m *Manager) lookup(r *http.Request) *store.Session {
	c, err := r.Cookie(SessionCookie)
	if err != nil || c.Value == "" {
		return nil
	}
	sess, err := m.store.SessionByHash(r.Context(), hashToken(c.Value))
	if err != nil || time.Now().After(sess.ExpiresAt) {
		return nil
	}
	return sess
}

// Require rejects unauthenticated requests and, for state-changing methods,
// enforces Origin and CSRF token checks.
func (m *Manager) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess := m.lookup(r)
		if sess == nil {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		if !isSafeMethod(r.Method) {
			if !m.CheckOrigin(r) {
				http.Error(w, `{"error":"bad origin"}`, http.StatusForbidden)
				return
			}
			got := r.Header.Get(CSRFHeader)
			if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(sess.CSRFToken)) != 1 {
				http.Error(w, `{"error":"invalid csrf token"}`, http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, sess)))
	})
}

// CheckOrigin rejects cross-site requests. A missing Origin header is accepted
// (non-browser clients), but Sec-Fetch-Site: cross-site is always rejected.
func (m *Manager) CheckOrigin(r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if m.allowedOrigins[origin] {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return u.Host == r.Host
}

func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// CleanupLoop periodically deletes expired sessions until ctx is done.
func (m *Manager) CleanupLoop(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		_ = m.store.DeleteExpiredSessions(ctx)
		m.limiter.Prune()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
