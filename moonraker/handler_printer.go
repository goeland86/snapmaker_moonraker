package moonraker

import (
	"encoding/json"
	"log"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/john/snapmaker_moonraker/files"
)

// registerPrinterHandlers sets up /printer/* routes.
func (s *Server) registerPrinterHandlers() {
	s.mux.HandleFunc("GET /printer/info", s.handlePrinterInfo)
	s.mux.HandleFunc("GET /printer/objects/list", s.handleObjectsList)
	s.mux.HandleFunc("GET /printer/objects/query", s.handleObjectsQuery)
	s.mux.HandleFunc("POST /printer/objects/query", s.handleObjectsQuery)
	s.mux.HandleFunc("POST /printer/gcode/script", s.handleGCodeScript)
	s.mux.HandleFunc("POST /printer/print/start", s.handlePrintStart)
	s.mux.HandleFunc("POST /printer/print/pause", s.handlePrintPause)
	s.mux.HandleFunc("POST /printer/print/resume", s.handlePrintResume)
	s.mux.HandleFunc("POST /printer/print/cancel", s.handlePrintCancel)
	s.mux.HandleFunc("POST /printer/emergency_stop", s.handleEmergencyStop)
}

func (s *Server) handlePrinterInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"result": s.printerInfo(),
	})
}

func (s *Server) printerInfo() map[string]interface{} {
	// Always report as ready so Mainsail loads the dashboard.
	// Actual printer state is reflected via printer objects (webhooks, print_stats).
	state := "ready"
	msg := ""

	snap := s.state.Snapshot()
	if snap.PrinterState == "printing" {
		state = "printing"
	}

	return map[string]interface{}{
		"state":            state,
		"state_message":    msg,
		"hostname":         "snapmaker-moonraker",
		"software_version": "v0.13.0-snapmaker_moonraker",
		"cpu_info":         "Snapmaker Moonraker Bridge",
		"klipper_path":     "/opt/snapmaker_moonraker",
		"python_path":      "",
		"log_file":         "",
		"config_file":      "",
	}
}

func (s *Server) handleObjectsList(w http.ResponseWriter, r *http.Request) {
	objects := &PrinterObjects{}
	writeJSON(w, map[string]interface{}{
		"result": map[string]interface{}{
			"objects": objects.AvailableObjects(),
		},
	})
}

