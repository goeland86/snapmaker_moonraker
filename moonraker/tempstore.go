package moonraker

import (
	"sync"

	"github.com/john/snapmaker_moonraker/printer"
)

// TempStore records temperature history in a ring buffer for the
// Moonraker temperature_store API. Mainsail uses this to populate
// temperature graphs, especially when switching tabs.
type TempStore struct {
	mu   sync.RWMutex
	size int
	// Per-sensor ring buffers.
	sensors map[string]*sensorStore
}

type sensorStore struct {
	temperatures []float64
	targets      []float64
	powers       []float64
	pos          int  // next write position
	full         bool // ring has wrapped
}

// NewTempStore creates a store that holds the last n readings per sensor.
func NewTempStore(n int) *TempStore {
	ts := &TempStore{
		size:    n,
		sensors: make(map[string]*sensorStore),
	}
	for _, name := range []string{"extruder", "extruder1", "heater_bed"} {
		ts.sensors[name] = &sensorStore{
			temperatures: make([]float64, n),
			targets:      make([]float64, n),
			powers:       make([]float64, n),
		}
	}
	return ts
}

// Record adds a temperature reading from the current printer state.
func (ts *TempStore) Record(state printer.StateData) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	ts.record("extruder", state.Extruder0Temp, state.Extruder0Target)
	ts.record("extruder1", state.Extruder1Temp, state.Extruder1Target)
	ts.record("heater_bed", state.BedTemp, state.BedTarget)
}

func (ts *TempStore) record(name string, temp, target float64) {
	s := ts.sensors[name]
	if s == nil {
		return
	}
	power := 0.0
	if target > 0 && temp < target {
		power = 1.0
	}
	s.temperatures[s.pos] = temp
	s.targets[s.pos] = target
	s.powers[s.pos] = power
	s.pos++
	if s.pos >= len(s.temperatures) {
		s.pos = 0
		s.full = true
	}
}

// Snapshot returns the Moonraker-format temperature store.
func (ts *TempStore) Snapshot() map[string]interface{} {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	result := make(map[string]interface{}, len(ts.sensors))
	for name, s := range ts.sensors {
		result[name] = map[string]interface{}{
			"temperatures": ts.ordered(s.temperatures, s.pos, s.full),
			"targets":      ts.ordered(s.targets, s.pos, s.full),
			"powers":       ts.ordered(s.powers, s.pos, s.full),
		}
	}
	return result
}

// ordered returns ring buffer contents in chronological order.
func (ts *TempStore) ordered(buf []float64, pos int, full bool) []float64 {
	if !full {
		// Haven't wrapped yet — just return what we have.
		out := make([]float64, pos)
		copy(out, buf[:pos])
		return out
	}
	// Wrapped — oldest data starts at pos.
	out := make([]float64, len(buf))
	copy(out, buf[pos:])
	copy(out[len(buf)-pos:], buf[:pos])
	return out
}
