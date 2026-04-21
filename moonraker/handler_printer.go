package moonraker

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
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
	// Klippy state is always "ready". Mainsail treats anything other than
	// "ready" here as "klipper is starting/not ready" and refuses to
	// dispatch printer/init → printer.objects.list → printer.objects.subscribe,
	// falling back to a 2-second poll of printer.info instead. Actual print
	// state is conveyed via the print_stats object, not here.
	return map[string]interface{}{
		"state":            "ready",
		"state_message":    "",
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
	objects := &PrinterObjects{server: s}
	writeJSON(w, map[string]interface{}{
		"result": map[string]interface{}{
			"objects": objects.AvailableObjects(),
		},
	})
}

func (s *Server) handleObjectsQuery(w http.ResponseWriter, r *http.Request) {
	objects := &PrinterObjects{server: s}
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

	// Intercept Klipper-specific commands (ACTIVATE_EXTRUDER, SET_HEATER_TEMPERATURE, etc.)
	// and temperature GCodes (M104/M109/M140/M190) to route through SACP directly.
	if handled, err := s.interceptGCode(body.Script); handled {
		if err != nil {
			log.Printf("GCode intercept error: %v", err)
			s.wsHub.BroadcastNotification("notify_gcode_response", []interface{}{
				"Error: " + err.Error(),
			})
		}
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

// interceptGCode handles Klipper-specific commands that the Snapmaker doesn't
// understand natively. Returns (handled, error). If handled is true, the caller
// should not forward the command to ExecuteGCode.
//
// Multi-line scripts (e.g. from the klipper-nfc daemon's action:prompt builder)
// are split and each line intercepted individually. If any line is not a known
// Klipper-only command, the script is passed through untouched so mixed scripts
// still reach ExecuteGCode as a unit.
func (s *Server) interceptGCode(script string) (bool, error) {
	if strings.ContainsAny(script, "\r\n") {
		lines := strings.FieldsFunc(script, func(r rune) bool { return r == '\n' || r == '\r' })
		// First pass: verify every non-empty line is a known Klipper command.
		for _, line := range lines {
			if strings.TrimSpace(line) == "" {
				continue
			}
			if !isKlipperCommand(line) {
				return false, nil
			}
		}
		// Second pass: execute each line's handler.
		for _, line := range lines {
			if strings.TrimSpace(line) == "" {
				continue
			}
			if _, err := s.interceptSingleGCode(line); err != nil {
				return true, err
			}
		}
		return true, nil
	}
	return s.interceptSingleGCode(script)
}

// isKlipperCommand returns true if the first word of the line matches a
// Klipper-specific command that interceptSingleGCode knows how to handle.
func isKlipperCommand(line string) bool {
	upper := strings.ToUpper(strings.TrimSpace(line))
	fields := strings.Fields(upper)
	if len(fields) == 0 {
		return false
	}
	switch fields[0] {
	case "ACTIVATE_EXTRUDER", "SET_HEATER_TEMPERATURE", "SET_GCODE_OFFSET",
		"SAVE_VARIABLE", "SET_GCODE_VARIABLE", "RESPOND",
		"NFC_ASSIGN_TOOL", "NFC_CANCEL",
		"TURN_OFF_HEATERS", "SET_FAN_SPEED",
		"M104", "M109", "M140", "M190", "M106", "M107":
		return true
	}
	return false
}

func (s *Server) interceptSingleGCode(script string) (bool, error) {
	upperScript := strings.ToUpper(strings.TrimSpace(script))
	fields := strings.Fields(upperScript)
	if len(fields) == 0 {
		return false, nil
	}
	cmd := fields[0]

	switch cmd {
	case "ACTIVATE_EXTRUDER":
		return s.handleActivateExtruder(script)
	case "SET_HEATER_TEMPERATURE":
		return s.handleSetHeaterTemperature(script)
	case "SET_GCODE_OFFSET":
		// M290 baby-stepping is accepted by the J1S firmware (result code 0)
		// but has no effect — BABYSTEPPING is likely disabled in Snapmaker's
		// Marlin configuration. Ignore silently so Mainsail doesn't error.
		return true, nil
	case "SAVE_VARIABLE":
		// Klipper-only command (requires [save_variables] section). Silently
		// accept so the NFC spoolman daemon can run with klipper_variables=true
		// without generating errors on the Snapmaker.
		return true, nil
	case "SET_GCODE_VARIABLE":
		// Klipper macro variable setter. We handle MACRO=_NFC_STATE natively
		// to store pending spool data from the NFC daemon. Other macros are
		// silently accepted since there are no real macros on the Snapmaker.
		return s.handleSetGCodeVariable(script)
	case "RESPOND":
		// Klipper's response module — used by the NFC daemon to emit
		// action:prompt_* messages that Mainsail parses to render a prompt
		// dialog. Broadcast the MSG as a notify_gcode_response so Mainsail
		// receives it identically to a real Klipper setup.
		return s.handleRespond(script)
	case "NFC_ASSIGN_TOOL":
		return s.handleNFCAssignTool(script)
	case "NFC_CANCEL":
		return s.handleNFCCancel()
	case "TURN_OFF_HEATERS":
		return s.handleTurnOffHeaters()
	case "M104", "M109":
		return s.handleMSetExtruderTemp(script, cmd)
	case "M140", "M190":
		return s.handleMSetBedTemp(script)
	case "M106":
		return s.handleM106(script)
	case "M107":
		return s.handleM107(script)
	case "SET_FAN_SPEED":
		return s.handleSetFanSpeed(script)
	}

	return false, nil
}

// handleActivateExtruder handles ACTIVATE_EXTRUDER EXTRUDER=extruder1.
// Sends a T0/T1 GCode command and updates the active extruder state.
func (s *Server) handleActivateExtruder(script string) (bool, error) {
	name := extractKlipperParam(script, "EXTRUDER")
	if name == "" {
		return true, fmt.Errorf("ACTIVATE_EXTRUDER: missing EXTRUDER parameter")
	}

	var toolID int
	switch name {
	case "extruder":
		toolID = 0
	case "extruder1":
		toolID = 1
	default:
		return true, fmt.Errorf("ACTIVATE_EXTRUDER: unknown extruder %q", name)
	}

	gcmd := fmt.Sprintf("T%d", toolID)
	if _, err := s.printerClient.ExecuteGCode(gcmd); err != nil {
		return true, fmt.Errorf("ACTIVATE_EXTRUDER: %w", err)
	}

	s.state.SetActiveExtruder(name)
	log.Printf("Active extruder changed to %s (T%d)", name, toolID)
	return true, nil
}

// handleSetGCodeOffset handles SET_GCODE_OFFSET Z=x Z_ADJUST=x MOVE=1.
// Translates to M290 baby-stepping commands on the Snapmaker.
func (s *Server) handleSetGCodeOffset(script string) (bool, error) {
	zStr := extractKlipperParam(script, "Z_ADJUST")
	if zStr != "" {
		// Relative adjustment: SET_GCODE_OFFSET Z_ADJUST=0.05 MOVE=1
		delta, err := strconv.ParseFloat(zStr, 64)
		if err != nil {
			return true, fmt.Errorf("SET_GCODE_OFFSET: invalid Z_ADJUST value %q", zStr)
		}
		gcmd := fmt.Sprintf("M290 Z%.3f", delta)
		if _, err := s.printerClient.ExecuteGCode(gcmd); err != nil {
			return true, fmt.Errorf("SET_GCODE_OFFSET: %w", err)
		}
		newOffset := s.state.AdjustZOffset(delta)
		log.Printf("Z offset adjusted by %.3f to %.3f", delta, newOffset)
		return true, nil
	}

	zStr = extractKlipperParam(script, "Z")
	if zStr != "" {
		// Absolute offset: SET_GCODE_OFFSET Z=0.1 MOVE=1
		offset, err := strconv.ParseFloat(zStr, 64)
		if err != nil {
			return true, fmt.Errorf("SET_GCODE_OFFSET: invalid Z value %q", zStr)
		}
		snap := s.state.Snapshot()
		delta := offset - snap.ZOffset
		if delta != 0 {
			gcmd := fmt.Sprintf("M290 Z%.3f", delta)
			if _, err := s.printerClient.ExecuteGCode(gcmd); err != nil {
				return true, fmt.Errorf("SET_GCODE_OFFSET: %w", err)
			}
		}
		s.state.SetZOffset(offset)
		log.Printf("Z offset set to %.3f", offset)
		return true, nil
	}

	// No Z parameter — ignore (could be X/Y offset, not relevant).
	return false, nil
}

// handleSetHeaterTemperature handles SET_HEATER_TEMPERATURE HEATER=extruder1 TARGET=200.
func (s *Server) handleSetHeaterTemperature(script string) (bool, error) {
	heater := extractKlipperParam(script, "HEATER")
	targetStr := extractKlipperParam(script, "TARGET")
	if heater == "" {
		return true, fmt.Errorf("SET_HEATER_TEMPERATURE: missing HEATER parameter")
	}

	target := 0
	if targetStr != "" {
		if v, err := strconv.ParseFloat(targetStr, 64); err == nil {
			target = int(v)
		}
	}

	switch heater {
	case "extruder":
		return true, s.printerClient.SetToolTemperature(0, target)
	case "extruder1":
		return true, s.printerClient.SetToolTemperature(1, target)
	case "heater_bed":
		return true, s.printerClient.SetBedTemperature(0, target)
	default:
		return true, fmt.Errorf("SET_HEATER_TEMPERATURE: unknown heater %q", heater)
	}
}

// handleTurnOffHeaters turns off all heaters via SACP.
func (s *Server) handleTurnOffHeaters() (bool, error) {
	var errs []string
	if err := s.printerClient.SetToolTemperature(0, 0); err != nil {
		errs = append(errs, err.Error())
	}
	if err := s.printerClient.SetToolTemperature(1, 0); err != nil {
		errs = append(errs, err.Error())
	}
	if err := s.printerClient.SetBedTemperature(0, 0); err != nil {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		return true, fmt.Errorf("TURN_OFF_HEATERS: %s", strings.Join(errs, "; "))
	}
	return true, nil
}

// handleMSetExtruderTemp handles M104/M109 with optional T parameter.
// M104 S200 T1 → SetToolTemperature(1, 200)
func (s *Server) handleMSetExtruderTemp(script string, cmd string) (bool, error) {
	temp := parseGCodeIntParam(script, 'S')
	toolID := parseGCodeIntParam(script, 'T')

	// If no T parameter, use the active extruder.
	if toolID < 0 {
		snap := s.state.Snapshot()
		if snap.ActiveExtruder == "extruder1" {
			toolID = 1
		} else {
			toolID = 0
		}
	}

	return true, s.printerClient.SetToolTemperature(toolID, temp)
}

// handleMSetBedTemp handles M140/M190 — always routes through SACP.
func (s *Server) handleMSetBedTemp(script string) (bool, error) {
	temp := parseGCodeIntParam(script, 'S')
	if temp < 0 {
		temp = 0
	}
	return true, s.printerClient.SetBedTemperature(0, temp)
}

// handleM106 handles M106 (set fan speed) with optional P parameter for fan index.
// When no P is specified, targets the active extruder's fan (Klipper behavior).
func (s *Server) handleM106(script string) (bool, error) {
	speed := parseGCodeIntParam(script, 'S')
	if speed < 0 {
		speed = 255 // M106 with no S defaults to full speed
	}

	fanIndex := parseGCodeIntParam(script, 'P')
	if fanIndex < 0 {
		// No P parameter: use active extruder's fan.
		snap := s.state.Snapshot()
		if snap.ActiveExtruder == "extruder1" {
			fanIndex = 1
		} else {
			fanIndex = 0
		}
	}

	gcmd := fmt.Sprintf("M106 P%d S%d", fanIndex, speed)
	_, err := s.printerClient.ExecuteGCode(gcmd)
	return true, err
}

// handleM107 handles M107 (turn off fan) with optional P parameter.
func (s *Server) handleM107(script string) (bool, error) {
	fanIndex := parseGCodeIntParam(script, 'P')
	if fanIndex < 0 {
		snap := s.state.Snapshot()
		if snap.ActiveExtruder == "extruder1" {
			fanIndex = 1
		} else {
			fanIndex = 0
		}
	}

	gcmd := fmt.Sprintf("M106 P%d S0", fanIndex)
	_, err := s.printerClient.ExecuteGCode(gcmd)
	return true, err
}

// handleSetFanSpeed handles Klipper-style SET_FAN_SPEED FAN=name SPEED=0.0-1.0.
func (s *Server) handleSetFanSpeed(script string) (bool, error) {
	fanName := extractKlipperParam(script, "FAN")
	speedStr := extractKlipperParam(script, "SPEED")

	speed := 0.0
	if speedStr != "" {
		if v, err := strconv.ParseFloat(speedStr, 64); err == nil {
			speed = v
		}
	}

	fanIndex := 0
	switch fanName {
	case "extruder1_partfan":
		fanIndex = 1
	case "extruder_partfan", "":
		fanIndex = 0
	}

	s255 := int(speed * 255)
	if s255 > 255 {
		s255 = 255
	}
	gcmd := fmt.Sprintf("M106 P%d S%d", fanIndex, s255)
	_, err := s.printerClient.ExecuteGCode(gcmd)
	return true, err
}

// extractKlipperParam extracts a named parameter from a Klipper-style command.
// e.g., extractKlipperParam("SET_HEATER_TEMPERATURE HEATER=extruder1 TARGET=200", "HEATER") = "extruder1"
func extractKlipperParam(script string, param string) string {
	prefix := strings.ToUpper(param) + "="
	for _, field := range strings.Fields(script) {
		upper := strings.ToUpper(field)
		if strings.HasPrefix(upper, prefix) {
			// Return the original-case value after the '='.
			idx := strings.IndexByte(field, '=')
			if idx >= 0 {
				return field[idx+1:]
			}
		}
	}
	return ""
}

// parseGCodeIntParam extracts a single-letter GCode parameter value.
// e.g., parseGCodeIntParam("M104 S200 T1", 'S') = 200
// Returns -1 if not found.
func parseGCodeIntParam(script string, param byte) int {
	upper := strings.ToUpper(script)
	p := string(param)
	for _, field := range strings.Fields(upper) {
		if strings.HasPrefix(field, p) && len(field) > 1 {
			if v, err := strconv.ParseFloat(field[1:], 64); err == nil {
				return int(v)
			}
		}
	}
	return -1
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
					s.StartSpoolmanTracking(filename)
				}
			}()
		}
	}

	writeJSON(w, map[string]interface{}{
		"result": map[string]interface{}{},
	})
}

