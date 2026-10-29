package api

import (
	"net/http"
	"strconv"
)

const maxHistoryDays = 365

// history returns task trends: per-day counts, time to resolve per kind and recurring tasks.
func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	days := 30
	if v := q.Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxHistoryDays {
			writeError(w, http.StatusBadRequest, "days must be between 1 and 365")
			return
		}
		days = n
	}
	inst := q.Get("instance")
	if inst != "" {
		if _, ok := s.Fleet.View(inst); !ok {
			writeError(w, http.StatusNotFound, "instance not found")
			return
		}
	}
	sum, err := s.Fleet.History(r.Context(), days, inst)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sum)
}
