package printer

import (
	"log"
	"sync"
	"time"
)

// StatusCallback is called when printer status is updated.
type StatusCallback func(state *State)

// StateData holds printer state values without synchronization.
// Safe to copy by value.
type StateData struct {
	// Connection state
	Connected    bool   `json:"connected"`
	PrinterState string `json:"printer_state"` // "idle", "printing", "paused", "error"

	// Temperatures
	Extruder0Temp   float64 `json:"extruder0_temp"`
	Extruder0Target float64 `json:"extruder0_target"`
	Extruder1Temp   float64 `json:"extruder1_temp"`
	Extruder1Target float64 `json:"extruder1_target"`
	BedTemp         float64 `json:"bed_temp"`
	BedTarget       float64 `json:"bed_target"`

	// Position
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`

	// Print progress
	PrintProgress float64 `json:"print_progress"` // 0.0 - 1.0
	PrintFileName string  `json:"print_file_name"`
	PrintDuration float64 `json:"print_duration"` // seconds

	// Homing
	HomedAxes string `json:"homed_axes"` // e.g. "xyz"

	// Speed
	SpeedFactor    float64 `json:"speed_factor"`    // 1.0 = 100%
	ExtrudeFactor  float64 `json:"extrude_factor"`  // 1.0 = 100%
	RequestedSpeed float64 `json:"requested_speed"` // mm/s

	// Fan
	FanSpeed float64 `json:"fan_speed"` // 0.0 - 1.0

	// Raw status from printer HTTP API
	RawStatus map[string]interface{} `json:"-"`
}

// State provides thread-safe access to StateData.
type State struct {
	mu   sync.RWMutex
	data StateData
}

// NewState creates a default state.
func NewState() *State {
	return &State{
		data: StateData{
			PrinterState:  "idle",
			HomedAxes:     "",
			SpeedFactor:   1.0,
			ExtrudeFactor: 1.0,
		},
	}
}

// Snapshot returns a copy of the current state data.
func (s *State) Snapshot() StateData {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data
}

// StatePoller periodically polls the printer and updates state.
type StatePoller struct {
	client   *Client
	state    *State
	interval time.Duration
	stopCh   chan struct{}
	callback StatusCallback
}

// NewStatePoller creates a new poller.
func NewStatePoller(client *Client, state *State, intervalSec int, cb StatusCallback) *StatePoller {
	return &StatePoller{
		client:   client,
		state:    state,
		interval: time.Duration(intervalSec) * time.Second,
		stopCh:   make(chan struct{}),
		callback: cb,
	}
}

// Start begins polling in a goroutine.
func (sp *StatePoller) Start() {
	go sp.run()
}

// Stop halts the polling loop.
func (sp *StatePoller) Stop() {
	close(sp.stopCh)
}

func (sp *StatePoller) run() {
	ticker := time.NewTicker(sp.interval)
	defer ticker.Stop()

	// Initial poll
	sp.poll()

	for {
		select {
		case <-ticker.C:
			sp.poll()
		case <-sp.stopCh:
			return
		}
	}
}

func (sp *StatePoller) poll() {
	if !sp.client.Connected() {
		// Try to reconnect automatically.
		if sp.client.IP() != "" {
			if sp.client.Ping() {
				log.Printf("Printer reachable, attempting reconnect...")
				if err := sp.client.Connect(); err != nil {
					log.Printf("Reconnect failed: %v", err)
				} else {
					log.Printf("Reconnected to printer successfully")
				}
			}
		}

		// Still not connected after attempt - update state.
		if !sp.client.Connected() {
			sp.state.mu.Lock()
			sp.state.data.Connected = false
			sp.state.mu.Unlock()

			if sp.callback != nil {
				sp.callback(sp.state)
			}
			return
		}
	}

	// Trigger temperature queries (data arrives asynchronously via the router).
	sp.client.QueryTemperatures()

	// Small delay to let query responses arrive.
	time.Sleep(300 * time.Millisecond)

	status, err := sp.client.GetStatus()
	if err != nil {
		log.Printf("Status poll error: %v", err)
		return
	}

	sp.state.mu.Lock()
	sp.state.data.Connected = true
	sp.state.data.RawStatus = status
	sp.parseStatus(status)
	sp.state.mu.Unlock()

	if sp.callback != nil {
		sp.callback(sp.state)
	}
}

// parseStatus extracts known fields from the raw Snapmaker status response.
func (sp *StatePoller) parseStatus(status map[string]interface{}) {
	if v, ok := status["status"].(string); ok {
		switch v {
		case "IDLE":
			sp.state.data.PrinterState = "idle"
		case "RUNNING":
			sp.state.data.PrinterState = "printing"
		case "PAUSED":
			sp.state.data.PrinterState = "paused"
		default:
			sp.state.data.PrinterState = v
		}
	}

	sp.state.data.Extruder0Temp = floatFromMap(status, "t0Temp")
	sp.state.data.Extruder0Target = floatFromMap(status, "t0Target")
	sp.state.data.Extruder1Temp = floatFromMap(status, "t1Temp")
	sp.state.data.Extruder1Target = floatFromMap(status, "t1Target")
	sp.state.data.BedTemp = floatFromMap(status, "heatbedTemp", "bedTemp")
	sp.state.data.BedTarget = floatFromMap(status, "heatbedTarget", "bedTarget")

	sp.state.data.X = floatFromMap(status, "x")
	sp.state.data.Y = floatFromMap(status, "y")
	sp.state.data.Z = floatFromMap(status, "z")

	// Progress: always update so it resets to 0 when print completes.
	sp.state.data.PrintProgress = floatFromMap(status, "progress") / 100.0

	// Filename: update from HTTP response; clear when idle.
	if v, ok := status["fileName"].(string); ok {
		sp.state.data.PrintFileName = v
	} else if sp.state.data.PrinterState == "idle" {
		sp.state.data.PrintFileName = ""
	}

	// Duration: always update so it resets to 0 when print completes.
	sp.state.data.PrintDuration = floatFromMap(status, "elapsedTime", "printTime")

	// Fan speed (Snapmaker reports as percentage 0-100, convert to 0.0-1.0).
	// Always update so it resets to 0 when fan stops.
	sp.state.data.FanSpeed = floatFromMap(status, "fanSpeed", "fan") / 100.0
}

// floatFromMap tries multiple keys and returns the first float value found.
func floatFromMap(m map[string]interface{}, keys ...string) float64 {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch val := v.(type) {
			case float64:
				return val
			case int:
				return float64(val)
			case int64:
				return float64(val)
			}
		}
	}
	return 0
}
