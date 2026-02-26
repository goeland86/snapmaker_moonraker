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
	dbKey       = "spoolman.spool_id"
)

// Manager handles communication with a Spoolman server for filament spool management.
type Manager struct {
	mu            sync.RWMutex
	serverURL     string
	httpClient    *http.Client
	db            *database.Database
	activeSpoolID int
	connected     bool

	// Health check
	stopHealth chan struct{}

	// Callbacks for WebSocket notifications
	onSpoolSet     func(int)
	onStatusChange func(bool)

	// Phase 2: filament usage tracking
	trackingMu          sync.Mutex
	filamentByLine      []float64 // cumulative mm indexed by line number
	totalFilamentMM     float64
	lastReportedUsageMM float64
	trackingActive      bool
}

// NewManager creates a new Spoolman manager.
func NewManager(serverURL string, db *database.Database, onSpoolSet func(int), onStatusChange func(bool)) *Manager {
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

	// Restore persisted spool ID from database.
	if val, ok := db.GetItem(dbNamespace, dbKey); ok {
		switch v := val.(type) {
		case float64:
			m.activeSpoolID = int(v)
		case int:
			m.activeSpoolID = v
		}
		if m.activeSpoolID > 0 {
			log.Printf("Spoolman: restored active spool ID %d from database", m.activeSpoolID)
		}
	}

	return m
}

// GetSpoolID returns the currently active spool ID (0 = none).
func (m *Manager) GetSpoolID() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.activeSpoolID
}

// SetSpoolID sets the active spool ID, persists it to the database, and fires the callback.
func (m *Manager) SetSpoolID(id int) error {
	m.mu.Lock()
	m.activeSpoolID = id
	m.mu.Unlock()

	// Persist to database.
	if err := m.db.SetItem(dbNamespace, dbKey, id); err != nil {
		return fmt.Errorf("persisting spool ID: %w", err)
	}

	log.Printf("Spoolman: active spool set to %d", id)

	if m.onSpoolSet != nil {
		m.onSpoolSet(id)
	}

	return nil
}

// Status returns the current Spoolman status.
func (m *Manager) Status() map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return map[string]interface{}{
		"spoolman_connected": m.connected,
		"pending_reports":    []interface{}{},
		"spool_id":           m.activeSpoolID,
	}
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

// --- Phase 2: Filament Usage Tracking ---

// StartTracking begins tracking filament usage for a bridge-started print.
// filamentByLine is a slice of cumulative mm indexed by gcode line number.
func (m *Manager) StartTracking(filamentByLine []float64) {
	m.trackingMu.Lock()
	defer m.trackingMu.Unlock()

	if m.GetSpoolID() == 0 || len(filamentByLine) == 0 {
		return
	}

	m.filamentByLine = filamentByLine
	m.totalFilamentMM = filamentByLine[len(filamentByLine)-1]
	m.lastReportedUsageMM = 0
	m.trackingActive = true
	log.Printf("Spoolman: tracking filament usage, total=%.1fmm (%d lines) on spool %d",
		m.totalFilamentMM, len(filamentByLine), m.GetSpoolID())
}

// ReportUsage reports filament usage based on the current gcode line number.
// Called periodically from the state poller during printing.
func (m *Manager) ReportUsage(currentLine int) {
	m.trackingMu.Lock()
	defer m.trackingMu.Unlock()

	if !m.trackingActive || len(m.filamentByLine) == 0 {
		return
	}

	spoolID := m.GetSpoolID()
	if spoolID == 0 {
		return
	}

	// Clamp line to valid range.
	idx := currentLine
	if idx < 0 {
		idx = 0
	}
	if idx >= len(m.filamentByLine) {
		idx = len(m.filamentByLine) - 1
	}

	usedMM := m.filamentByLine[idx]
	deltaMM := usedMM - m.lastReportedUsageMM

	// Only report if delta is meaningful (> 0.1mm).
	if deltaMM < 0.1 {
		return
	}

	// Send usage to Spoolman.
	url := fmt.Sprintf("%s/api/v1/spool/%d/use", m.serverURL, spoolID)
	payload := fmt.Sprintf(`{"use_length": %.2f}`, deltaMM)

	req, err := http.NewRequest("PUT", url, strings.NewReader(payload))
	if err != nil {
		log.Printf("Spoolman: error creating usage request: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		log.Printf("Spoolman: error reporting usage: %v", err)
		return
	}
	resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		m.lastReportedUsageMM = usedMM
	} else {
		log.Printf("Spoolman: usage report returned status %d", resp.StatusCode)
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

	// Send final delta if any remains.
	spoolID := m.GetSpoolID()
	if spoolID == 0 {
		m.filamentByLine = nil
		return
	}

	deltaMM := m.totalFilamentMM - m.lastReportedUsageMM
	if deltaMM < 0.1 {
		log.Printf("Spoolman: tracking stopped, all usage reported")
		return
	}

	url := fmt.Sprintf("%s/api/v1/spool/%d/use", m.serverURL, spoolID)
	payload := fmt.Sprintf(`{"use_length": %.2f}`, deltaMM)

	req, err := http.NewRequest("PUT", url, strings.NewReader(payload))
	if err != nil {
		log.Printf("Spoolman: error creating final usage request: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		log.Printf("Spoolman: error reporting final usage: %v", err)
		return
	}
	resp.Body.Close()

	log.Printf("Spoolman: tracking stopped, final %.1fmm reported to spool %d", deltaMM, spoolID)
	m.filamentByLine = nil
}

// IsTracking returns whether filament tracking is active.
func (m *Manager) IsTracking() bool {
	m.trackingMu.Lock()
	defer m.trackingMu.Unlock()
	return m.trackingActive
}
