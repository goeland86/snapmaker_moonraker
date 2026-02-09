package history

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// JobStatus represents the state of a print job.
type JobStatus string

const (
	StatusInProgress  JobStatus = "in_progress"
	StatusCompleted   JobStatus = "completed"
	StatusCancelled   JobStatus = "cancelled"
	StatusError       JobStatus = "error"
	StatusKlippyError JobStatus = "klippy_shutdown"
)

// Job represents a print job in history.
type Job struct {
	JobID         string    `json:"job_id"`
	Filename      string    `json:"filename"`
	Status        JobStatus `json:"status"`
	StartTime     float64   `json:"start_time"`     // Unix timestamp
	EndTime       float64   `json:"end_time"`       // Unix timestamp
	PrintDuration float64   `json:"print_duration"` // seconds
	TotalDuration float64   `json:"total_duration"` // seconds (includes pauses)
	FilamentUsed  float64   `json:"filament_used"`  // mm
	Metadata      JobMeta   `json:"metadata"`
}

// JobMeta contains metadata about the printed file.
type JobMeta struct {
	Size           int64   `json:"size"`
	Modified       float64 `json:"modified"`
	Slicer         string  `json:"slicer,omitempty"`
	SlicerVersion  string  `json:"slicer_version,omitempty"`
	EstimatedTime  float64 `json:"estimated_time,omitempty"`
	FilamentTotal  float64 `json:"filament_total,omitempty"`
	FirstLayerTemp float64 `json:"first_layer_extr_temp,omitempty"`
	FirstLayerBed  float64 `json:"first_layer_bed_temp,omitempty"`
}

// Totals represents cumulative statistics.
type Totals struct {
	TotalJobs       int     `json:"total_jobs"`
	TotalTime       float64 `json:"total_time"`
	TotalPrintTime  float64 `json:"total_print_time"`
	TotalFilament   float64 `json:"total_filament_used"`
	LongestJob      float64 `json:"longest_job"`
	LongestPrint    float64 `json:"longest_print"`
	CompletedJobs   int     `json:"completed_jobs"`
	CancelledJobs   int     `json:"cancelled_jobs"`
	FailedJobs      int     `json:"failed_jobs"`
}

// HistoryChangedAction is the action type for history change events.
type HistoryChangedAction string

const (
	ActionAdded   HistoryChangedAction = "added"
	ActionFinished HistoryChangedAction = "finished"
)

// HistoryChangedCallback is called when the history changes.
type HistoryChangedCallback func(action HistoryChangedAction, job *Job)

// Manager manages print job history.
type Manager struct {
	mu         sync.RWMutex
	jobs       []*Job
	dataPath   string
	nextJobID  int
	currentJob *Job
	callback   HistoryChangedCallback
}

// NewManager creates a new history manager.
func NewManager(dataDir string, callback HistoryChangedCallback) (*Manager, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("creating history directory: %w", err)
	}

	m := &Manager{
		dataPath:  filepath.Join(dataDir, "history.json"),
		jobs:      make([]*Job, 0),
		nextJobID: 1,
		callback:  callback,
	}

	if err := m.load(); err != nil {
		// Log but continue - empty history is fine
		fmt.Printf("Warning: failed to load history: %v\n", err)
	}

	return m, nil
}

// load reads history from disk.
func (m *Manager) load() error {
	data, err := os.ReadFile(m.dataPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var state struct {
		Jobs      []*Job `json:"jobs"`
		NextJobID int    `json:"next_job_id"`
	}

	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}

	m.jobs = state.Jobs
	m.nextJobID = state.NextJobID
	if m.nextJobID == 0 {
		m.nextJobID = len(m.jobs) + 1
	}

	return nil
}

