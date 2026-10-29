package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/schmidtfx/home-assistant-fleet/internal/ai"
	"github.com/schmidtfx/home-assistant-fleet/internal/auth"
)

// AI requests can take minutes; they get their own deadlines instead of the server's WriteTimeout.
const aiTimeout = 5 * time.Minute

// aiRoutes registers the AI routes. Reading cached results needs any role;
// anything that can call the model (and spend tokens) needs op.
func (s *Server) aiRoutes(mux *http.ServeMux, req, op func(http.Handler) http.Handler) {
	mux.Handle("GET /api/ai/status", req(http.HandlerFunc(s.aiStatus)))
	mux.Handle("GET /api/ai/reviews", req(s.requireAI(s.aiReviews)))
	mux.Handle("POST /api/instances/{id}/tasks/{task}/ai/review", op(s.requireAI(s.aiReview)))
	mux.Handle("POST /api/instances/{id}/tasks/{task}/ai/explain", op(s.requireAI(s.aiExplain)))
	mux.Handle("GET /api/ai/briefing", req(s.requireAI(s.aiGetBriefing)))
	mux.Handle("POST /api/ai/briefing", op(s.requireAI(s.aiNewBriefing)))
	mux.Handle("POST /api/ai/chat", op(s.requireAI(s.aiChat)))
	mux.Handle("POST /api/instances/{id}/actions/{action}/ai/health", op(s.requireAI(s.aiHealth)))
}

func (s *Server) requireAI(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.AI == nil {
			writeError(w, http.StatusNotFound, "AI features are not configured")
			return
		}
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(aiTimeout + 30*time.Second))
		ctx, cancel := context.WithTimeout(r.Context(), aiTimeout)
		defer cancel()
		h(w, r.WithContext(ctx))
	})
}

func writeAIError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ai.ErrDisabled):
		writeError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, ai.ErrRateLimited):
		writeError(w, http.StatusTooManyRequests, err.Error())
	case errors.Is(err, ai.ErrRefused):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, ai.ErrTooLong):
		writeError(w, http.StatusRequestEntityTooLarge, err.Error())
	case strings.HasSuffix(err.Error(), "not found"):
		writeError(w, http.StatusNotFound, err.Error())
	default:
		writeError(w, http.StatusBadGateway, err.Error())
	}
}

func (s *Server) aiStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.AI.Status()) // nil-safe
}

func (s *Server) aiReviews(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.AI.CachedReviews(r.Context()))
}

type refreshBody struct {
	Refresh bool `json:"refresh"`
}

func (s *Server) decodeRefresh(w http.ResponseWriter, r *http.Request) (bool, bool) {
	var b refreshBody
	if r.ContentLength == 0 {
		return false, true
	}
	if !decode(w, r, &b) {
		return false, false
	}
	return b.Refresh, true
}

func (s *Server) aiReview(w http.ResponseWriter, r *http.Request) {
	refresh, ok := s.decodeRefresh(w, r)
	if !ok {
		return
	}
	user := auth.SessionFrom(r.Context()).Username
	rev, err := s.AI.Review(r.Context(), user, r.PathValue("id"), r.PathValue("task"), refresh)
	if err != nil {
		writeAIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rev)
}

func (s *Server) aiExplain(w http.ResponseWriter, r *http.Request) {
	refresh, ok := s.decodeRefresh(w, r)
	if !ok {
		return
	}
	user := auth.SessionFrom(r.Context()).Username
	e, err := s.AI.Explain(r.Context(), user, r.PathValue("id"), r.PathValue("task"), refresh)
	if err != nil {
		writeAIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

// aiHealth runs the post-update health check for a finished install action.
func (s *Server) aiHealth(w http.ResponseWriter, r *http.Request) {
	refresh, ok := s.decodeRefresh(w, r)
	if !ok {
		return
	}
	rec, found := s.Actions.Get(r.PathValue("action"))
	if !found || rec.InstanceID != r.PathValue("id") {
		writeError(w, http.StatusNotFound, "action not found (only recent actions since the last restart can be checked)")
		return
	}
	user := auth.SessionFrom(r.Context()).Username
	h, err := s.AI.HealthCheck(r.Context(), user, rec, refresh)
	if errors.Is(err, ai.ErrNotAnUpdate) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		writeAIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h)
}

func (s *Server) aiGetBriefing(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"briefing": s.AI.LatestBriefing(r.Context())})
}

func (s *Server) aiNewBriefing(w http.ResponseWriter, r *http.Request) {
	b, err := s.AI.GenerateBriefing(r.Context(), auth.SessionFrom(r.Context()).Username)
	if err != nil {
		writeAIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"briefing": b})
}

// aiChat runs one chat turn and streams events as Server-Sent Events.
func (s *Server) aiChat(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ConversationID string `json:"conversation_id"`
		Message        string `json:"message"`
	}
	if !decode(w, r, &body) {
		return
	}
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	emit := func(ev ai.ChatEvent) {
		data, _ := json.Marshal(ev)
		_, _ = w.Write([]byte("event: " + ev.Type + "\ndata: " + string(data) + "\n\n"))
		_ = rc.Flush()
	}
	user := auth.SessionFrom(r.Context()).Username
	if err := s.AI.Chat(r.Context(), user, body.ConversationID, strings.TrimSpace(body.Message), emit); err != nil {
		emit(ai.ChatEvent{Type: "error", Text: err.Error()})
	}
}
