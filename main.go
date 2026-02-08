package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/john/snapmaker_moonraker/files"
	"github.com/john/snapmaker_moonraker/moonraker"
	"github.com/john/snapmaker_moonraker/printer"
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

	// Initialize file manager.
	fm, err := files.NewManager(cfg.Files.GCodeDir)
	if err != nil {
		log.Fatalf("Failed to initialize file manager: %v", err)
	}
	log.Printf("GCode directory: %s", cfg.Files.GCodeDir)

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

	// Create the Moonraker server.
	server := moonraker.NewServer(moonCfg, pc, state, fm)

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
	poller := printer.NewStatePoller(pc, state, cfg.Printer.PollInterval, func(s *printer.State) {
		server.Hub().BroadcastStatusUpdate(s)
	})
	poller.Start()

	// Handle graceful shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		log.Printf("Received signal %v, shutting down...", sig)

		poller.Stop()
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
