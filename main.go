package main

import (
	"context"
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
