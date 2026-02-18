package moonraker

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// registerServerHandlers sets up /server/* and /machine/* routes.
func (s *Server) registerServerHandlers() {
	s.mux.HandleFunc("GET /server/info", s.handleServerInfo)
	s.mux.HandleFunc("GET /server/config", s.handleServerConfig)
	s.mux.HandleFunc("POST /server/restart", s.handleServerRestart)
	s.mux.HandleFunc("GET /server/temperature_store", s.handleTemperatureStore)
	s.mux.HandleFunc("GET /server/gcode_store", s.handleGCodeStore)
	s.mux.HandleFunc("GET /server/announcements/list", s.handleAnnouncementsList)
	s.mux.HandleFunc("GET /server/webcams/list", s.handleWebcamsList)
	s.mux.HandleFunc("GET /machine/system_info", s.handleMachineSystemInfo)
	s.mux.HandleFunc("GET /machine/proc_stats", s.handleMachineProcStats)
	s.mux.HandleFunc("GET /machine/services/list", s.handleMachineServicesList)
	s.mux.HandleFunc("POST /machine/services/restart", s.handleMachineServiceRestart)
	s.mux.HandleFunc("POST /machine/services/stop", s.handleMachineServiceStop)
	s.mux.HandleFunc("POST /machine/services/start", s.handleMachineServiceStart)
	s.mux.HandleFunc("GET /machine/update/status", s.handleMachineUpdateStatus)
}

func (s *Server) handleServerInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"result": s.serverInfo(),
	})
}

func (s *Server) serverInfo() map[string]interface{} {
	// Always report as ready - this bridge IS the "Klipper" from Mainsail's perspective.
	// Printer connectivity is reflected in webhooks state and print_stats, not here.
	return map[string]interface{}{
		"klippy_connected":    true,
		"klippy_state":        "ready",
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
	components := []string{
		"server",
		"file_manager",
		"klippy_apis",
		"machine",
		"data_store",
		"database",
		"history",
		"octoprint_compat",
		"webcam",
	}
	if s.spoolman != nil {
		components = append(components, "spoolman")
	}
	return components
}

func (s *Server) handleServerConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"result": s.serverConfig(),
	})
}

func (s *Server) serverConfig() map[string]interface{} {
	cfg := map[string]interface{}{
		"config": map[string]interface{}{
			"server": map[string]interface{}{
				"host":               s.config.Server.Host,
				"port":               s.config.Server.Port,
				"klippy_uds_address": "/tmp/klippy_uds",
			},
		},
	}
	if s.spoolman != nil {
		cfg["config"].(map[string]interface{})["spoolman"] = map[string]interface{}{
			"server": s.config.Spoolman.Server,
		}
	}
	return cfg
}

func (s *Server) handleServerRestart(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"result": "ok",
	})
}

func (s *Server) handleTemperatureStore(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"result": s.temperatureStore(),
	})
}

func (s *Server) temperatureStore() map[string]interface{} {
	// Return empty temperature history for each sensor.
	// Mainsail expects arrays of temperature readings keyed by sensor name.
	emptyStore := func() map[string]interface{} {
		return map[string]interface{}{
			"temperatures": []float64{},
			"targets":      []float64{},
			"powers":       []float64{},
		}
	}
	return map[string]interface{}{
		"extruder":   emptyStore(),
		"extruder1":  emptyStore(),
		"heater_bed": emptyStore(),
	}
}

func (s *Server) handleGCodeStore(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"result": s.gcodeStore(),
	})
}

func (s *Server) gcodeStore() map[string]interface{} {
	return map[string]interface{}{
		"gcode_store": []interface{}{},
	}
}

func (s *Server) handleAnnouncementsList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"result": map[string]interface{}{
			"entries": []interface{}{},
			"feeds":   []interface{}{},
		},
	})
}

func (s *Server) handleMachineSystemInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"result": s.machineSystemInfo(),
	})
}

func (s *Server) machineSystemInfo() map[string]interface{} {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	return map[string]interface{}{
		"system_info": map[string]interface{}{
			"cpu_info": map[string]interface{}{
				"cpu_count":    runtime.NumCPU(),
				"bits":         "32bit",
				"processor":    "armv7l",
				"cpu_desc":     "Snapmaker Moonraker Bridge",
				"serial_number": "",
				"hardware":     "",
				"model":        "Raspberry Pi 3",
				"total_memory": memStats.Sys,
				"memory_units": "B",
			},
			"sd_info":      map[string]interface{}{},
			"distribution": map[string]interface{}{
				"name":       "Raspbian GNU/Linux",
				"id":         "raspbian",
				"version":    "12",
				"version_parts": map[string]interface{}{
					"major": "12",
					"minor": "",
					"build_number": "",
				},
				"like":       "debian",
				"codename":   "bookworm",
			},
			"virtualization": map[string]interface{}{
				"virt_type":       "none",
				"virt_identifier": "none",
			},
			"network":            map[string]interface{}{},
			"canbus":             map[string]interface{}{},
			"python":             map[string]interface{}{
				"version": []int{0, 0, 0},
			},
			"available_services": allowedServices,
			"service_state":      getServiceStates(),
		},
	}
}

