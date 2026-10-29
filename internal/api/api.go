// Package api exposes the fleet manager over a JSON REST API with SSE events
// and serves the embedded frontend.
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/schmidtfx/home-assistant-fleet/internal/actions"
	"github.com/schmidtfx/home-assistant-fleet/internal/ai"
	"github.com/schmidtfx/home-assistant-fleet/internal/auth"
	"github.com/schmidtfx/home-assistant-fleet/internal/fleet"
	"github.com/schmidtfx/home-assistant-fleet/internal/hass"
	"github.com/schmidtfx/home-assistant-fleet/internal/rollout"
	"github.com/schmidtfx/home-assistant-fleet/internal/secrets"
	"github.com/schmidtfx/home-assistant-fleet/internal/store"
	"github.com/schmidtfx/home-assistant-fleet/internal/tasks"
)

type Server struct {
	Store   *store.Store
	Box     *secrets.Box
	Auth    *auth.Manager
	Fleet   *fleet.Manager
	Actions *actions.Runner
	// Rollouts may be nil in tests that do not need them; the routes are then not registered.
	Rollouts *rollout.Manager
	AI       *ai.Service // nil when AI features are disabled
	Static   fs.FS       // built frontend (may be empty)
	Log      *slog.Logger
	HSTS     bool
	DevMode  bool
	// MetricsToken enables GET /metrics for requests with this bearer token.
	MetricsToken string
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	req := s.Auth.Require // any role; GET routes are read-only
	op := func(h http.Handler) http.Handler { return s.Auth.RequireRole(auth.RoleOperator, h) }
	admin := func(h http.Handler) http.Handler { return s.Auth.RequireRole(auth.RoleAdmin, h) }

	mux.HandleFunc("POST /api/auth/login", s.login)
	mux.HandleFunc("GET /metrics", s.metrics) // own bearer-token check, see metrics.go
	mux.Handle("POST /api/auth/logout", req(http.HandlerFunc(s.logout)))
	mux.Handle("GET /api/auth/me", req(http.HandlerFunc(s.me)))

	mux.Handle("GET /api/instances", req(http.HandlerFunc(s.listInstances)))
	mux.Handle("POST /api/instances", admin(http.HandlerFunc(s.createInstance)))
	mux.Handle("POST /api/instances/test", admin(http.HandlerFunc(s.testInstance)))
	mux.Handle("GET /api/instances/{id}", req(http.HandlerFunc(s.getInstance)))
	mux.Handle("PATCH /api/instances/{id}", admin(http.HandlerFunc(s.updateInstance)))
	mux.Handle("DELETE /api/instances/{id}", admin(http.HandlerFunc(s.deleteInstance)))
	mux.Handle("POST /api/instances/{id}/resync", op(http.HandlerFunc(s.resyncInstance)))
	mux.Handle("POST /api/instances/{id}/actions", op(http.HandlerFunc(s.submitAction)))
	mux.Handle("GET /api/instances/{id}/tasks/{task}/release-notes", req(http.HandlerFunc(s.releaseNotes)))
	mux.Handle("POST /api/instances/{id}/tasks/{task}/ack", op(http.HandlerFunc(s.ackTask)))
	mux.Handle("DELETE /api/instances/{id}/tasks/{task}/ack", op(http.HandlerFunc(s.unackTask)))

	mux.Handle("GET /api/tasks", req(http.HandlerFunc(s.listTasks)))
	mux.Handle("GET /api/actions", req(http.HandlerFunc(s.listActions)))
	mux.Handle("GET /api/audit", req(http.HandlerFunc(s.listAudit)))
	mux.Handle("GET /api/history", req(http.HandlerFunc(s.history)))
	mux.Handle("GET /api/drift", req(http.HandlerFunc(s.drift)))
	mux.Handle("GET /api/events", req(http.HandlerFunc(s.events)))
	mux.Handle("GET /api/settings", req(http.HandlerFunc(s.getSettings)))
	mux.Handle("PUT /api/settings", admin(http.HandlerFunc(s.putSettings)))
	s.aiRoutes(mux, req, op)
	if s.Rollouts != nil {
		s.rolloutRoutes(mux, req, op)
	}

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
	mux.Handle("/", s.spa())
	return s.securityHeaders(mux)
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	csp := "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: https:; " +
		"connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		if !s.DevMode {
			h.Set("Content-Security-Policy", csp)
		}
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if s.HSTS {
			h.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// spa serves static files and falls back to index.html for client-side routes.
func (s *Server) spa() http.Handler {
	files := http.FileServerFS(s.Static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" {
			if st, err := fs.Stat(s.Static, p); err == nil && !st.IsDir() {
				if strings.HasPrefix(p, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		index, err := fs.ReadFile(s.Static, "index.html")
		if err != nil {
			http.Error(w, "frontend not built: run `make web`", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(index)
	})
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return false
	}
	return true
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---- auth ----

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.Auth.CheckOrigin(r) {
		writeError(w, http.StatusForbidden, "bad origin")
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, &body) {
		return
	}
	sess, err := s.Auth.Login(w, r, body.Username, body.Password)
	switch {
	case errors.Is(err, auth.ErrRateLimited):
		writeError(w, http.StatusTooManyRequests, err.Error())
	case errors.Is(err, auth.ErrInvalidCredentials):
		writeError(w, http.StatusUnauthorized, err.Error())
	case err != nil:
		s.Log.Error("login", "err", err)
		writeError(w, http.StatusInternalServerError, "login failed")
	default:
		s.Log.Info("user logged in", "user", sess.Username)
		writeJSON(w, http.StatusOK, map[string]string{"username": sess.Username, "role": sess.Role})
	}
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.Auth.Logout(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	sess := auth.SessionFrom(r.Context())
	writeJSON(w, http.StatusOK, map[string]string{"username": sess.Username, "role": sess.Role})
}

// ---- tasks, actions, audit ----

type fleetTask struct {
	tasks.Task
	InstanceName string `json:"instance_name"`
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	out := []fleetTask{}
	for _, v := range s.Fleet.Views() {
		if id := q.Get("instance"); id != "" && id != v.ID {
			continue
		}
		for _, t := range v.Tasks {
			if k := q.Get("kind"); k != "" && string(t.Kind) != k {
				continue
			}
			if sev := q.Get("severity"); sev != "" && string(t.Severity) != sev {
				continue
			}
			out = append(out, fleetTask{Task: t, InstanceName: v.Name})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) submitAction(w http.ResponseWriter, r *http.Request) {
	var req actions.Request
	if !decode(w, r, &req) {
		return
	}
	user := auth.SessionFrom(r.Context()).Username
	rec, err := s.Actions.Submit(user, r.PathValue("id"), req)
	switch {
	case errors.Is(err, actions.ErrUnknownTask):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, actions.ErrActionNotAllowed):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, actions.ErrAlreadyRunning):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, fleet.ErrNotConnected):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
	default:
		s.Log.Info("action submitted", "user", user, "instance", rec.InstanceID, "action", rec.Action, "target", rec.Target)
		writeJSON(w, http.StatusAccepted, rec)
	}
}

// ackTask hides a task until it changes, resolves, or the optional snooze ends.
func (s *Server) ackTask(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Note          string `json:"note"`
		DurationHours int    `json:"duration_hours"` // 0 = until it changes or resolves
	}
	if !decode(w, r, &body) {
		return
	}
	body.Note = strings.TrimSpace(body.Note)
	if len(body.Note) > 500 || body.DurationHours < 0 || body.DurationHours > 24*365 {
		writeError(w, http.StatusBadRequest, "note must be at most 500 characters and duration at most a year")
		return
	}
	user := auth.SessionFrom(r.Context()).Username
	t, err := s.Fleet.Acknowledge(r.Context(), r.PathValue("id"), r.PathValue("task"), user, body.Note,
		time.Duration(body.DurationHours)*time.Hour)
	s.writeAckResult(w, r, "acknowledge", t, body.Note, err)
}

func (s *Server) unackTask(w http.ResponseWriter, r *http.Request) {
	t, err := s.Fleet.Unacknowledge(r.Context(), r.PathValue("id"), r.PathValue("task"))
	s.writeAckResult(w, r, "unacknowledge", t, "", err)
}

func (s *Server) writeAckResult(w http.ResponseWriter, r *http.Request, action string, t tasks.Task, note string, err error) {
	if errors.Is(err, fleet.ErrTaskNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	v, _ := s.Fleet.View(t.InstanceID)
	if err := s.Store.AddAudit(r.Context(), &store.AuditEntry{
		Username: auth.SessionFrom(r.Context()).Username, InstanceID: v.ID, InstanceName: v.Name,
		Action: action, Target: t.Title, Result: "ok", Message: note,
	}); err != nil {
		s.Log.Error("write audit entry", "err", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listActions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Actions.Recent())
}

func (s *Server) releaseNotes(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	task, ok := s.Fleet.Task(id, r.PathValue("task"))
	if !ok || task.Kind != tasks.KindUpdate {
		writeError(w, http.StatusNotFound, "update task not found")
		return
	}
	entityID, _ := task.Source["entity_id"].(string)
	if task.Source["supports_release_notes"] != true {
		writeJSON(w, http.StatusOK, map[string]any{"notes": nil, "release_url": task.Source["release_url"]})
		return
	}
	c, err := s.Fleet.Client(id)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	notes, err := c.ReleaseNotes(ctx, entityID)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"notes": notes, "release_url": task.Source["release_url"]})
}

func (s *Server) listAudit(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	entries, err := s.Store.ListAudit(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

// events streams fleet changes as Server-Sent Events.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{}) // long-lived stream
	ch, cancel := s.Fleet.Subscribe()
	defer cancel()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	send := func(event string, v any) error {
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if _, err := w.Write([]byte("event: " + event + "\ndata: " + string(data) + "\n\n")); err != nil {
			return err
		}
		return rc.Flush()
	}
	if send("snapshot", map[string]any{"instances": s.Fleet.Views(), "actions": s.Actions.Recent()}) != nil {
		return
	}
	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := w.Write([]byte(": ping\n\n")); err != nil || rc.Flush() != nil {
				return
			}
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if send(ev.Type, ev) != nil {
				return
			}
		}
	}
}

// ---- instances ----

type instanceInput struct {
	Name              *string `json:"name"`
	URL               *string `json:"url"`
	Token             *string `json:"token"`
	CAPEM             *string `json:"ca_pem"`
	CertFingerprint   *string `json:"cert_fingerprint"`
	AllowInsecureHTTP *bool   `json:"allow_insecure_http"`
	Enabled           *bool   `json:"enabled"`
	AIEnabled         *bool   `json:"ai_enabled"`
	PublishStatus     *bool   `json:"publish_status"`
	MaintWindow       *string `json:"maint_window"` // "HH:MM-HH:MM" server local time, "" = any time
	// Settings override the fleet-wide check settings for this instance.
	Settings *tasks.Settings `json:"settings"`
}

// apply merges non-nil fields into in and validates the result.
func (s *Server) apply(in *store.Instance, body instanceInput) error {
	if body.Name != nil {
		in.Name = strings.TrimSpace(*body.Name)
	}
	if body.URL != nil {
		in.URL = strings.TrimRight(strings.TrimSpace(*body.URL), "/")
	}
	if body.CAPEM != nil {
		in.CAPEM = strings.TrimSpace(*body.CAPEM)
	}
	if body.CertFingerprint != nil {
		in.CertFingerprint = strings.TrimSpace(*body.CertFingerprint)
	}
	if body.AllowInsecureHTTP != nil {
		in.AllowInsecureHTTP = *body.AllowInsecureHTTP
	}
	if body.Enabled != nil {
		in.Enabled = *body.Enabled
	}
	if body.AIEnabled != nil {
		in.AIEnabled = *body.AIEnabled
	}
	if body.PublishStatus != nil {
		in.PublishStatus = *body.PublishStatus
	}
	if body.MaintWindow != nil {
		w, err := rollout.ParseWindow(*body.MaintWindow)
		if err != nil {
			return err
		}
		in.MaintWindow = w.String()
	}
	if body.Settings != nil {
		if err := body.Settings.Validate(); err != nil {
			return err
		}
		b, err := json.Marshal(body.Settings)
		if err != nil {
			return err
		}
		in.Settings = string(b)
	}
	if in.Name == "" || len(in.Name) > 100 {
		return errors.New("name is required (max 100 characters)")
	}
	if _, err := hass.WebSocketURL(in.URL, in.AllowInsecureHTTP); err != nil {
		return err
	}
	if _, err := hass.TLSConfig(hass.Options{CAPEM: in.CAPEM, CertFingerprint: in.CertFingerprint}); err != nil {
		return err
	}
	if body.Token != nil {
		tok := strings.TrimSpace(*body.Token)
		if tok == "" {
			return errors.New("token must not be empty")
		}
		enc, err := s.Box.Seal([]byte(tok), []byte(in.ID))
		if err != nil {
			return err
		}
		in.TokenEnc = enc
	}
	if len(in.TokenEnc) == 0 {
		return errors.New("token is required")
	}
	return nil
}

func (s *Server) listInstances(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Fleet.Views())
}

