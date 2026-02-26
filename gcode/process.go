package gcode

import (
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
)

// metadata holds extracted gcode metadata for header generation.
type metadata struct {
	nozzleTemp       [2]float64
	nozzleTempSet    [2]bool
	bedTemp          float64
	bedTempSet       bool
	minX, minY       float64
	minZ             float64
	maxX, maxY       float64
	maxZ             float64
	hasCoords        bool
	filamentMM       [2]float64  // per-tool filament extruded in mm
	layerHeight      float64
	estimatedTime    float64 // seconds
	toolsUsed        [2]bool
	filamentType     [2]string
	nozzleDiameter   [2]float64
	retraction       [2]float64
	switchRetraction [2]float64
	maxToolNum       int
	lastToolLine     [2]int // last line index where each (remapped) tool is active
}

// Process takes raw gcode data and a printer model string, and returns
// transformed gcode with a Snapmaker-compatible metadata header prepended
// and tool numbers remapped for dual-extruder compatibility.
//
// If the data already contains a ";Header Start" marker, it is returned
// unchanged (idempotency).
func Process(data []byte, printerModel string) []byte {
	content := string(data)

	// Idempotency: skip if already processed.
	if strings.Contains(content, ";Header Start") {
		log.Printf("gcode: header already present, skipping processing")
		return data
	}

	// Normalize line endings.
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	lines := strings.Split(content, "\n")

	// Pass 1: scan for metadata.
	meta := scanMetadata(lines)

	log.Printf("gcode: scanned %d lines — tools=%v maxTool=T%d temps=[%.0f,%.0f] bed=%.0f filament=[%.1f,%.1f]mm est=%.0fs",
		len(lines), meta.toolsUsed, meta.maxToolNum,
		meta.nozzleTemp[0], meta.nozzleTemp[1], meta.bedTemp,
		meta.filamentMM[0], meta.filamentMM[1], meta.estimatedTime)

	// Pass 2: transform lines (tool remap + nozzle shutoff).
	transformed := transformLines(lines, meta)

	// Build and prepend header.
	header := buildHeader(meta, printerModel, len(transformed))

	log.Printf("gcode: %s header prepended (%d bytes), output %d lines",
		headerVersion(printerModel), len(header), len(transformed))

	return []byte(header + strings.Join(transformed, "\n"))
}

