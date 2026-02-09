package moonraker

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// registerDatabaseHandlers sets up /server/database/* routes.
func (s *Server) registerDatabaseHandlers() {
	s.mux.HandleFunc("GET /server/database/list", s.handleDatabaseList)
	s.mux.HandleFunc("GET /server/database/item", s.handleDatabaseGetItem)
	s.mux.HandleFunc("POST /server/database/item", s.handleDatabasePostItem)
	s.mux.HandleFunc("DELETE /server/database/item", s.handleDatabaseDeleteItem)
}

func (s *Server) handleDatabaseList(w http.ResponseWriter, r *http.Request) {
	namespaces := s.database.ListNamespaces()
	writeJSON(w, map[string]interface{}{
		"result": map[string]interface{}{
			"namespaces": namespaces,
		},
	})
}

func (s *Server) handleDatabaseGetItem(w http.ResponseWriter, r *http.Request) {
	namespace := r.URL.Query().Get("namespace")
	key := r.URL.Query().Get("key")

	if namespace == "" {
		writeJSONError(w, http.StatusBadRequest, "namespace is required")
		return
	}

	// If no key, return entire namespace
	if key == "" {
		ns, ok := s.database.GetNamespace(namespace)
		if !ok {
			writeJSON(w, map[string]interface{}{
				"result": map[string]interface{}{
					"namespace": namespace,
					"value":     map[string]interface{}{},
				},
			})
			return
		}
		writeJSON(w, map[string]interface{}{
			"result": map[string]interface{}{
				"namespace": namespace,
				"value":     ns,
			},
		})
		return
	}

	value, ok := s.database.GetItem(namespace, key)
	if !ok {
		writeJSON(w, map[string]interface{}{
			"result": map[string]interface{}{
				"namespace": namespace,
				"key":       key,
				"value":     nil,
			},
		})
		return
	}

	writeJSON(w, map[string]interface{}{
		"result": map[string]interface{}{
			"namespace": namespace,
			"key":       key,
			"value":     value,
		},
	})
}

func (s *Server) handleDatabasePostItem(w http.ResponseWriter, r *http.Request) {
	namespace := r.URL.Query().Get("namespace")
	if namespace == "" {
		writeJSONError(w, http.StatusBadRequest, "namespace is required")
		return
	}

	// Parse request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	var req struct {
		Key   string      `json:"key"`
		Value interface{} `json:"value"`
	}

	// Try to parse as JSON
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			// Check if key is in query params
			req.Key = r.URL.Query().Get("key")
			// Try to parse body as just the value
			var value interface{}
			if err := json.Unmarshal(body, &value); err == nil {
				req.Value = value
			}
		}
	} else {
		req.Key = r.URL.Query().Get("key")
		// No body, check for value in query (less common)
		if v := r.URL.Query().Get("value"); v != "" {
			req.Value = v
		}
	}

	if req.Key == "" {
		writeJSONError(w, http.StatusBadRequest, "key is required")
		return
	}

	if err := s.database.SetItem(namespace, req.Key, req.Value); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, map[string]interface{}{
		"result": map[string]interface{}{
			"namespace": namespace,
			"key":       req.Key,
			"value":     req.Value,
		},
	})
}

func (s *Server) handleDatabaseDeleteItem(w http.ResponseWriter, r *http.Request) {
	namespace := r.URL.Query().Get("namespace")
	key := r.URL.Query().Get("key")

	if namespace == "" {
		writeJSONError(w, http.StatusBadRequest, "namespace is required")
		return
	}

	if key == "" {
		writeJSONError(w, http.StatusBadRequest, "key is required")
		return
	}

	// Get the value before deletion for the response
	value, _ := s.database.GetItem(namespace, key)

	if err := s.database.DeleteItem(namespace, key); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, map[string]interface{}{
		"result": map[string]interface{}{
			"namespace": namespace,
			"key":       key,
			"value":     value,
		},
	})
}

// Database JSON-RPC handlers for WebSocket

func (h *WSHub) handleDatabaseList() interface{} {
	namespaces := h.server.database.ListNamespaces()
	return map[string]interface{}{
		"namespaces": namespaces,
	}
}

func (h *WSHub) handleDatabaseGetItem(params interface{}) interface{} {
	namespace := extractStringParam(params, "namespace")
	key := extractStringParam(params, "key")

	if namespace == "" {
		return map[string]interface{}{"error": "namespace is required"}
	}

	if key == "" {
		ns, _ := h.server.database.GetNamespace(namespace)
		if ns == nil {
			ns = make(map[string]interface{})
		}
		return map[string]interface{}{
			"namespace": namespace,
			"value":     ns,
		}
	}

	value, _ := h.server.database.GetItem(namespace, key)
	return map[string]interface{}{
		"namespace": namespace,
		"key":       key,
		"value":     value,
	}
}

func (h *WSHub) handleDatabasePostItem(params interface{}) interface{} {
	namespace := extractStringParam(params, "namespace")
	key := extractStringParam(params, "key")

	if namespace == "" || key == "" {
		return map[string]interface{}{"error": "namespace and key are required"}
	}

	var value interface{}
	if p, ok := params.(map[string]interface{}); ok {
		value = p["value"]
	}

	if err := h.server.database.SetItem(namespace, key, value); err != nil {
		return map[string]interface{}{"error": err.Error()}
	}

	return map[string]interface{}{
		"namespace": namespace,
		"key":       key,
		"value":     value,
	}
}

func (h *WSHub) handleDatabaseDeleteItem(params interface{}) interface{} {
	namespace := extractStringParam(params, "namespace")
	key := extractStringParam(params, "key")

	if namespace == "" || key == "" {
		return map[string]interface{}{"error": "namespace and key are required"}
	}

	value, _ := h.server.database.GetItem(namespace, key)
	h.server.database.DeleteItem(namespace, key)

	return map[string]interface{}{
		"namespace": namespace,
		"key":       key,
		"value":     value,
	}
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{
			"code":    status,
			"message": message,
		},
	})
}

// extractStringParam now handles nested keys via dot notation
func extractNestedStringParam(params interface{}, keys ...string) string {
	if params == nil {
		return ""
	}

	p, ok := params.(map[string]interface{})
	if !ok {
		return ""
	}

	for _, key := range keys {
		parts := strings.Split(key, ".")
		current := p

		for i, part := range parts {
			if i == len(parts)-1 {
				if v, ok := current[part].(string); ok {
					return v
				}
			} else {
				next, ok := current[part].(map[string]interface{})
				if !ok {
					break
				}
				current = next
			}
		}
	}

	return ""
}
