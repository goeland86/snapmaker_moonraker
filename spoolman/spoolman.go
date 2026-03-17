package spoolman

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/john/snapmaker_moonraker/database"
)

const (
	dbNamespace = "moonraker"
	// Legacy single-spool key (migrated to per-tool keys on first startup).
	dbKeyLegacy = "spoolman.spool_id"
)

// dbKeyForTool returns the database key for a tool's spool ID.
func dbKeyForTool(tool int) string {
	return fmt.Sprintf("spoolman.spool_id.%d", tool)
}

// Manager handles communication with a Spoolman server for filament spool management.
type Manager struct {
	mu            sync.RWMutex
	serverURL     string
	httpClient    *http.Client
	db            *database.Database
	activeSpoolIDs [2]int // per-tool spool IDs (0 = none)
	connected     bool

	// Health check
	stopHealth chan struct{}

	// Callbacks for WebSocket notifications
	onSpoolSet     func(spoolID int, tool int)
	onStatusChange func(bool)

	// Filament usage tracking (per-tool)
	trackingMu          sync.Mutex
	filamentByLine      [2][]float64 // per-tool cumulative mm indexed by line number
	totalFilamentMM     [2]float64
	lastReportedUsageMM [2]float64
	trackingActive      bool
}

// NewManager creates a new Spoolman manager.
func NewManager(serverURL string, db *database.Database, onSpoolSet func(int, int), onStatusChange func(bool)) *Manager {
	// Normalize URL: strip trailing slash
	serverURL = strings.TrimRight(serverURL, "/")

	m := &Manager{
		serverURL:  serverURL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		db:         db,
		stopHealth: make(chan struct{}),
		onSpoolSet:     onSpoolSet,
		onStatusChange: onStatusChange,
	}

	// Migrate legacy single spool ID to tool 0 if needed.
	if _, ok := db.GetItem(dbNamespace, dbKeyForTool(0)); !ok {
		if val, ok := db.GetItem(dbNamespace, dbKeyLegacy); ok {
			var id int
			switch v := val.(type) {
			case float64:
				id = int(v)
			case int:
				id = v
			}
			if id > 0 {
				db.SetItem(dbNamespace, dbKeyForTool(0), id)
				log.Printf("Spoolman: migrated legacy spool_id %d to tool 0", id)
			}
		}
	}

	// Restore persisted spool IDs from database.
	for t := 0; t < 2; t++ {
		if val, ok := db.GetItem(dbNamespace, dbKeyForTool(t)); ok {
			switch v := val.(type) {
			case float64:
				m.activeSpoolIDs[t] = int(v)
			case int:
				m.activeSpoolIDs[t] = v
			}
			if m.activeSpoolIDs[t] > 0 {
				log.Printf("Spoolman: restored active spool ID %d for tool %d", m.activeSpoolIDs[t], t)
			}
		}
	}

	return m
}

// GetSpoolID returns the active spool ID for a tool (0 = none).
func (m *Manager) GetSpoolID(tool int) int {
	if tool < 0 || tool > 1 {
		return 0
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.activeSpoolIDs[tool]
}

// SetSpoolID sets the active spool ID for a tool, persists it, and fires the callback.
func (m *Manager) SetSpoolID(id int, tool int) error {
	if tool < 0 || tool > 1 {
		return fmt.Errorf("invalid tool index %d", tool)
	}

	m.mu.Lock()
	m.activeSpoolIDs[tool] = id
	m.mu.Unlock()

	// Persist to database.
	if err := m.db.SetItem(dbNamespace, dbKeyForTool(tool), id); err != nil {
		return fmt.Errorf("persisting spool ID: %w", err)
	}

	log.Printf("Spoolman: active spool for tool %d set to %d", tool, id)

	if m.onSpoolSet != nil {
		m.onSpoolSet(id, tool)
	}

	return nil
}

// HasAnySpool returns true if any tool has a spool assigned.
func (m *Manager) HasAnySpool() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.activeSpoolIDs[0] > 0 || m.activeSpoolIDs[1] > 0
}

// Status returns the current Spoolman status.
func (m *Manager) Status() map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Build tool_spool_map for multi-tool frontends (Mainsail).
	// Keys are string-encoded tool indices, values are spool IDs.
	toolSpoolMap := map[string]interface{}{}
	for t := 0; t < 2; t++ {
		if m.activeSpoolIDs[t] > 0 {
			toolSpoolMap[fmt.Sprintf("%d", t)] = m.activeSpoolIDs[t]
		} else {
			toolSpoolMap[fmt.Sprintf("%d", t)] = nil
		}
	}

	return map[string]interface{}{
		"spoolman_connected": m.connected,
		"pending_reports":    []interface{}{},
		"spool_id":           spoolIDOrNil(m.activeSpoolIDs[0]), // backward compat
		"tool_spool_map":     toolSpoolMap,
	}
}

// spoolIDOrNil converts a spool ID to nil if 0 (no spool), so JSON marshals as null.
func spoolIDOrNil(id int) interface{} {
	if id == 0 {
		return nil
	}
	return id
}

