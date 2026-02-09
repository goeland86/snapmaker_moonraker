package moonraker

import (
	"net/http"
	"strconv"
)

// registerHistoryHandlers sets up /server/history/* routes.
func (s *Server) registerHistoryHandlers() {
	s.mux.HandleFunc("GET /server/history/list", s.handleHistoryList)
	s.mux.HandleFunc("GET /server/history/job", s.handleHistoryGetJob)
	s.mux.HandleFunc("DELETE /server/history/job", s.handleHistoryDeleteJob)
	s.mux.HandleFunc("GET /server/history/totals", s.handleHistoryTotals)
	s.mux.HandleFunc("POST /server/history/reset_totals", s.handleHistoryResetTotals)
}

func (s *Server) handleHistoryList(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	start, _ := strconv.Atoi(query.Get("start"))
	limit, _ := strconv.Atoi(query.Get("limit"))
	before, _ := strconv.ParseFloat(query.Get("before"), 64)
	since, _ := strconv.ParseFloat(query.Get("since"), 64)
	order := query.Get("order")

	if limit == 0 {
		limit = 50 // Default limit
	}

	jobs, count := s.history.ListJobs(start, limit, before, since, order)

	writeJSON(w, map[string]interface{}{
		"result": map[string]interface{}{
			"count": count,
			"jobs":  jobs,
		},
	})
}

func (s *Server) handleHistoryGetJob(w http.ResponseWriter, r *http.Request) {
	uid := r.URL.Query().Get("uid")
	if uid == "" {
		writeJSONError(w, http.StatusBadRequest, "uid is required")
		return
	}

	job := s.history.GetJob(uid)
	if job == nil {
		writeJSONError(w, http.StatusNotFound, "job not found")
		return
	}

	writeJSON(w, map[string]interface{}{
		"result": map[string]interface{}{
			"job": job,
		},
	})
}

func (s *Server) handleHistoryDeleteJob(w http.ResponseWriter, r *http.Request) {
	uid := r.URL.Query().Get("uid")
	if uid == "" {
		writeJSONError(w, http.StatusBadRequest, "uid is required")
		return
	}

	deleted := s.history.DeleteJob(uid)

	writeJSON(w, map[string]interface{}{
		"result": map[string]interface{}{
			"deleted_jobs": []string{},
		},
	})

	if deleted {
		// Could broadcast notify_history_changed here if needed
	}
}

func (s *Server) handleHistoryTotals(w http.ResponseWriter, r *http.Request) {
	totals := s.history.GetTotals()

	writeJSON(w, map[string]interface{}{
		"result": map[string]interface{}{
			"job_totals": totals,
		},
	})
}

func (s *Server) handleHistoryResetTotals(w http.ResponseWriter, r *http.Request) {
	s.history.ResetTotals()

	writeJSON(w, map[string]interface{}{
		"result": map[string]interface{}{
			"last_totals": s.history.GetTotals(),
		},
	})
}

// History JSON-RPC handlers for WebSocket

func (h *WSHub) handleHistoryList(params interface{}) interface{} {
	start := extractIntParam(params, "start")
	limit := extractIntParam(params, "limit")
	before := extractFloatParam(params, "before")
	since := extractFloatParam(params, "since")
	order := extractStringParam(params, "order")

	if limit == 0 {
		limit = 50
	}

	jobs, count := h.server.history.ListJobs(start, limit, before, since, order)

	return map[string]interface{}{
		"count": count,
		"jobs":  jobs,
	}
}

func (h *WSHub) handleHistoryGetJob(params interface{}) interface{} {
	uid := extractStringParam(params, "uid")
	if uid == "" {
		return map[string]interface{}{"error": "uid is required"}
	}

	job := h.server.history.GetJob(uid)
	if job == nil {
		return map[string]interface{}{"error": "job not found"}
	}

	return map[string]interface{}{
		"job": job,
	}
}

func (h *WSHub) handleHistoryDeleteJob(params interface{}) interface{} {
	uid := extractStringParam(params, "uid")
	if uid == "" {
		return map[string]interface{}{"error": "uid is required"}
	}

	h.server.history.DeleteJob(uid)

	return map[string]interface{}{
		"deleted_jobs": []string{uid},
	}
}

func (h *WSHub) handleHistoryTotals() interface{} {
	totals := h.server.history.GetTotals()
	return map[string]interface{}{
		"job_totals": totals,
	}
}

func (h *WSHub) handleHistoryResetTotals() interface{} {
	h.server.history.ResetTotals()
	return map[string]interface{}{
		"last_totals": h.server.history.GetTotals(),
	}
}

// Helper functions for extracting typed params

func extractIntParam(params interface{}, key string) int {
	if params == nil {
		return 0
	}

	if p, ok := params.(map[string]interface{}); ok {
		if v, ok := p[key]; ok {
			switch val := v.(type) {
			case float64:
				return int(val)
			case int:
				return val
			case int64:
				return int(val)
			}
		}
	}
	return 0
}

func extractFloatParam(params interface{}, key string) float64 {
	if params == nil {
		return 0
	}

	if p, ok := params.(map[string]interface{}); ok {
		if v, ok := p[key]; ok {
			switch val := v.(type) {
			case float64:
				return val
			case int:
				return float64(val)
			case int64:
				return float64(val)
			}
		}
	}
	return 0
}