// scanMetadata makes a single pass over all gcode lines to extract metadata.
func scanMetadata(lines []string) *metadata {
	meta := &metadata{
		minX:             math.MaxFloat64,
		minY:             math.MaxFloat64,
		minZ:             math.MaxFloat64,
		maxX:             -math.MaxFloat64,
		maxY:             -math.MaxFloat64,
		maxZ:             -math.MaxFloat64,
		filamentType:     [2]string{"PLA", "PLA"},
		nozzleDiameter:   [2]float64{0.4, 0.4},
		retraction:       [2]float64{0.8, 0.8},
		switchRetraction: [2]float64{0, 0},
		lastToolLine:     [2]int{-1, -1},
	}

	currentTool := 0
	relative := false
	var lastAbsE float64
	var prevZ float64
	zMoves := 0

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Pure comment line.
		if strings.HasPrefix(trimmed, ";") {
			scanComment(trimmed, meta)
			continue
		}

		// Split code and inline comment.
		codePart := trimmed
		if idx := strings.IndexByte(codePart, ';'); idx >= 0 {
			scanComment(codePart[idx:], meta)
			codePart = strings.TrimSpace(codePart[:idx])
		}
		if codePart == "" {
			continue
		}

		upper := strings.ToUpper(codePart)

		// Tool change (T0, T1, T2, ...).
		if len(upper) >= 2 && upper[0] == 'T' {
			if n, err := strconv.Atoi(upper[1:]); err == nil {
				currentTool = n
				if n > meta.maxToolNum {
					meta.maxToolNum = n
				}
				remapped := n % 2
				meta.toolsUsed[remapped] = true
				meta.lastToolLine[remapped] = i
				continue
			}
		}

		// Extrusion mode.
		switch upper {
		case "M82":
			relative = false
			continue
		case "M83":
			relative = true
			continue
		}

		// G92 — position reset (track E axis for absolute extrusion).
		if strings.HasPrefix(upper, "G92") {
			for _, f := range strings.Fields(codePart) {
				if len(f) >= 2 && (f[0] == 'E' || f[0] == 'e') {
					if v, err := strconv.ParseFloat(f[1:], 64); err == nil {
						lastAbsE = v
					}
				}
			}
		}

		// Temperature commands.
		if strings.HasPrefix(upper, "M104 ") || strings.HasPrefix(upper, "M109 ") {
			scanTempCommand(codePart, currentTool, meta, false)
		} else if strings.HasPrefix(upper, "M140 ") || strings.HasPrefix(upper, "M190 ") {
			scanTempCommand(codePart, currentTool, meta, true)
		}

		// G0/G1 move commands.
		if isG0G1(upper) {
			remapped := currentTool % 2
			for _, f := range strings.Fields(codePart)[1:] {
				if len(f) < 2 {
					continue
				}
				val, err := strconv.ParseFloat(f[1:], 64)
				if err != nil {
					continue
				}
				switch f[0] {
				case 'X', 'x':
					meta.hasCoords = true
					if val < meta.minX {
						meta.minX = val
					}
					if val > meta.maxX {
						meta.maxX = val
					}
				case 'Y', 'y':
					meta.hasCoords = true
					if val < meta.minY {
						meta.minY = val
					}
					if val > meta.maxY {
						meta.maxY = val
					}
				case 'Z', 'z':
					meta.hasCoords = true
					if val < meta.minZ {
						meta.minZ = val
					}
					if val > meta.maxZ {
						meta.maxZ = val
					}
					// Derive layer height from first Z delta (fallback).
					if meta.layerHeight == 0 && zMoves > 0 && val > prevZ {
						meta.layerHeight = val - prevZ
					}
					prevZ = val
					zMoves++
				case 'E', 'e':
					meta.lastToolLine[remapped] = i
					if relative {
						if val > 0 {
							meta.filamentMM[remapped] += val
						}
					} else {
						if val > lastAbsE {
							meta.filamentMM[remapped] += val - lastAbsE
						}
						lastAbsE = val
					}
				}
			}
		}
	}

	// Defaults for missing coordinate data.
	if !meta.hasCoords {
		meta.minX, meta.minY, meta.minZ = 0, 0, 0
		meta.maxX, meta.maxY, meta.maxZ = 0, 0, 0
	}

	// Mark tools as used based on filament extrusion (covers implicit T0).
	if meta.filamentMM[0] > 0 {
		meta.toolsUsed[0] = true
	}
	if meta.filamentMM[1] > 0 {
		meta.toolsUsed[1] = true
	}

	return meta
}

// isG0G1 returns true if the uppercased line is a G0 or G1 move command.
func isG0G1(upper string) bool {
	return strings.HasPrefix(upper, "G0 ") || strings.HasPrefix(upper, "G1 ") ||
		strings.HasPrefix(upper, "G0\t") || strings.HasPrefix(upper, "G1\t") ||
		upper == "G0" || upper == "G1"
}

// scanComment extracts metadata from a gcode comment.
func scanComment(comment string, meta *metadata) {
	s := strings.TrimLeft(comment, "; ")
	lower := strings.ToLower(s)

	// ;TIME:3600 (Cura/OrcaSlicer format).
	if strings.HasPrefix(lower, "time:") {
		if v, err := strconv.ParseFloat(strings.TrimSpace(s[5:]), 64); err == nil && meta.estimatedTime == 0 {
			meta.estimatedTime = v
		}
		return
	}

	// Key = value pairs.
	idx := strings.Index(s, "=")
	if idx < 0 {
		return
	}
	key := strings.TrimSpace(strings.ToLower(s[:idx]))
	val := strings.TrimSpace(s[idx+1:])

	switch key {
	case "layer_height":
		if v, err := strconv.ParseFloat(val, 64); err == nil && meta.layerHeight == 0 {
			meta.layerHeight = v
		}
	case "estimated printing time", "estimated printing time (normal mode)":
		if meta.estimatedTime == 0 {
			meta.estimatedTime = parseDuration(val)
		}
	case "filament_type":
		// May be semicolon-separated for multi-tool (e.g., "PLA;PETG").
		parts := strings.Split(val, ";")
		if t := strings.TrimSpace(parts[0]); t != "" {
			meta.filamentType[0] = t
		}
		if len(parts) > 1 {
			if t := strings.TrimSpace(parts[1]); t != "" {
				meta.filamentType[1] = t
			}
		}
	case "nozzle_diameter":
		// May be comma-separated for multi-tool.
		parts := strings.Split(val, ",")
		if v, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64); err == nil {
			meta.nozzleDiameter[0] = v
		}
		if len(parts) > 1 {
			if v, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64); err == nil {
				meta.nozzleDiameter[1] = v
			}
		}
	case "retract_length":
		if v, err := strconv.ParseFloat(strings.Split(val, ",")[0], 64); err == nil {
			meta.retraction[0] = v
			meta.retraction[1] = v
		}
	case "retract_length_toolchange":
		if v, err := strconv.ParseFloat(strings.Split(val, ",")[0], 64); err == nil {
			meta.switchRetraction[0] = v
			meta.switchRetraction[1] = v
		}
	}
}

