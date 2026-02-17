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
				"value":     map[string]interface{}{},
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
	var namespace, key string
	var value interface{}

	contentType := r.Header.Get("Content-Type")

	if strings.HasPrefix(contentType, "application/json") {
		// JSON body
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "failed to read request body")
			return
		}
		var req struct {
			Namespace string      `json:"namespace"`
			Key       string      `json:"key"`
			Value     interface{} `json:"value"`
		}
		if len(body) > 0 {
			json.Unmarshal(body, &req)
		}
		namespace = req.Namespace
		key = req.Key
		value = req.Value
	} else {
		// Form-encoded body (used by moonraker-obico)
		r.ParseForm()
		namespace = r.FormValue("namespace")
		key = r.FormValue("key")
		if v := r.FormValue("value"); v != "" {
			// Try to parse as JSON, fall back to string
			var parsed interface{}
			if err := json.Unmarshal([]byte(v), &parsed); err == nil {
				value = parsed
			} else {
				value = v
			}
		}
	}

	// Query params override body values
	if v := r.URL.Query().Get("namespace"); v != "" {
		namespace = v
	}
	if v := r.URL.Query().Get("key"); v != "" {
		key = v
	}

	if namespace == "" {
		writeJSONError(w, http.StatusBadRequest, "namespace is required")
		return
	}
	if key == "" {
		writeJSONError(w, http.StatusBadRequest, "key is required")
		return
	}

	if err := s.database.SetItem(namespace, key, value); err != nil {
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