// StartSpoolmanTracking initiates filament usage tracking if Spoolman is configured.
func (s *Server) StartSpoolmanTracking(filename string) {
	if s.spoolman == nil || !s.spoolman.HasAnySpool() {
		return
	}

	gcodeDir := s.fileManager.GetRootPath("gcodes")
	fullPath := filepath.Join(gcodeDir, filepath.FromSlash(filename))

	filamentByTool, err := files.ParseFilamentByLinePerTool(fullPath)
	if err != nil {
		log.Printf("Spoolman: failed to parse filament data from %s: %v", filename, err)
		return
	}

	// Start tracking if at least one tool has filament data.
	hasData := false
	for t := 0; t < 2; t++ {
		if len(filamentByTool[t]) > 0 && filamentByTool[t][len(filamentByTool[t])-1] > 0 {
			hasData = true
		}
	}
	if hasData {
		s.spoolman.StartTracking(filamentByTool)
	}
}

func (s *Server) handlePrintPause(w http.ResponseWriter, r *http.Request) {
	if err := s.printerClient.PausePrint(); err != nil {
		log.Printf("Pause error: %v", err)
	}
	writeJSON(w, map[string]interface{}{
		"result": map[string]interface{}{},
	})
}

func (s *Server) handlePrintResume(w http.ResponseWriter, r *http.Request) {
	if err := s.printerClient.ResumePrint(); err != nil {
		log.Printf("Resume error: %v", err)
	}
	writeJSON(w, map[string]interface{}{
		"result": map[string]interface{}{},
	})
}

func (s *Server) handlePrintCancel(w http.ResponseWriter, r *http.Request) {
	if err := s.printerClient.StopPrint(); err != nil {
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