// save writes history to disk.
func (m *Manager) save() error {
	state := struct {
		Jobs      []*Job `json:"jobs"`
		NextJobID int    `json:"next_job_id"`
	}{
		Jobs:      m.jobs,
		NextJobID: m.nextJobID,
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(m.dataPath, data, 0644)
}

// StartJob begins tracking a new print job.
func (m *Manager) StartJob(filename string, metadata JobMeta) *Job {
	m.mu.Lock()
	defer m.mu.Unlock()

	job := &Job{
		JobID:     fmt.Sprintf("%06X", m.nextJobID),
		Filename:  filename,
		Status:    StatusInProgress,
		StartTime: float64(time.Now().Unix()),
		Metadata:  metadata,
	}

	m.nextJobID++
	m.currentJob = job
	m.jobs = append(m.jobs, job)

	m.save()

	if m.callback != nil {
		m.callback(ActionAdded, job)
	}

	return job
}

// FinishJob completes the current job with the given status.
func (m *Manager) FinishJob(status JobStatus, printDuration, filamentUsed float64) *Job {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.currentJob == nil {
		return nil
	}

	job := m.currentJob
	job.Status = status
	job.EndTime = float64(time.Now().Unix())
	job.PrintDuration = printDuration
	job.TotalDuration = job.EndTime - job.StartTime
	job.FilamentUsed = filamentUsed

	m.currentJob = nil
	m.save()

	if m.callback != nil {
		m.callback(ActionFinished, job)
	}

	return job
}

// GetCurrentJob returns the job currently in progress, if any.
func (m *Manager) GetCurrentJob() *Job {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.currentJob
}

// ListJobs returns jobs with pagination and optional filtering.
// Jobs are returned in reverse chronological order (newest first).
func (m *Manager) ListJobs(start, limit int, before, since float64, order string) ([]*Job, int) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Filter jobs
	filtered := make([]*Job, 0, len(m.jobs))
	for _, job := range m.jobs {
		if before > 0 && job.StartTime >= before {
			continue
		}
		if since > 0 && job.StartTime < since {
			continue
		}
		filtered = append(filtered, job)
	}

	// Sort by start time
	if order == "asc" {
		sort.Slice(filtered, func(i, j int) bool {
			return filtered[i].StartTime < filtered[j].StartTime
		})
	} else {
		sort.Slice(filtered, func(i, j int) bool {
			return filtered[i].StartTime > filtered[j].StartTime
		})
	}

	total := len(filtered)

	// Apply pagination
	if start >= len(filtered) {
		return []*Job{}, total
	}
	filtered = filtered[start:]

	if limit > 0 && limit < len(filtered) {
		filtered = filtered[:limit]
	}

	return filtered, total
}

// GetJob retrieves a specific job by ID.
func (m *Manager) GetJob(jobID string) *Job {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, job := range m.jobs {
		if job.JobID == jobID {
			return job
		}
	}
	return nil
}

// DeleteJob removes a job from history.
func (m *Manager) DeleteJob(jobID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i, job := range m.jobs {
		if job.JobID == jobID {
			m.jobs = append(m.jobs[:i], m.jobs[i+1:]...)
			m.save()
			return true
		}
	}
	return false
}

// GetTotals calculates cumulative statistics.
func (m *Manager) GetTotals() Totals {
	m.mu.RLock()
	defer m.mu.RUnlock()

	totals := Totals{}

	for _, job := range m.jobs {
		if job.Status == StatusInProgress {
			continue
		}

		totals.TotalJobs++
		totals.TotalTime += job.TotalDuration
		totals.TotalPrintTime += job.PrintDuration
		totals.TotalFilament += job.FilamentUsed

		if job.TotalDuration > totals.LongestJob {
			totals.LongestJob = job.TotalDuration
		}
		if job.PrintDuration > totals.LongestPrint {
			totals.LongestPrint = job.PrintDuration
		}

		switch job.Status {
		case StatusCompleted:
			totals.CompletedJobs++
		case StatusCancelled:
			totals.CancelledJobs++
		case StatusError, StatusKlippyError:
			totals.FailedJobs++
		}
	}

	return totals
}

// ResetTotals clears all history.
func (m *Manager) ResetTotals() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.jobs = make([]*Job, 0)
	m.currentJob = nil
	m.save()
}
