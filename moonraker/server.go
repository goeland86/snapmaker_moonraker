package moonraker

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/john/snapmaker_moonraker/database"
	"github.com/john/snapmaker_moonraker/files"
	"github.com/john/snapmaker_moonraker/history"
	"github.com/john/snapmaker_moonraker/printer"
)

// ServerConfig holds the configuration needed by the Moonraker server.
type ServerConfig struct {
	Host string
	Port int
}

// Config is the full application config passed to the server.
type Config struct {
	Server  ServerConfig
	Printer struct {
		IP    string
		Token string
		Model string
	}
	Files struct {
		GCodeDir string
	}
}

// Server is the Moonraker-compatible HTTP/WebSocket server.
type Server struct {
	config        Config
	mux           *http.ServeMux
	httpServer    *http.Server
	printerClient *printer.Client
	state         *printer.State
	fileManager   *files.Manager
	database      *database.Database
	history       *history.Manager
	wsHub         *WSHub
}

// NewServer creates a new Moonraker server.
func NewServer(cfg Config, pc *printer.Client, st *printer.State, fm *files.Manager, db *database.Database, hist *history.Manager) *Server {
	s := &Server{
		config:        cfg,
		mux:           http.NewServeMux(),
		printerClient: pc,
		state:         st,
		fileManager:   fm,
		database:      db,
		history:       hist,
	}

	s.wsHub = NewWSHub(s)
	s.registerRoutes()
	s.httpServer = &http.Server{
		Addr:    fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port),
		Handler: corsMiddleware(s.mux),
	}

	return s
}

// History returns the history manager for external access.
func (s *Server) History() *history.Manager {
	return s.history
}

// WSHub returns the WebSocket hub for external access (e.g., status broadcasts).
func (s *Server) Hub() *WSHub {
	return s.wsHub
}

func (s *Server) registerRoutes() {
	s.registerServerHandlers()
	s.registerPrinterHandlers()
	s.registerFileHandlers()
	s.registerDatabaseHandlers()
	s.registerHistoryHandlers()

	// WebSocket endpoint.
	s.mux.HandleFunc("GET /websocket", s.wsHub.HandleWebSocket)

	// Root access endpoint (some frontends check this).
	s.mux.HandleFunc("GET /{$}", s.handleRoot)
	s.mux.HandleFunc("GET /access/info", s.handleAccessInfo)
	s.mux.HandleFunc("GET /access/api_key", s.handleAccessAPIKey)
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"result": "Snapmaker Moonraker Bridge",
	})
}

func (s *Server) handleAccessAPIKey(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"result": "snapmaker-moonraker-api-key",
	})
}

func (s *Server) handleAccessInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"result": map[string]interface{}{
			"default_source":  "moonraker",
			"available_sources": []string{"moonraker"},
		},
	})
}

// Start begins serving HTTP requests.
func (s *Server) Start() error {
	log.Printf("Moonraker server starting on %s", s.httpServer.Addr)
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// corsMiddleware adds CORS headers for frontend compatibility.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Api-Key, Authorization")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}