// getServiceStates queries systemd for the active/sub state of each allowed service.
func getServiceStates() map[string]interface{} {
	states := make(map[string]interface{})
	for _, svc := range allowedServices {
		active := "inactive"
		sub := "dead"
		if out, err := exec.Command("systemctl", "show", "-p", "ActiveState,SubState", "--value", svc).Output(); err == nil {
			lines := strings.Split(strings.TrimSpace(string(out)), "\n")
			if len(lines) >= 2 {
				active = strings.TrimSpace(lines[0])
				sub = strings.TrimSpace(lines[1])
			}
		}
		states[svc] = map[string]interface{}{
			"active_state": active,
			"sub_state":    sub,
		}
	}
	return states
}

func (s *Server) handleMachineProcStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"result": s.machineProcStats(),
	})
}

func (s *Server) machineProcStats() map[string]interface{} {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	return map[string]interface{}{
		"moonraker_stats": []map[string]interface{}{
			{
				"time":       float64(time.Now().Unix()),
				"cpu_usage":  0.0,
				"memory":     memStats.Alloc / 1024, // KB
				"mem_units":  "kB",
			},
		},
		"throttled_state": map[string]interface{}{
			"bits":  0,
			"flags": []string{},
		},
		"cpu_temp":        0.0,
		"system_cpu_usage": map[string]interface{}{
			"cpu":  0.0,
			"cpu0": 0.0,
		},
		"system_memory": map[string]interface{}{
			"total":     memStats.Sys / 1024,
			"available": (memStats.Sys - memStats.Alloc) / 1024,
			"used":      memStats.Alloc / 1024,
		},
		"websocket_connections": len(s.wsHub.clients),
	}
}

func (s *Server) handleWebcamsList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"result": s.getWebcamsList(),
	})
}

func (s *Server) getWebcamsList() map[string]interface{} {
	return map[string]interface{}{
		"webcams": []map[string]interface{}{
			{
				"name":         "Default",
				"location":     "printer",
				"service":      "mjpegstreamer-adaptive",
				"enabled":      true,
				"icon":         "mdiWebcam",
				"target_fps":   15,
				"target_fps_idle": 5,
				"stream_url":   "/webcam/?action=stream",
				"snapshot_url": "/webcam/?action=snapshot",
				"flip_horizontal": false,
				"flip_vertical":   false,
				"rotation":     0,
				"aspect_ratio": "4:3",
				"source":       "config",
				"uid":          "default-webcam",
			},
		},
	}
}

// allowedServices defines services that can be controlled from Mainsail's
// power menu, mimicking Moonraker's moonraker.asvc file.
var allowedServices = []string{
	"crowsnest",
	"moonraker-obico",
}

func (s *Server) handleMachineServicesList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"result": s.machineServicesList(),
	})
}

func (s *Server) machineServicesList() map[string]interface{} {
	return map[string]interface{}{
		"services": allowedServices,
	}
}

func (s *Server) handleMachineServiceRestart(w http.ResponseWriter, r *http.Request) {
	service := r.URL.Query().Get("service")
	if err := machineServiceAction("restart", service); err != nil {
		writeJSON(w, map[string]interface{}{
			"error": map[string]interface{}{
				"code":    400,
				"message": err.Error(),
			},
		})
		return
	}
	writeJSON(w, map[string]interface{}{"result": "ok"})
}

func (s *Server) handleMachineServiceStop(w http.ResponseWriter, r *http.Request) {
	service := r.URL.Query().Get("service")
	if err := machineServiceAction("stop", service); err != nil {
		writeJSON(w, map[string]interface{}{
			"error": map[string]interface{}{
				"code":    400,
				"message": err.Error(),
			},
		})
		return
	}
	writeJSON(w, map[string]interface{}{"result": "ok"})
}

func (s *Server) handleMachineServiceStart(w http.ResponseWriter, r *http.Request) {
	service := r.URL.Query().Get("service")
	if err := machineServiceAction("start", service); err != nil {
		writeJSON(w, map[string]interface{}{
			"error": map[string]interface{}{
				"code":    400,
				"message": err.Error(),
			},
		})
		return
	}
	writeJSON(w, map[string]interface{}{"result": "ok"})
}

// machineServiceAction executes a systemctl action on an allowed service.
func machineServiceAction(action, service string) error {
	if service == "" {
		return fmt.Errorf("missing service parameter")
	}

	allowed := false
	for _, s := range allowedServices {
		if s == service {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Errorf("service %q is not allowed", service)
	}

	log.Printf("Service %s: %s", action, service)
	cmd := exec.Command("sudo", "systemctl", action, service)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl %s %s failed: %s (%w)", action, service, strings.TrimSpace(string(out)), err)
	}
	return nil
}

func (s *Server) handleMachineUpdateStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"result": map[string]interface{}{
			"busy":         false,
			"github_rate_limit":  nil,
			"github_requests_remaining": nil,
			"github_limit_reset_time":   nil,
			"version_info": map[string]interface{}{},
		},
	})
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