// scanTempCommand extracts temperature values from M104/M109/M140/M190 commands.
func scanTempCommand(line string, currentTool int, meta *metadata, isBed bool) {
	fields := strings.Fields(line)
	sVal := 0.0
	tVal := currentTool

	for _, f := range fields[1:] {
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case 'S', 's':
			if v, err := strconv.ParseFloat(f[1:], 64); err == nil {
				sVal = v
			}
		case 'T', 't':
			if v, err := strconv.Atoi(f[1:]); err == nil {
				tVal = v
			}
		}
	}

	if isBed {
		if !meta.bedTempSet && sVal > 0 {
			meta.bedTemp = sVal
			meta.bedTempSet = true
		}
	} else {
		remapped := tVal % 2
		if !meta.nozzleTempSet[remapped] && sVal > 0 {
			meta.nozzleTemp[remapped] = sVal
			meta.nozzleTempSet[remapped] = true
		}
		if tVal > meta.maxToolNum {
			meta.maxToolNum = tVal
		}
	}
}

// transformLines processes gcode lines to remap tool numbers and insert
// nozzle shutoff commands for unused extruders.
func transformLines(lines []string, meta *metadata) []string {
	needRemap := meta.maxToolNum > 1
	result := make([]string, 0, len(lines)+10)
	currentTool := 0

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Split code and inline comment.
		codePart := trimmed
		commentPart := ""
		if idx := strings.IndexByte(trimmed, ';'); idx >= 0 {
			commentPart = trimmed[idx:]
			codePart = strings.TrimSpace(trimmed[:idx])
		}

		if codePart == "" {
			result = append(result, line)
			continue
		}

		upper := strings.ToUpper(codePart)

		// Tool change.
		if len(upper) >= 2 && upper[0] == 'T' {
			if n, err := strconv.Atoi(upper[1:]); err == nil {
				prevTool := currentTool % 2
				currentTool = n
				newTool := n % 2

				// Remap tool number if needed.
				if needRemap && n > 1 {
					out := fmt.Sprintf("T%d", newTool)
					if commentPart != "" {
						out += " " + commentPart
					}
					result = append(result, out)
				} else {
					result = append(result, line)
				}

				// Unused nozzle shutoff: if the previous tool won't be used
				// again after this point, turn off its heater.
				if prevTool != newTool && meta.lastToolLine[prevTool] >= 0 && meta.lastToolLine[prevTool] <= i {
					result = append(result, fmt.Sprintf("M104 S0 T%d ; shutoff unused nozzle", prevTool))
				}

				continue
			}
		}

		// Remap T param on M104/M109.
		if needRemap && (strings.HasPrefix(upper, "M104 ") || strings.HasPrefix(upper, "M109 ")) {
			result = append(result, remapParam(line, codePart, commentPart, 'T'))
			continue
		}

		// Remap P param on M106/M107.
		if needRemap && (strings.HasPrefix(upper, "M106 ") || strings.HasPrefix(upper, "M107 ")) {
			result = append(result, remapParam(line, codePart, commentPart, 'P'))
			continue
		}

		result = append(result, line)
	}

	return result
}

// remapParam rewrites a parameter (T or P) with values > 1 using mod 2.
func remapParam(original, codePart, commentPart string, param byte) string {
	fields := strings.Fields(codePart)
	changed := false
	upper := param
	lower := param + 32 // ASCII lowercase

	for i, f := range fields {
		if len(f) >= 2 && (f[0] == upper || f[0] == lower) {
			if n, err := strconv.Atoi(f[1:]); err == nil && n > 1 {
				fields[i] = fmt.Sprintf("%c%d", upper, n%2)
				changed = true
			}
		}
	}

	if !changed {
		return original
	}
	out := strings.Join(fields, " ")
	if commentPart != "" {
		out += " " + commentPart
	}
	return out
}

