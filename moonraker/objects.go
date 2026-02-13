package moonraker

import (
	"github.com/john/snapmaker_moonraker/printer"
)

// PrinterObjects builds the Klipper-compatible printer object tree from state.
// These objects mimic what Moonraker exposes from Klipper, enabling
// Mainsail/Fluidd to render printer status.
type PrinterObjects struct{}

// BuildAll returns all printer objects for a full query.
func (po *PrinterObjects) BuildAll(state printer.StateData) map[string]interface{} {
	return map[string]interface{}{
		"toolhead":       po.Toolhead(state),
		"extruder":       po.Extruder(state, 0),
		"extruder1":      po.Extruder(state, 1),
		"heater_bed":     po.HeaterBed(state),
		"gcode_move":     po.GCodeMove(state),
		"print_stats":    po.PrintStats(state),
		"virtual_sdcard": po.VirtualSDCard(state),
		"webhooks":       po.Webhooks(state),
		"fan":            po.Fan(state),
		"heaters":        po.Heaters(state),
		"display_status": po.DisplayStatus(state),
	}
}

// Query returns only the requested objects/fields.
func (po *PrinterObjects) Query(state printer.StateData, objects map[string]interface{}) map[string]interface{} {
	all := po.BuildAll(state)
	result := make(map[string]interface{})

	for name, requestedFields := range objects {
		obj, ok := all[name]
		if !ok {
			continue
		}

		objMap, ok := obj.(map[string]interface{})
		if !ok {
			result[name] = obj
			continue
		}

		// If requested fields is nil or empty, return all fields.
		if requestedFields == nil {
			result[name] = objMap
			continue
		}

		if fieldList, ok := requestedFields.([]interface{}); ok && len(fieldList) > 0 {
			filtered := make(map[string]interface{})
			for _, f := range fieldList {
				if fieldName, ok := f.(string); ok {
					if val, exists := objMap[fieldName]; exists {
						filtered[fieldName] = val
					}
				}
			}
			result[name] = filtered
		} else {
			result[name] = objMap
		}
	}

	return result
}

// AvailableObjects returns the list of available object names.
func (po *PrinterObjects) AvailableObjects() []string {
	return []string{
		"toolhead",
		"extruder",
		"extruder1",
		"heater_bed",
		"gcode_move",
		"print_stats",
		"virtual_sdcard",
		"webhooks",
		"fan",
		"heaters",
		"display_status",
	}
}

func (po *PrinterObjects) Toolhead(state printer.StateData) map[string]interface{} {
	return map[string]interface{}{
		"position":             []float64{state.X, state.Y, state.Z, 0},
		"homed_axes":           state.HomedAxes,
		"print_time":           state.PrintDuration,
		"estimated_print_time": state.PrintDuration,
		"max_velocity":         300.0,
		"max_accel":            3000.0,
		"max_velocity_x":       300.0,
		"max_velocity_y":       300.0,
		"max_velocity_z":       40.0,
		"axis_minimum":         []float64{0, 0, 0, 0},
		"axis_maximum":         []float64{325, 325, 340, 0},
		"stalls":               0,
		"extruder":             "extruder",
	}
}

func (po *PrinterObjects) Extruder(state printer.StateData, index int) map[string]interface{} {
	temp := state.Extruder0Temp
	target := state.Extruder0Target
	if index == 1 {
		temp = state.Extruder1Temp
		target = state.Extruder1Target
	}

	power := 0.0
	if target > 0 && temp < target {
		power = 1.0
	}

	return map[string]interface{}{
		"temperature":      temp,
		"target":           target,
		"power":            power,
		"pressure_advance": 0.0,
		"smooth_time":      0.04,
		"can_extrude":      temp > 170,
	}
}

func (po *PrinterObjects) HeaterBed(state printer.StateData) map[string]interface{} {
	power := 0.0
	if state.BedTarget > 0 && state.BedTemp < state.BedTarget {
		power = 1.0
	}

	return map[string]interface{}{
		"temperature": state.BedTemp,
		"target":      state.BedTarget,
		"power":       power,
	}
}

func (po *PrinterObjects) GCodeMove(state printer.StateData) map[string]interface{} {
	return map[string]interface{}{
		"speed_factor":         state.SpeedFactor,
		"extrude_factor":       state.ExtrudeFactor,
		"absolute_coordinates": true,
		"absolute_extrude":     true,
		"homing_origin":        []float64{0, 0, 0, 0},
		"position":             []float64{state.X, state.Y, state.Z, 0},
		"gcode_position":       []float64{state.X, state.Y, state.Z, 0},
		"speed":                state.RequestedSpeed,
	}
}

func (po *PrinterObjects) PrintStats(state printer.StateData) map[string]interface{} {
	s := "standby"
	switch state.PrinterState {
	case "printing":
		s = "printing"
	case "paused":
		s = "paused"
	case "error":
		s = "error"
	case "idle":
		s = "standby"
	}

	return map[string]interface{}{
		"state":          s,
		"print_duration": state.PrintDuration,
		"total_duration": state.PrintDuration,
		"filament_used":  0.0,
		"filename":       state.PrintFileName,
		"message":        "",
		"info": map[string]interface{}{
			"total_layer":   nil,
			"current_layer": nil,
		},
	}
}

func (po *PrinterObjects) VirtualSDCard(state printer.StateData) map[string]interface{} {
	isActive := state.PrinterState == "printing" || state.PrinterState == "paused"
	return map[string]interface{}{
		"file_path":     state.PrintFileName,
		"progress":      state.PrintProgress,
		"is_active":     isActive,
		"file_position": 0,
		"file_size":     0,
	}
}

func (po *PrinterObjects) Webhooks(state printer.StateData) map[string]interface{} {
	// Always report ready - the bridge is the "Klipper" from Mainsail's perspective.
	return map[string]interface{}{
		"state":         "ready",
		"state_message": "",
	}
}

func (po *PrinterObjects) Fan(state printer.StateData) map[string]interface{} {
	return map[string]interface{}{
		"speed": state.FanSpeed,
		"rpm":   nil,
	}
}

func (po *PrinterObjects) Heaters(state printer.StateData) map[string]interface{} {
	// Return list of available heaters and sensors
	return map[string]interface{}{
		"available_heaters": []string{"heater_bed", "extruder", "extruder1"},
		"available_sensors": []string{"heater_bed", "extruder", "extruder1"},
	}
}

func (po *PrinterObjects) DisplayStatus(state printer.StateData) map[string]interface{} {
	progress := state.PrintProgress
	message := ""
	if state.PrinterState == "printing" && state.PrintFileName != "" {
		message = "Printing: " + state.PrintFileName
	}
	return map[string]interface{}{
		"progress": progress,
		"message":  message,
	}
}
