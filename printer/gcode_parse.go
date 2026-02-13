package printer

import (
	"strconv"
	"strings"
)

// parseM105 parses an M105 temperature response into the status map.
// Typical M105 response formats:
//   "ok T:200.0 /210.0 B:60.0 /60.0 T0:200.0 /210.0 T1:25.0 /0.0"
//   "ok T0:200.0 /210.0 T1:25.0 /0.0 B:60.0 /60.0"
//   "T:200.00 /210.00 B:60.00 /60.00 T0:200.00 /210.00 T1:25.00 /0.00 @:127 B@:64"
func parseM105(resp string, result map[string]interface{}) {
	resp = strings.TrimPrefix(resp, "ok ")
	resp = strings.TrimPrefix(resp, "ok")
	resp = strings.TrimSpace(resp)

	// Split on spaces but handle "T0:200.0 /210.0" pairs (current / target).
	parts := strings.Fields(resp)

	for i := 0; i < len(parts); i++ {
		part := parts[i]
		if !strings.Contains(part, ":") {
			continue
		}

		kv := strings.SplitN(part, ":", 2)
		key := kv[0]
		valStr := kv[1]

		current, _ := strconv.ParseFloat(valStr, 64)

		// Check if next part is "/target"
		var target float64
		if i+1 < len(parts) && strings.HasPrefix(parts[i+1], "/") {
			targetStr := strings.TrimPrefix(parts[i+1], "/")
			target, _ = strconv.ParseFloat(targetStr, 64)
			i++ // skip the target part
		}

		switch key {
		case "T", "T0":
			result["t0Temp"] = current
			result["t0Target"] = target
		case "T1":
			result["t1Temp"] = current
			result["t1Target"] = target
		case "B":
			result["heatbedTemp"] = current
			result["heatbedTarget"] = target
		}
	}

	// Default status to IDLE if not set.
	if _, ok := result["status"]; !ok {
		result["status"] = "IDLE"
	}
}

// parseM114 parses an M114 position response into the status map.
// Typical M114 response: "X:100.00 Y:200.00 Z:10.00 E:0.00 Count X:100.00 Y:200.00 Z:10.00"
func parseM114(resp string, result map[string]interface{}) {
	resp = strings.TrimPrefix(resp, "ok ")
	resp = strings.TrimPrefix(resp, "ok")
	resp = strings.TrimSpace(resp)

	// Only parse the first part (before "Count" if present).
	if idx := strings.Index(resp, "Count"); idx > 0 {
		resp = resp[:idx]
	}

	parts := strings.Fields(resp)
	for _, part := range parts {
		if !strings.Contains(part, ":") {
			continue
		}
		kv := strings.SplitN(part, ":", 2)
		val, err := strconv.ParseFloat(kv[1], 64)
		if err != nil {
			continue
		}

		switch kv[0] {
		case "X":
			result["x"] = val
		case "Y":
			result["y"] = val
		case "Z":
			result["z"] = val
		}
	}
}
