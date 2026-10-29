// Package hasstest provides a fake Home Assistant WebSocket server for tests.
package hasstest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"

	"github.com/coder/websocket"
)

// Handler answers a command. Returning a non-nil error message makes the
// command fail with that code.
type Handler func(cmd map[string]any) (result any, errCode string)

type Server struct {
	*httptest.Server
	Token   string
	Version string

	mu       sync.Mutex
	handlers map[string]Handler
	calls    []map[string]any
	conns    map[*websocket.Conn]*connState
}

type connState struct {
	writeMu sync.Mutex
	subs    []int64
}

// New starts a fake HA (plain HTTP; use AllowInsecureHTTP in clients).
func New(token string) *Server {
	s := newServer(token)
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// NewTLS is New over HTTPS with httptest's self-signed certificate (see Certificate).
func NewTLS(token string) *Server {
	s := newServer(token)
	s.Server = httptest.NewTLSServer(http.HandlerFunc(s.serve))
	return s
}

func newServer(token string) *Server {
	s := &Server{Token: token, Version: "2026.10.1", handlers: map[string]Handler{}, conns: map[*websocket.Conn]*connState{}}
	s.Handle("ping", nil)
	s.Handle("subscribe_events", func(map[string]any) (any, string) { return nil, "" })
	return s
}

// Handle registers a handler for a command type. A nil handler returns a null result.
func (s *Server) Handle(typ string, h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h == nil {
		h = func(map[string]any) (any, string) { return nil, "" }
	}
	s.handlers[typ] = h
}

// Result registers a handler that always returns v.
func (s *Server) Result(typ string, v any) {
	s.Handle(typ, func(map[string]any) (any, string) { return v, "" })
}

// Calls returns all commands received so far.
func (s *Server) Calls() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.calls...)
}

// FireEvent sends an event to every subscription on every connection.
func (s *Server) FireEvent(event any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c, st := range s.conns {
		for _, id := range st.subs {
			st.write(c, map[string]any{"id": id, "type": "event", "event": event})
		}
	}
}

// DropConnections forcibly closes all client connections.
func (s *Server) DropConnections() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.conns {
		c.CloseNow()
	}
}

func (st *connState) write(c *websocket.Conn, v any) {
	data, _ := json.Marshal(v)
	st.writeMu.Lock()
	defer st.writeMu.Unlock()
	_ = c.Write(context.Background(), websocket.MessageText, data)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/websocket" {
		http.NotFound(w, r)
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	c.SetReadLimit(1 << 20)
	ctx := r.Context()
	st := &connState{}
	st.write(c, map[string]any{"type": "auth_required", "ha_version": s.Version})

	var auth struct {
		Type        string `json:"type"`
		AccessToken string `json:"access_token"`
	}
	_, data, err := c.Read(ctx)
	if err != nil || json.Unmarshal(data, &auth) != nil || auth.Type != "auth" || auth.AccessToken != s.Token {
		st.write(c, map[string]any{"type": "auth_invalid", "message": "Invalid access token or password"})
		c.Close(websocket.StatusPolicyViolation, "")
		return
	}
	st.write(c, map[string]any{"type": "auth_ok", "ha_version": s.Version})

	s.mu.Lock()
	s.conns[c] = st
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
	}()

	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		var cmd map[string]any
		if json.Unmarshal(data, &cmd) != nil {
			return
		}
		id := int64(cmd["id"].(float64))
		typ, _ := cmd["type"].(string)

		s.mu.Lock()
		s.calls = append(s.calls, cmd)
		h := s.handlers[typ]
		if typ == "subscribe_events" {
			st.subs = append(st.subs, id)
		}
		s.mu.Unlock()

		if typ == "ping" {
			st.write(c, map[string]any{"id": id, "type": "pong"})
			continue
		}
		if h == nil {
			st.write(c, map[string]any{"id": id, "type": "result", "success": false,
				"error": map[string]string{"code": "unknown_command", "message": "Unknown command."}})
			continue
		}
		res, code := h(cmd)
		if code != "" {
			st.write(c, map[string]any{"id": id, "type": "result", "success": false,
				"error": map[string]string{"code": code, "message": code}})
			continue
		}
		st.write(c, map[string]any{"id": id, "type": "result", "success": true, "result": res})
	}
}
