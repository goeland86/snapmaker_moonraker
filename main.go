package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/john/snapmaker_moonraker/database"
	"github.com/john/snapmaker_moonraker/files"
	"github.com/john/snapmaker_moonraker/history"
	"github.com/john/snapmaker_moonraker/moonraker"
	"github.com/john/snapmaker_moonraker/printer"
	"github.com/john/snapmaker_moonraker/spoolman"
)

// printState is persisted to disk so progress and Spoolman tracking
// can be restored if the bridge restarts during a print.
type printState struct {
	Filename   string `json:"filename"`
	TotalLines uint32 `json:"total_lines"`
}

func writePrintState(path string, ps printState) {
	data, err := json.Marshal(ps)
	if err != nil {
		log.Printf("Failed to marshal print state: %v", err)
		return
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		log.Printf("Failed to write print state: %v", err)
	}
}

func readPrintState(path string) (printState, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return printState{}, false
	}
	var ps printState
	if err := json.Unmarshal(data, &ps); err != nil {
		return printState{}, false
	}
	if ps.Filename == "" {
		return printState{}, false
	}
	return ps, true
}

func clearPrintState(path string) {
	os.Remove(path)
}

func main() {
	configPath := flag.String("config", "config.yaml", "path to configuration file")
	discover := flag.Bool("discover", false, "discover printers on the network and exit")
	flag.Parse()

	// Handle discovery mode.
	if *discover {
		runDiscovery()
		return
	}

	// Load configuration.
	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	log.Printf("Snapmaker Moonraker Bridge starting")
	log.Printf("Server: %s", cfg.ListenAddr())
	log.Printf("Printer: %s (%s)", cfg.Printer.IP, cfg.Printer.Model)

	// Resolve config directory (default: sibling of gcode dir).
	configDir := cfg.Files.ConfigDir
	if configDir == "" {
		configDir = filepath.Join(filepath.Dir(cfg.Files.GCodeDir), "config")
	}
	if !filepath.IsAbs(configDir) {
		dir, _ := os.Getwd()
		configDir = filepath.Join(dir, configDir)
	}

	// Initialize file manager.
	fm, err := files.NewManager(cfg.Files.GCodeDir, configDir)
	if err != nil {
		log.Fatalf("Failed to initialize file manager: %v", err)
	}
	log.Printf("GCode directory: %s", cfg.Files.GCodeDir)
	log.Printf("Config directory: %s", configDir)

	// Initialize database (for Obico and other integrations).
	dataDir := filepath.Join(filepath.Dir(cfg.Files.GCodeDir), ".moonraker_data")
	db, err := database.New(filepath.Join(dataDir, "database"))
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	log.Printf("Database directory: %s", filepath.Join(dataDir, "database"))

	// Print state file for restart recovery.
	printStatePath := filepath.Join(dataDir, "print_state.json")

	// Initialize history manager (will be connected to server hub after server creation).
	var historyMgr *history.Manager

	// Initialize printer client.
	pc := printer.NewClient(cfg.Printer.IP, cfg.Printer.Token, cfg.Printer.Model)

	// Initialize printer state.
	state := printer.NewState()

	// Build the moonraker server config.
	moonCfg := moonraker.Config{
		Server: moonraker.ServerConfig{
			Host: cfg.Server.Host,
			Port: cfg.Server.Port,
		},
	}
	moonCfg.Printer.IP = cfg.Printer.IP
	moonCfg.Printer.Token = cfg.Printer.Token
	moonCfg.Printer.Model = cfg.Printer.Model
	moonCfg.Files.GCodeDir = cfg.Files.GCodeDir

	// Initialize history manager with a placeholder callback (will be set after server creation).
	historyMgr, err = history.NewManager(filepath.Join(dataDir, "history"), nil)
	if err != nil {
		log.Fatalf("Failed to initialize history manager: %v", err)
	}
	log.Printf("History directory: %s", filepath.Join(dataDir, "history"))

	// Initialize Spoolman manager (nil if not configured).
	var spoolmanMgr *spoolman.Manager
	if cfg.Spoolman.Server != "" {
		spoolmanMgr = spoolman.NewManager(cfg.Spoolman.Server, db, nil, nil)
		moonCfg.Spoolman.Server = cfg.Spoolman.Server
		log.Printf("Spoolman: configured with server %s", cfg.Spoolman.Server)
	}

	// Create the Moonraker server.
	server := moonraker.NewServer(moonCfg, pc, state, fm, db, historyMgr, spoolmanMgr)

	// Start Spoolman health check and wire notification callbacks.
	if spoolmanMgr != nil {
		hub := server.Hub()
		spoolmanMgr = spoolman.NewManager(cfg.Spoolman.Server, db,
			func(spoolID int) {
				hub.BroadcastNotification("notify_active_spool_set", []interface{}{
					map[string]interface{}{"spool_id": spoolID},
				})
			},
			func(connected bool) {
				hub.BroadcastNotification("notify_spoolman_status_changed", []interface{}{
					map[string]interface{}{"spoolman_connected": connected},
				})
			},
		)
		// Re-set on the server since we recreated the manager with callbacks.
		server.SetSpoolman(spoolmanMgr)
		spoolmanMgr.StartHealthCheck()
	}

	// Connect to printer (non-fatal if it fails - we'll retry).
	if cfg.Printer.IP != "" {
		if err := pc.Connect(); err != nil {
			log.Printf("WARNING: Could not connect to printer: %v", err)
			log.Printf("Server will start anyway - printer commands will fail until connected")
		} else {
			// Notify WebSocket clients that printer is ready.
			server.Hub().BroadcastNotification("notify_klippy_ready", nil)
		}
	} else {
		log.Printf("WARNING: No printer IP configured - running in offline mode")
	}

	// Start state poller.
	var prevPrinterState string
	var printStateWritten bool  // track whether we've written the state file for this print
	var printStateRestored bool // avoid retrying file reads every poll cycle
	poller := printer.NewStatePoller(pc, state, cfg.Printer.PollInterval, func(s *printer.State) {
		snap := s.Snapshot()
		server.Hub().BroadcastStatusUpdate(s)

		// History tracking: record print start/finish.
		// Create a job when transitioning to printing, or when already printing
		// but no job exists yet (e.g., filename arrived late from SACP query).
		if snap.PrinterState == "printing" && snap.PrintFileName != "" && historyMgr.GetCurrentJob() == nil {
			historyMgr.StartJob(snap.PrintFileName, history.JobMeta{})
			server.Hub().BroadcastHistoryChanged("added", historyMgr.GetCurrentJob())
			log.Printf("History: started job for %s", snap.PrintFileName)
		}
		if prevPrinterState == "printing" && snap.PrinterState != "printing" && snap.PrinterState != "paused" {
			var status history.JobStatus
			switch snap.PrinterState {
			case "idle":
				status = history.StatusCompleted
			default:
				status = history.StatusCancelled
			}
			if job := historyMgr.FinishJob(status, snap.PrintDuration, 0); job != nil {
				server.Hub().BroadcastHistoryChanged("finished", job)
				log.Printf("History: finished job %s (%s)", job.Filename, job.Status)
			}
			clearPrintState(printStatePath)
			printStateWritten = false
			printStateRestored = false
		}

		// Print state persistence: restore totalLines from state file after
		// a restart, and write the state file when we have all the data.
		if snap.PrinterState == "printing" && snap.PrintFileName != "" {
			if pc.TotalLines() == 0 && !printStateRestored {
				// totalLines unknown — try to restore from state file or compute from file on disk.
				if ps, ok := readPrintState(printStatePath); ok && ps.Filename == snap.PrintFileName && ps.TotalLines > 0 {
					pc.SetTotalLines(ps.TotalLines)
					printStateRestored = true
					log.Printf("Restored totalLines=%d for %s from print state file", ps.TotalLines, ps.Filename)
				} else {
					// No state file or filename mismatch — try to count lines from the file on disk.
					gcodeDir := fm.GetRootPath("gcodes")
					fullPath := filepath.Join(gcodeDir, filepath.FromSlash(snap.PrintFileName))
					if data, err := os.ReadFile(fullPath); err == nil {
						lineCount := uint32(bytes.Count(data, []byte{'\n'}))
						if lineCount > 0 {
							pc.SetTotalLines(lineCount)
							writePrintState(printStatePath, printState{
								Filename:   snap.PrintFileName,
								TotalLines: lineCount,
							})
							printStateWritten = true
							log.Printf("Computed totalLines=%d for %s from file on disk", lineCount, snap.PrintFileName)
						}
					}
					printStateRestored = true // don't retry file reads every poll cycle
				}
			} else if !printStateWritten {
				// totalLines is set (from Upload) but we haven't persisted it yet.
				writePrintState(printStatePath, printState{
					Filename:   snap.PrintFileName,
					TotalLines: pc.TotalLines(),
				})
				printStateWritten = true
				log.Printf("Saved print state: %s (%d lines)", snap.PrintFileName, pc.TotalLines())
			}
		}

		// Spoolman filament usage tracking.
		if spoolmanMgr != nil {
			if snap.PrinterState == "printing" && spoolmanMgr.IsTracking() {
				spoolmanMgr.ReportUsage(snap.CurrentLine)
			}
			// Detect transition away from printing to stop tracking.
			if prevPrinterState == "printing" && snap.PrinterState != "printing" {
				spoolmanMgr.StopTracking()
			}
			// Restore Spoolman tracking after restart if printing but not tracking.
			if snap.PrinterState == "printing" && snap.PrintFileName != "" && !spoolmanMgr.IsTracking() {
				server.StartSpoolmanTracking(snap.PrintFileName)
			}
		}

		prevPrinterState = snap.PrinterState
	})
	poller.Start()

	// Handle graceful shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		log.Printf("Received signal %v, shutting down...", sig)

		poller.Stop()
		if spoolmanMgr != nil {
			spoolmanMgr.StopHealthCheck()
		}
		pc.Disconnect()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(ctx)

		os.Exit(0)
	}()

	// Start the HTTP server (blocks).
	if err := server.Start(); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

func runDiscovery() {
	log.Println("Discovering Snapmaker printers on the network...")

	printers, err := printer.Discover(5 * time.Second)
	if err != nil {
		log.Fatalf("Discovery failed: %v", err)
	}

	if len(printers) == 0 {
		fmt.Println("No printers found.")
		return
	}

	fmt.Printf("Found %d printer(s):\n", len(printers))
	for i, p := range printers {
		sacp := "no"
		if p.SACP {
			sacp = "yes"
		}
		fmt.Printf("  %d. %s (%s) - IP: %s, SACP: %s\n", i+1, p.Model, p.ID, p.IP, sacp)
	}
}