func (s *Server) handleObjectsQuery(w http.ResponseWriter, r *http.Request) {
	objects := &PrinterObjects{}
	snap := s.state.Snapshot()

	// Parse requested objects from query params or body.
	requested := make(map[string]interface{})

	if r.Method == "GET" {
		// Query string format: ?toolhead&extruder=temperature,target
		for key, values := range r.URL.Query() {
			if len(values) > 0 && values[0] != "" {
				// Split comma-separated field list.
				fields := splitFields(values[0])
				ifaces := make([]interface{}, len(fields))
				for i, f := range fields {
					ifaces[i] = f
				}
				requested[key] = ifaces
			} else {
				requested[key] = nil
			}
		}
	} else {
		var body struct {
			Objects map[string]interface{} `json:"objects"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			requested = body.Objects
		}
	}

	status := objects.Query(snap, requested)

	writeJSON(w, map[string]interface{}{
		"result": map[string]interface{}{
			"eventtime": 0.0,
			"status":    status,
		},
	})
}

func (s *Server) handleGCodeScript(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Script string `json:"script"`
	}

	// Try JSON body first.
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Script == "" {
		// Fall back to query param.
		body.Script = r.URL.Query().Get("script")
	}

	if body.Script == "" {
		writeJSON(w, map[string]interface{}{
			"result": map[string]interface{}{},
		})
		return
	}

	// Intercept FIRMWARE_RESTART and RESTART to trigger printer reconnection.
	upperScript := strings.ToUpper(strings.TrimSpace(body.Script))
	if upperScript == "FIRMWARE_RESTART" || upperScript == "RESTART" {
		go func() {
			if err := s.printerClient.Reconnect(); err != nil {
				log.Printf("Reconnect failed: %v", err)
			}
		}()
		writeJSON(w, map[string]interface{}{
			"result": map[string]interface{}{},
		})
		return
	}

	// Intercept ? and HELP — these are Klipper console commands, not real GCode.
	if upperScript == "?" || upperScript == "HELP" {
		s.wsHub.BroadcastNotification("notify_gcode_response", []interface{}{gcodeHelpText()})
		writeJSON(w, map[string]interface{}{
			"result": map[string]interface{}{},
		})
		return
	}

	result, err := s.printerClient.ExecuteGCode(body.Script)
	if err != nil {
		log.Printf("GCode error: %v", err)
		s.wsHub.BroadcastNotification("notify_gcode_response", []interface{}{
			"Error: " + err.Error(),
		})
	}

	// Broadcast gcode response to WS clients.
	if result != "" {
		s.wsHub.BroadcastNotification("notify_gcode_response", []interface{}{result})
	}

	writeJSON(w, map[string]interface{}{
		"result": map[string]interface{}{},
	})
}

func (s *Server) handlePrintStart(w http.ResponseWriter, r *http.Request) {
	filename := r.URL.Query().Get("filename")
	if filename == "" {
		var body struct {
			Filename string `json:"filename"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		filename = body.Filename
	}

	if filename != "" {
		data, err := s.fileManager.ReadFile("gcodes", filename)
		if err != nil {
			log.Printf("Error reading file for print: %v", err)
		} else {
			// Run upload in background so the RPC response returns immediately.
			// Mainsail expects a fast response; status updates arrive via websocket
			// notifications as the printer state changes (idle → printing).
			go func() {
				if err := s.printerClient.Upload(filename, data); err != nil {
					log.Printf("Error uploading to printer: %v", err)
				} else {
					s.startSpoolmanTracking(filename)
				}
			}()
		}
	}

	writeJSON(w, map[string]interface{}{
		"result": map[string]interface{}{},
	})
}

// startSpoolmanTracking initiates filament usage tracking if Spoolman is configured.
func (s *Server) startSpoolmanTracking(filename string) {
	if s.spoolman == nil || s.spoolman.GetSpoolID() == 0 {
		return
	}

	gcodeDir := s.fileManager.GetRootPath("gcodes")
	fullPath := filepath.Join(gcodeDir, filepath.FromSlash(filename))

	filamentByLine, err := files.ParseFilamentByLine(fullPath)
	if err != nil {
		log.Printf("Spoolman: failed to parse filament data from %s: %v", filename, err)
		return
	}

	if len(filamentByLine) > 0 && filamentByLine[len(filamentByLine)-1] > 0 {
		s.spoolman.StartTracking(filamentByLine)
	}
}

func (s *Server) handlePrintPause(w http.ResponseWriter, r *http.Request) {
	if _, err := s.printerClient.ExecuteGCode("M25"); err != nil {
		log.Printf("Pause error: %v", err)
	}
	writeJSON(w, map[string]interface{}{
		"result": map[string]interface{}{},
	})
}

func (s *Server) handlePrintResume(w http.ResponseWriter, r *http.Request) {
	if _, err := s.printerClient.ExecuteGCode("M24"); err != nil {
		log.Printf("Resume error: %v", err)
	}
	writeJSON(w, map[string]interface{}{
		"result": map[string]interface{}{},
	})
}

func (s *Server) handlePrintCancel(w http.ResponseWriter, r *http.Request) {
	if _, err := s.printerClient.ExecuteGCode("M26"); err != nil {
		log.Printf("Cancel error: %v", err)
	}
	writeJSON(w, map[string]interface{}{
		"result": map[string]interface{}{},
	})
}

func (s *Server) handleEmergencyStop(w http.ResponseWriter, r *http.Request) {
	if _, err := s.printerClient.ExecuteGCode("M112"); err != nil {
		log.Printf("Emergency stop error: %v", err)
	}
	writeJSON(w, map[string]interface{}{
		"result": map[string]interface{}{},
	})
}

// gcodeHelpText returns a help message for the Mainsail console.
func gcodeHelpText() string {
	return "Snapmaker Moonraker Bridge - Supported Console Commands:\n" +
		"  RESTART - Reconnect to the printer\n" +
		"  FIRMWARE_RESTART - Reconnect to the printer\n" +
		"  HELP / ? - Show this help message\n" +
		"Standard GCode commands are forwarded to the printer (e.g. G28, M104, M140, G0/G1)."
}

func splitFields(s string) []string {
	var fields []string
	current := ""
	for _, c := range s {
		if c == ',' {
			if current != "" {
				fields = append(fields, current)
			}
			current = ""
		} else {
			current += string(c)
		}
	}
	if current != "" {
		fields = append(fields, current)
	}
	return fields
}