// isJ1Model returns true if the printer model is a Snapmaker J1 variant.
func isJ1Model(model string) bool {
	return strings.Contains(strings.ToLower(model), "j1")
}

// headerVersion returns a label for the header format being used.
func headerVersion(printerModel string) string {
	if isJ1Model(printerModel) {
		return "V1"
	}
	return "V0"
}

// buildHeader generates the appropriate Snapmaker header for the printer model.
func buildHeader(meta *metadata, printerModel string, totalLines int) string {
	if isJ1Model(printerModel) {
		return buildHeaderV1(meta, totalLines)
	}
	return buildHeaderV0(meta, printerModel)
}

// v1HeaderLines is the number of lines in a V1 header (without thumbnail).
const v1HeaderLines = 25

// buildHeaderV1 generates the Snapmaker V1 header format used by J1/J1S.
// This is the format the J1S HMI requires to index and display files.
func buildHeaderV1(meta *metadata, totalLines int) string {
	// Extruder mode.
	extruderMode := "Default"
	if meta.toolsUsed[0] && meta.toolsUsed[1] {
		extruderMode = "Default" // dual-extrusion default; IDEX modes not detectable from gcode alone
	}

	// Extruders used count: 1 or 2.
	extrudersUsed := 0
	if meta.toolsUsed[0] {
		extrudersUsed++
	}
	if meta.toolsUsed[1] {
		extrudersUsed++
	}
	if extrudersUsed == 0 {
		extrudersUsed = 1
	}

	var b strings.Builder
	b.WriteString(";Header Start\n")
	b.WriteString(";Version:1\n")
	b.WriteString(";Printer:Snapmaker J1\n")
	fmt.Fprintf(&b, ";Estimated Print Time:%d\n", int(meta.estimatedTime))
	fmt.Fprintf(&b, ";Lines:%d\n", totalLines+v1HeaderLines)
	fmt.Fprintf(&b, ";Extruder Mode:%s\n", extruderMode)

	// Per-extruder fields.
	for i := 0; i < 2; i++ {
		material := meta.filamentType[i]
		temp := meta.nozzleTemp[i]
		nozzle := meta.nozzleDiameter[i]
		retract := meta.retraction[i]
		switchRetract := meta.switchRetraction[i]

		// Unused extruder: clear material and temps (matches SMFix behavior).
		if !meta.toolsUsed[i] && (i == 1 || !meta.toolsUsed[0]) {
			material = "-"
			temp = 0
			retract = 0
			switchRetract = 0
		}

		fmt.Fprintf(&b, ";Extruder %d Nozzle Size:%.1f\n", i, nozzle)
		fmt.Fprintf(&b, ";Extruder %d Material:%s\n", i, material)
		fmt.Fprintf(&b, ";Extruder %d Print Temperature:%.0f\n", i, temp)
		fmt.Fprintf(&b, ";Extruder %d Retraction Distance:%.2f\n", i, retract)
		fmt.Fprintf(&b, ";Extruder %d Switch Retraction Distance:%.2f\n", i, switchRetract)
	}

	fmt.Fprintf(&b, ";Bed Temperature:%.0f\n", meta.bedTemp)
	fmt.Fprintf(&b, ";Work Range - Min X:%.4f\n", meta.minX)
	fmt.Fprintf(&b, ";Work Range - Min Y:%.4f\n", meta.minY)
	fmt.Fprintf(&b, ";Work Range - Min Z:%.4f\n", meta.minZ)
	fmt.Fprintf(&b, ";Work Range - Max X:%.4f\n", meta.maxX)
	fmt.Fprintf(&b, ";Work Range - Max Y:%.4f\n", meta.maxY)
	fmt.Fprintf(&b, ";Work Range - Max Z:%.4f\n", meta.maxZ)
	fmt.Fprintf(&b, ";Extruder(s) Used:%d\n", extrudersUsed)
	b.WriteString(";Header End\n")

	return b.String()
}

