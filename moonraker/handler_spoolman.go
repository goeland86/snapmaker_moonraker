package moonraker

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
)

// registerSpoolmanHandlers sets up /server/spoolman/* routes.
func (s *Server) registerSpoolmanHandlers() {
	s.mux.HandleFunc("GET /server/spoolman/status", s.handleSpoolmanStatus)
	s.mux.HandleFunc("GET /server/spoolman/spool_id", s.handleSpoolmanGetSpoolID)
	s.mux.HandleFunc("POST /server/spoolman/spool_id", s.handleSpoolmanSetSpoolID)
	s.mux.HandleFunc("POST /server/spoolman/proxy", s.handleSpoolmanProxy)
}

func (s *Server) handleSpoolmanStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"result": s.spoolman.Status(),
	})
}

func (s *Server) handleSpoolmanGetSpoolID(w http.ResponseWriter, r *http.Request) {
	tool := 0
	if t := r.URL.Query().Get("tool"); t != "" {
		if v, err := strconv.Atoi(t); err == nil {
			tool = v
		}
	}
	writeJSON(w, map[string]interface{}{
		"result": map[string]interface{}{
			"spool_id": spoolIDOrNull(s.spoolman.GetSpoolID(tool)),
			"tool":     tool,
		},
	})
}

func (s *Server) handleSpoolmanSetSpoolID(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SpoolID int  `json:"spool_id"`
		Tool    *int `json:"tool,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, map[string]interface{}{
			"error": map[string]interface{}{
				"code":    400,
				"message": "invalid request body",
			},
		})
		return
	}

	tool := 0
	if body.Tool != nil {
		tool = *body.Tool
	}

	if err := s.spoolman.SetSpoolID(body.SpoolID, tool); err != nil {
		writeJSON(w, map[string]interface{}{
			"error": map[string]interface{}{
				"code":    500,
				"message": err.Error(),
			},
		})
		return
	}

	writeJSON(w, map[string]interface{}{
		"result": map[string]interface{}{
			"spool_id": spoolIDOrNull(s.spoolman.GetSpoolID(tool)),
			"tool":     tool,
		},
	})
}

// spoolIDOrNull converts a spool ID to nil if it's 0 (no spool),
// so JSON marshals as null instead of 0 — Mainsail expects null for "no spool".
func spoolIDOrNull(id int) interface{} {
	if id == 0 {
		return nil
	}
	return id
}

func (s *Server) handleSpoolmanProxy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Method string      `json:"request_method"`
		Path   string      `json:"path"`
		Query  string      `json:"query"`
		Body   interface{} `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, map[string]interface{}{
			"error": map[string]interface{}{
				"code":    400,
				"message": "invalid request body",
			},
		})
		return
	}

	if body.Method == "" {
		body.Method = "GET"
	}

	// Marshal the body back to JSON if present.
	var bodyReader *strings.Reader
	if body.Body != nil {
		bodyJSON, _ := json.Marshal(body.Body)
		bodyReader = strings.NewReader(string(bodyJSON))
	} else {
		bodyReader = strings.NewReader("")
	}

	statusCode, result, err := s.spoolman.Proxy(body.Method, body.Path, body.Query, bodyReader)
	if err != nil {
		log.Printf("Spoolman proxy error: %v", err)
		writeJSON(w, map[string]interface{}{
			"error": map[string]interface{}{
				"code":    502,
				"message": err.Error(),
			},
		})
		return
	}

	// Return the proxied response as the result.
	if statusCode >= 200 && statusCode < 300 {
		writeJSON(w, map[string]interface{}{
			"result": result,
		})
	} else {
		writeJSON(w, map[string]interface{}{
			"error": map[string]interface{}{
				"code":    statusCode,
				"message": "spoolman returned error",
				"data":    result,
			},
		})
	}
}

// --- WebSocket RPC handlers ---

func (h *WSHub) handleSpoolmanStatus() interface{} {
	return h.server.spoolman.Status()
}

func (h *WSHub) handleSpoolmanGetSpoolID(params interface{}) interface{} {
	tool := extractIntParam(params, "tool")
	return map[string]interface{}{
		"spool_id": spoolIDOrNull(h.server.spoolman.GetSpoolID(tool)),
		"tool":     tool,
	}
}

func (h *WSHub) handleSpoolmanSetSpoolID(params interface{}) interface{} {
	spoolID := extractIntParam(params, "spool_id")
	tool := extractIntParam(params, "tool")

	if err := h.server.spoolman.SetSpoolID(spoolID, tool); err != nil {
		log.Printf("Spoolman set spool ID error: %v", err)
		return map[string]interface{}{"error": err.Error()}
	}

	return map[string]interface{}{
		"spool_id": spoolIDOrNull(h.server.spoolman.GetSpoolID(tool)),
		"tool":     tool,
	}
}

func (h *WSHub) handleSpoolmanProxy(params interface{}) interface{} {
	method := extractStringParam(params, "request_method")
	path := extractStringParam(params, "path")
	query := extractStringParam(params, "query")

	if method == "" {
		method = "GET"
	}

	// Extract body from params.
	var bodyReader *strings.Reader
	if p, ok := params.(map[string]interface{}); ok {
		if bodyVal, exists := p["body"]; exists && bodyVal != nil {
			bodyJSON, _ := json.Marshal(bodyVal)
			bodyReader = strings.NewReader(string(bodyJSON))
		}
	}
	if bodyReader == nil {
		bodyReader = strings.NewReader("")
	}

	statusCode, result, err := h.server.spoolman.Proxy(method, path, query, bodyReader)
	if err != nil {
		log.Printf("Spoolman proxy error: %v", err)
		return map[string]interface{}{"error": err.Error()}
	}

	if statusCode >= 200 && statusCode < 300 {
		return result
	}

	return map[string]interface{}{
		"error":       "spoolman returned error",
		"status_code": statusCode,
		"data":        result,
	}
}
