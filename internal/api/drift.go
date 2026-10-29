package api

import (
	"net/http"

	"github.com/schmidtfx/home-assistant-fleet/internal/fleet"
)

// drift compares versions, add-ons and integrations across instances.
func (s *Server) drift(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, fleet.BuildDrift(s.Fleet.Views()))
}
