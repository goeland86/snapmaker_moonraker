package moonraker

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
)

// NFCState holds pending spool data from the klipper-nfc daemon between a tag
// scan and the user's tool assignment via the action:prompt dialog. The daemon
// writes these fields via SET_GCODE_VARIABLE MACRO=_NFC_STATE (which the bridge
// intercepts natively — there is no Klipper macro system behind it).
type NFCState struct {
	mu     sync.Mutex
	fields map[string]string
}

func NewNFCState() *NFCState {
	return &NFCState{fields: make(map[string]string)}
}

// SetField stores a pending spool field. VALUE is unquoted if wrapped as
// '"..."' (the daemon quotes string values so Klipper's SAVE_VARIABLE would
// accept them as strings rather than identifiers).
func (s *NFCState) SetField(name, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fields[name] = unquote(value)
}

// GetInt reads a pending field as an integer. Returns 0 if missing or invalid.
func (s *NFCState) GetInt(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, _ := strconv.Atoi(s.fields[name])
	return v
}

// GetString reads a pending field as a string. Returns "" if missing.
func (s *NFCState) GetString(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fields[name]
}

// Clear resets all pending fields (after assignment or cancellation).
func (s *NFCState) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fields = make(map[string]string)
}

// unquote strips a single layer of single-outer then double-inner quotes, i.e.
// `'"PLA"'` → `PLA`. Also handles plain `"PLA"` and unquoted values.
func unquote(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
		v = v[1 : len(v)-1]
	}
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		v = v[1 : len(v)-1]
	}
	return v
}

// handleSetGCodeVariable intercepts SET_GCODE_VARIABLE MACRO=_NFC_STATE.
// Returns (handled, error). Non-NFC macros are ignored.
func (s *Server) handleSetGCodeVariable(script string) (bool, error) {
	macro := extractKlipperParam(script, "MACRO")
	if macro != "_NFC_STATE" {
		// Not for us — let the caller fall through (other SET_GCODE_VARIABLE
		// calls are silently dropped by the interceptor above).
		return true, nil
	}

	name := extractKlipperParam(script, "VARIABLE")
	if name == "" {
		return true, nil
	}
	// VALUE may contain spaces when wrapped as '"..."' (e.g., filament names),
	// so use the quote-aware extractor instead of the whitespace-delimited one.
	value := extractQuotedParam(script, "VALUE")

	s.nfcState.SetField(strings.ToLower(name), value)
	return true, nil
}

// handleRespond intercepts RESPOND commands. Two flavors matter:
//
//	RESPOND TYPE=command MSG="action:..."  → broadcast as "// action:..."
//	  (Mainsail's prompt dialog listens for these notify_gcode_response strings)
//	RESPOND MSG="text"                      → broadcast as "// text" for console
func (s *Server) handleRespond(script string) (bool, error) {
	msg := extractQuotedParam(script, "MSG")
	if msg == "" {
		return true, nil
	}
	// Klipper conventionally prefixes command-type responses with "// ".
	// Mainsail's prompt parser requires this exact prefix.
	s.wsHub.BroadcastGCodeResponse("// " + msg)
	return true, nil
}