// buildHeaderV0 generates the Snapmaker V0 header format used by A150/A250/A350/A400/Artisan.
func buildHeaderV0(meta *metadata, printerModel string) string {
	// Tool head type.
	toolHead := "singleExtruderToolheadForSM2"
	if meta.toolsUsed[1] {
		toolHead = "dualExtruderToolheadForSM2"
	}

	// Machine name.
	machine := printerModel
	if machine == "" {
		machine = "Snapmaker"
	}

	// Total filament in meters.
	totalFilamentMM := meta.filamentMM[0] + meta.filamentMM[1]
	totalFilamentM := totalFilamentMM / 1000.0

	// Filament weight: volume (cm³) × density (g/cm³).
	radiusMM := 1.75 / 2.0
	volumeCM3 := totalFilamentMM * math.Pi * radiusMM * radiusMM / 1000.0
	weightG := volumeCM3 * 1.24 // PLA density g/cm³

	// Estimated time with 1.07× multiplier (matches SMFix V0).
	estTime := meta.estimatedTime * 1.07

	// Extruder bitmask: 1=T0 only, 2=T1 only, 3=both.
	extruderMask := 0
	if meta.toolsUsed[0] {
		extruderMask |= 1
	}
	if meta.toolsUsed[1] {
		extruderMask |= 2
	}
	if extruderMask == 0 {
		extruderMask = 1
	}

	layerHeight := meta.layerHeight
	if layerHeight == 0 {
		layerHeight = 0.20
	}

	var b strings.Builder
	b.WriteString(";Header Start\n")
	b.WriteString(";FAVOR:Marlin\n")
	fmt.Fprintf(&b, ";TIME:6666\n") // hardcoded dummy (matches SMFix)
	fmt.Fprintf(&b, ";Filament used: %.5fm\n", totalFilamentM)
	fmt.Fprintf(&b, ";Layer height: %.2f\n", layerHeight)
	b.WriteString(";header_type: 3dp\n")
	fmt.Fprintf(&b, ";tool_head: %s\n", toolHead)
	fmt.Fprintf(&b, ";machine: %s\n", machine)
	fmt.Fprintf(&b, ";estimated_time(s): %.0f\n", estTime)
	fmt.Fprintf(&b, ";nozzle_temperature(\u00b0C): %.0f\n", meta.nozzleTemp[0])
	fmt.Fprintf(&b, ";nozzle_0_diameter(mm): %.1f\n", meta.nozzleDiameter[0])
	fmt.Fprintf(&b, ";nozzle_0_material: %s\n", meta.filamentType[0])
	fmt.Fprintf(&b, ";nozzle_1_temperature(\u00b0C): %.0f\n", meta.nozzleTemp[1])
	fmt.Fprintf(&b, ";nozzle_1_diameter(mm): %.1f\n", meta.nozzleDiameter[1])
	fmt.Fprintf(&b, ";nozzle_1_material: %s\n", meta.filamentType[1])
	fmt.Fprintf(&b, ";build_plate_temperature(\u00b0C): %.0f\n", meta.bedTemp)
	fmt.Fprintf(&b, ";max_x(mm): %.4f\n", meta.maxX)
	fmt.Fprintf(&b, ";max_y(mm): %.4f\n", meta.maxY)
	fmt.Fprintf(&b, ";max_z(mm): %.4f\n", meta.maxZ)
	fmt.Fprintf(&b, ";min_x(mm): %.4f\n", meta.minX)
	fmt.Fprintf(&b, ";min_y(mm): %.4f\n", meta.minY)
	fmt.Fprintf(&b, ";min_z(mm): %.4f\n", meta.minZ)
	fmt.Fprintf(&b, ";Extruder(s) Used = %d\n", extruderMask)
	fmt.Fprintf(&b, ";matierial_weight: %.4f\n", weightG)     // deliberate typo matches firmware
	fmt.Fprintf(&b, ";matierial_length: %.5f\n", totalFilamentM) // deliberate typo matches firmware
	b.WriteString(";Header End\n")

	return b.String()
}

// parseDuration parses human-readable durations like "1h 30m 15s" to seconds.
func parseDuration(s string) float64 {
	s = strings.ReplaceAll(s, " ", "")

	// Try as plain number (seconds).
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		return v
	}

	total := 0.0
	for len(s) > 0 {
		// Find the numeric part.
		i := 0
		for i < len(s) && ((s[i] >= '0' && s[i] <= '9') || s[i] == '.') {
			i++
		}
		if i == 0 || i >= len(s) {
			break
		}
		val, err := strconv.ParseFloat(s[:i], 64)
		if err != nil {
			break
		}
		switch s[i] {
		case 'd', 'D':
			total += val * 86400
		case 'h', 'H':
			total += val * 3600
		case 'm', 'M':
			total += val * 60
		case 's', 'S':
			total += val
		}
		s = s[i+1:]
	}

	return total
}