func (s *Server) getInstance(w http.ResponseWriter, r *http.Request) {
	v, ok := s.Fleet.View(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "instance not found")
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) createInstance(w http.ResponseWriter, r *http.Request) {
	var body instanceInput
	if !decode(w, r, &body) {
		return
	}
	in := &store.Instance{ID: newID(), Enabled: true}
	if err := s.apply(in, body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.Store.CreateInstance(r.Context(), in); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.Fleet.Reload(r.Context(), in.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.auditInstance(r, "instance_create", in.ID, in.Name)
	v, _ := s.Fleet.View(in.ID)
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) updateInstance(w http.ResponseWriter, r *http.Request) {
	in, err := s.Store.GetInstance(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "instance not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var body instanceInput
	if !decode(w, r, &body) {
		return
	}
	if err := s.apply(in, body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.Store.UpdateInstance(r.Context(), in); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.Fleet.Reload(r.Context(), in.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.auditInstance(r, "instance_update", in.ID, in.Name)
	v, _ := s.Fleet.View(in.ID)
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) deleteInstance(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	v, _ := s.Fleet.View(id)
	if err := s.Store.DeleteInstance(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "instance not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Fleet.Remove(id)
	s.auditInstance(r, "instance_delete", id, v.Name)
	w.WriteHeader(http.StatusNoContent)
}

// auditInstance records a change to an instance's settings. The settings
// themselves (URL, token, TLS options) are not written to the audit log.
func (s *Server) auditInstance(r *http.Request, action, id, name string) {
	user := auth.SessionFrom(r.Context()).Username
	s.Log.Info("instance changed", "action", action, "user", user, "instance", name)
	if err := s.Store.AddAudit(r.Context(), &store.AuditEntry{
		Username: user, InstanceID: id, InstanceName: name, Action: action, Target: name, Result: "ok",
	}); err != nil {
		s.Log.Error("write audit entry", "err", err)
	}
}

func (s *Server) resyncInstance(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.Fleet.View(r.PathValue("id")); !ok {
		writeError(w, http.StatusNotFound, "instance not found")
		return
	}
	s.Fleet.Resync(r.PathValue("id"))
	w.WriteHeader(http.StatusAccepted)
}

// testInstance dials an instance with the given settings without saving it.
// If token is omitted and instance_id is set, the stored token is used.
func (s *Server) testInstance(w http.ResponseWriter, r *http.Request) {
	var body struct {
		instanceInput
		InstanceID string `json:"instance_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	in := &store.Instance{ID: "test"}
	if body.InstanceID != "" {
		existing, err := s.Store.GetInstance(r.Context(), body.InstanceID)
		if err != nil {
			writeError(w, http.StatusNotFound, "instance not found")
			return
		}
		in = existing
	}
	if err := s.apply(in, body.instanceInput); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tok, err := s.Box.Open(in.TokenEnc, []byte(in.ID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cannot decrypt stored token")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	c, err := hass.Dial(ctx, hass.Options{URL: in.URL, Token: string(tok), CAPEM: in.CAPEM,
		CertFingerprint: in.CertFingerprint, AllowInsecureHTTP: in.AllowInsecureHTTP})
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	defer c.Close()
	cfg, err := c.GetConfig(ctx)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	isAdmin, _ := c.IsAdmin(ctx)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "ha_version": cfg.Version, "location_name": cfg.LocationName,
		"supervisor": cfg.HasComponent("hassio"), "is_admin": isAdmin,
	})
}