// Proxy forwards a request to the Spoolman server and returns the response.
func (m *Manager) Proxy(method, path, query string, body io.Reader) (int, interface{}, error) {
	url := m.serverURL + "/api" + path
	if query != "" {
		url += "?" + query
	}

	req, err := http.NewRequest(strings.ToUpper(method), url, body)
	if err != nil {
		return 0, nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("spoolman request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("reading response: %w", err)
	}

	var result interface{}
	if len(respBody) > 0 {
		if err := json.Unmarshal(respBody, &result); err != nil {
			// Return as raw string if not valid JSON
			result = string(respBody)
		}
	}

	return resp.StatusCode, result, nil
}

// CheckConnection pings the Spoolman health endpoint.
func (m *Manager) CheckConnection() {
	url := m.serverURL + "/api/v1/health"
	resp, err := m.httpClient.Get(url)

	wasConnected := m.connected
	if err != nil || resp.StatusCode != http.StatusOK {
		m.mu.Lock()
		m.connected = false
		m.mu.Unlock()
		if wasConnected {
			log.Printf("Spoolman: connection lost to %s", m.serverURL)
			if m.onStatusChange != nil {
				m.onStatusChange(false)
			}
		}
		return
	}
	resp.Body.Close()

	m.mu.Lock()
	m.connected = true
	m.mu.Unlock()
	if !wasConnected {
		log.Printf("Spoolman: connected to %s", m.serverURL)
		if m.onStatusChange != nil {
			m.onStatusChange(true)
		}
	}
}

// StartHealthCheck begins periodic health checking in a background goroutine.
func (m *Manager) StartHealthCheck() {
	// Do an initial check immediately.
	m.CheckConnection()

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				m.CheckConnection()
			case <-m.stopHealth:
				return
			}
		}
	}()
}

// StopHealthCheck stops the periodic health checker.
func (m *Manager) StopHealthCheck() {
	close(m.stopHealth)
}

// --- Filament Usage Tracking (Per-Tool) ---

// StartTracking begins tracking filament usage for a bridge-started print.
// filamentByLine contains per-tool cumulative mm indexed by gcode line number.
func (m *Manager) StartTracking(filamentByLine [2][]float64) {
	m.trackingMu.Lock()
	defer m.trackingMu.Unlock()

	if !m.HasAnySpool() {
		return
	}

	// Only track tools that have both a spool and filament data.
	hasData := false
	for t := 0; t < 2; t++ {
		if m.GetSpoolID(t) > 0 && len(filamentByLine[t]) > 0 && filamentByLine[t][len(filamentByLine[t])-1] > 0 {
			hasData = true
		}
	}
	if !hasData {
		return
	}

	m.filamentByLine = filamentByLine
	for t := 0; t < 2; t++ {
		if len(filamentByLine[t]) > 0 {
			m.totalFilamentMM[t] = filamentByLine[t][len(filamentByLine[t])-1]
		} else {
			m.totalFilamentMM[t] = 0
		}
		m.lastReportedUsageMM[t] = 0
	}
	m.trackingActive = true

	log.Printf("Spoolman: tracking filament usage, T0=%.1fmm (spool %d), T1=%.1fmm (spool %d), %d lines",
		m.totalFilamentMM[0], m.GetSpoolID(0),
		m.totalFilamentMM[1], m.GetSpoolID(1),
		len(filamentByLine[0]))
}

// ReportUsage reports filament usage based on the current gcode line number.
// Called periodically from the state poller during printing.
func (m *Manager) ReportUsage(currentLine int) {
	m.trackingMu.Lock()
	defer m.trackingMu.Unlock()

	if !m.trackingActive {
		return
	}

	for t := 0; t < 2; t++ {
		if len(m.filamentByLine[t]) == 0 || m.GetSpoolID(t) == 0 {
			continue
		}

		// Clamp line to valid range.
		idx := currentLine
		if idx < 0 {
			idx = 0
		}
		if idx >= len(m.filamentByLine[t]) {
			idx = len(m.filamentByLine[t]) - 1
		}

		usedMM := m.filamentByLine[t][idx]
		deltaMM := usedMM - m.lastReportedUsageMM[t]

		// Only report if delta is meaningful (> 0.1mm).
		if deltaMM < 0.1 {
			continue
		}

		m.reportToSpool(m.GetSpoolID(t), deltaMM, t)
		m.lastReportedUsageMM[t] = usedMM
	}
}

// StopTracking stops filament usage tracking and sends a final report.
func (m *Manager) StopTracking() {
	m.trackingMu.Lock()
	defer m.trackingMu.Unlock()

	if !m.trackingActive {
		return
	}

	m.trackingActive = false

	for t := 0; t < 2; t++ {
		spoolID := m.GetSpoolID(t)
		if spoolID == 0 {
			continue
		}

		deltaMM := m.totalFilamentMM[t] - m.lastReportedUsageMM[t]
		if deltaMM >= 0.1 {
			m.reportToSpool(spoolID, deltaMM, t)
			log.Printf("Spoolman: tracking stopped, final %.1fmm reported to spool %d (T%d)", deltaMM, spoolID, t)
		}
	}

	m.filamentByLine = [2][]float64{}
	log.Printf("Spoolman: tracking stopped")
}

// reportToSpool sends a filament usage report to the Spoolman server for a specific spool.
func (m *Manager) reportToSpool(spoolID int, deltaMM float64, tool int) {
	url := fmt.Sprintf("%s/api/v1/spool/%d/use", m.serverURL, spoolID)
	payload := fmt.Sprintf(`{"use_length": %.2f}`, deltaMM)

	req, err := http.NewRequest("PUT", url, strings.NewReader(payload))
	if err != nil {
		log.Printf("Spoolman: error creating usage request for T%d: %v", tool, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		log.Printf("Spoolman: error reporting usage for T%d: %v", tool, err)
		return
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Spoolman: usage report for T%d returned status %d", tool, resp.StatusCode)
	}
}

// IsTracking returns whether filament tracking is active.
func (m *Manager) IsTracking() bool {
	m.trackingMu.Lock()
	defer m.trackingMu.Unlock()
	return m.trackingActive
}
