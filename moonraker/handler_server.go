package moonraker

import (
	"encoding/json"
	"net/http"
)

// registerServerHandlers sets up /server/* routes.
func (s *Server) registerServerHandlers() {
	s.mux.HandleFunc("GET /server/info", s.handleServerInfo)
	s.mux.HandleFunc("GET /server/config", s.handleServerConfig)
	s.mux.HandleFunc("POST /server/restart", s.handleServerRestart)
}

func (s *Server) handleServerInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"result": s.serverInfo(),
	})
}

func (s *Server) serverInfo() map[string]interface{} {
	klippyState := "ready"
	if !s.printerClient.Connected() {
		klippyState = "error"
	}

	return map[string]interface{}{
		"klippy_connected":    s.printerClient.Connected(),
		"klippy_state":        klippyState,
		"components":          s.loadedComponents(),
		"failed_components":   []string{},
		"registered_directories": []string{"gcodes"},
		"warnings":            []string{},
		"websocket_count":     len(s.wsHub.clients),
		"moonraker_version":   "0.9.0-snapmaker",
		"api_version":         []int{1, 5, 0},
		"api_version_string":  "1.5.0",
	}
}

func (s *Server) loadedComponents() []string {
	return []string{
		"server",
		"file_manager",
		"klippy_apis",
		"machine",
		"data_store",
		"history",
		"octoprint_compat",
	}
}

func (s *Server) handleServerConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"result": map[string]interface{}{
			"config": map[string]interface{}{
				"server": map[string]interface{}{
					"host":            s.config.Server.Host,
					"port":            s.config.Server.Port,
					"klippy_uds_address": "/tmp/klippy_uds",
				},
			},
		},
	})
}

func (s *Server) handleServerRestart(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"result": "ok",
	})
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