// handleNFCAssignTool processes NFC_ASSIGN_TOOL TOOL=<n>. Reads pending spool
// data, assigns it to the given tool via the Spoolman manager, then closes the
// prompt and clears pending state.
func (s *Server) handleNFCAssignTool(script string) (bool, error) {
	if s.nfcState == nil {
		return true, nil
	}

	tool := parseGCodeIntParam(script, 'T')
	if tool < 0 {
		// Fall back to Klipper-style TOOL=n.
		if v := extractKlipperParam(script, "TOOL"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				tool = n
			}
		}
	}
	if tool < 0 || tool > 1 {
		return true, nil
	}

	spoolID := s.nfcState.GetInt("pending_spool_id")
	if spoolID <= 0 {
		s.wsHub.BroadcastGCodeResponse("!! No pending NFC spool to assign")
		s.wsHub.BroadcastGCodeResponse("// action:prompt_end")
		return true, nil
	}

	if s.spoolman != nil {
		if err := s.spoolman.SetSpoolID(spoolID, tool); err != nil {
			log.Printf("NFC_ASSIGN_TOOL: SetSpoolID failed: %v", err)
			s.wsHub.BroadcastGCodeResponse("!! NFC assign failed: " + err.Error())
			s.wsHub.BroadcastGCodeResponse("// action:prompt_end")
			return true, nil
		}
	}

	vendor := s.nfcState.GetString("pending_vendor")
	material := s.nfcState.GetString("pending_material")

	// Persist per-tool metadata to the save_variables namespace so queries to
	// printer.save_variables.variables.<name> (from Mainsail, macros, or the
	// klipper-nfc NFC_STATUS helper) see the assignment. Mirrors what the
	// Klipper-side NFC_ASSIGN_TOOL macro writes via SAVE_VARIABLE.
	s.persistNFCMetadata(tool, spoolID)

	s.wsHub.BroadcastGCodeResponse("// action:prompt_end")
	s.wsHub.BroadcastGCodeResponse(
		"// Spool #" + strconv.Itoa(spoolID) + " (" + vendor + " " + material +
			") assigned to T" + strconv.Itoa(tool),
	)
	s.nfcState.Clear()

	log.Printf("NFC: assigned spool %d to T%d (%s %s)", spoolID, tool, vendor, material)
	return true, nil
}

// persistNFCMetadata writes nfc_t<tool>_* fields to the save_variables
// database namespace so printer.save_variables.variables exposes them.
func (s *Server) persistNFCMetadata(tool, spoolID int) {
	if s.database == nil {
		return
	}
	prefix := fmt.Sprintf("nfc_t%d_", tool)
	fields := map[string]interface{}{
		prefix + "spool_id":      spoolID,
		prefix + "material":      s.nfcState.GetString("pending_material"),
		prefix + "vendor":        s.nfcState.GetString("pending_vendor"),
		prefix + "name":          s.nfcState.GetString("pending_name"),
		prefix + "color":         s.nfcState.GetString("pending_color"),
		prefix + "extruder_temp": s.nfcState.GetInt("pending_extruder_temp"),
		prefix + "bed_temp":      s.nfcState.GetInt("pending_bed_temp"),
	}
	for key, val := range fields {
		if err := s.database.SetItem("save_variables", key, val); err != nil {
			log.Printf("NFC: failed to persist %s: %v", key, err)
		}
	}
}

// handleNFCCancel processes NFC_CANCEL: close the prompt and clear pending.
func (s *Server) handleNFCCancel() (bool, error) {
	if s.nfcState != nil {
		s.nfcState.Clear()
	}
	s.wsHub.BroadcastGCodeResponse("// action:prompt_end")
	s.wsHub.BroadcastGCodeResponse("// NFC spool assignment cancelled")
	return true, nil
}

// extractQuotedParam extracts a quoted parameter value from a Klipper-style
// command, e.g. `RESPOND MSG="hello world"` → `hello world`. Handles both
// double and single quotes, plus unquoted single-token values.
func extractQuotedParam(script string, param string) string {
	prefix := strings.ToUpper(param) + "="
	upper := strings.ToUpper(script)
	idx := strings.Index(upper, prefix)
	if idx < 0 {
		return ""
	}
	rest := script[idx+len(prefix):]
	if rest == "" {
		return ""
	}
	// Quoted form: find the matching closing quote.
	if q := rest[0]; q == '"' || q == '\'' {
		end := strings.IndexByte(rest[1:], q)
		if end < 0 {
			return rest[1:] // unterminated — take everything after opening quote
		}
		return rest[1 : 1+end]
	}
	// Unquoted: take up to the next whitespace.
	if end := strings.IndexAny(rest, " \t\r\n"); end >= 0 {
		return rest[:end]
	}
	return rest
}
