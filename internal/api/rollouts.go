package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/schmidtfx/home-assistant-fleet/internal/auth"
	"github.com/schmidtfx/home-assistant-fleet/internal/rollout"
	"github.com/schmidtfx/home-assistant-fleet/internal/store"
)

func (s *Server) rolloutRoutes(mux *http.ServeMux, req, op func(http.Handler) http.Handler) {
	mux.Handle("GET /api/rollouts", req(http.HandlerFunc(s.listRollouts)))
	mux.Handle("POST /api/rollouts", op(http.HandlerFunc(s.createRollout)))
	mux.Handle("GET /api/rollouts/{id}", req(http.HandlerFunc(s.getRollout)))
	mux.Handle("POST /api/rollouts/{id}/pause", op(s.rolloutControl("rollout_pause", s.Rollouts.Pause)))
	mux.Handle("POST /api/rollouts/{id}/resume", op(s.rolloutControl("rollout_resume", s.Rollouts.Resume)))
	mux.Handle("POST /api/rollouts/{id}/cancel", op(s.rolloutControl("rollout_cancel", s.Rollouts.Cancel)))
	mux.Handle("POST /api/rollouts/{id}/steps/{ord}/skip", op(http.HandlerFunc(s.skipRolloutStep)))
}

func (s *Server) listRollouts(w http.ResponseWriter, r *http.Request) {
	list, err := s.Rollouts.List(r.Context(), 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = []*store.Rollout{}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) getRollout(w http.ResponseWriter, r *http.Request) {
	ro, err := s.Rollouts.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeRolloutError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ro)
}

func (s *Server) createRollout(w http.ResponseWriter, r *http.Request) {
	var body rollout.CreateRequest
	if !decode(w, r, &body) {
		return
	}
	user := auth.SessionFrom(r.Context()).Username
	ro, err := s.Rollouts.Create(r.Context(), user, body)
	if err != nil {
		writeRolloutError(w, err)
		return
	}
	s.auditRollout(r, "rollout_create", ro, "")
	writeJSON(w, http.StatusCreated, ro)
}

func (s *Server) rolloutControl(action string, fn func(ctx context.Context, id string) (*store.Rollout, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ro, err := fn(r.Context(), r.PathValue("id"))
		if err != nil {
			writeRolloutError(w, err)
			return
		}
		s.auditRollout(r, action, ro, "")
		writeJSON(w, http.StatusOK, ro)
	})
}

func (s *Server) skipRolloutStep(w http.ResponseWriter, r *http.Request) {
	ord, err := strconv.Atoi(r.PathValue("ord"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid step")
		return
	}
	ro, err := s.Rollouts.Skip(r.Context(), r.PathValue("id"), ord)
	if err != nil {
		writeRolloutError(w, err)
		return
	}
	name := ro.Steps[ord].InstanceID
	if v, ok := s.Fleet.View(name); ok {
		name = v.Name
	}
	s.auditRollout(r, "rollout_skip", ro, name)
	writeJSON(w, http.StatusOK, ro)
}

// auditRollout records a user's change to a rollout. Each install is also
// audited by the action runner, under the user "rollout:<creator>".
func (s *Server) auditRollout(r *http.Request, action string, ro *store.Rollout, message string) {
	if err := s.Store.AddAudit(r.Context(), &store.AuditEntry{
		Username: auth.SessionFrom(r.Context()).Username, Action: action,
		Target: ro.Title, Result: "ok", Message: message,
	}); err != nil {
		s.Log.Error("write audit entry", "err", err)
	}
}

func writeRolloutError(w http.ResponseWriter, err error) {
	var ve *rollout.ValidationError
	switch {
	case errors.As(err, &ve):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": ve.Message, "instances": ve.Instances})
	case errors.Is(err, rollout.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, rollout.ErrInvalidState):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}
