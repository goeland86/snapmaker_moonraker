package moonraker

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/john/snapmaker_moonraker/printer"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// jsonRPCRequest represents an incoming JSON-RPC 2.0 request.
type jsonRPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
	ID      interface{} `json:"id"`
}

// jsonRPCResponse represents an outgoing JSON-RPC 2.0 response.
type jsonRPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	Result  interface{} `json:"result,omitempty"`
	Error   *rpcError   `json:"error,omitempty"`
	ID      interface{} `json:"id"`
}

// jsonRPCNotification represents a server-to-client notification (no id).
type jsonRPCNotification struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// WSClient represents a connected WebSocket client.
type WSClient struct {
	conn         *websocket.Conn
	mu           sync.Mutex
	subscribed   map[string]interface{} // object name -> requested fields
	isSubscribed bool
}

func (c *WSClient) send(v interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.WriteJSON(v)
}

// WSHub manages all WebSocket clients.
type WSHub struct {
	mu      sync.RWMutex
	clients map[*WSClient]bool
	server  *Server
}

func NewWSHub(s *Server) *WSHub {
	return &WSHub{
		clients: make(map[*WSClient]bool),
		server:  s,
	}
}

func (h *WSHub) register(c *WSClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[c] = true
}

func (h *WSHub) unregister(c *WSClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, c)
}

// BroadcastStatusUpdate sends notify_status_update to all subscribed clients.
func (h *WSHub) BroadcastStatusUpdate(state *printer.State) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	snap := state.Snapshot()
	objects := &PrinterObjects{}

	for client := range h.clients {
		if !client.isSubscribed || len(client.subscribed) == 0 {
			continue
		}

		status := objects.Query(snap, client.subscribed)
		notification := jsonRPCNotification{
			JSONRPC: "2.0",
			Method:  "notify_status_update",
			Params:  []interface{}{status, 0.0},
		}

		if err := client.send(notification); err != nil {
			log.Printf("WebSocket send error: %v", err)
		}
	}
}

// BroadcastNotification sends a notification to all connected clients.
func (h *WSHub) BroadcastNotification(method string, params interface{}) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	notification := jsonRPCNotification{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}

	for client := range h.clients {
		if err := client.send(notification); err != nil {
			log.Printf("WebSocket broadcast error: %v", err)
		}
	}
}

// BroadcastHistoryChanged sends notify_history_changed to all clients.
func (h *WSHub) BroadcastHistoryChanged(action string, job interface{}) {
	h.BroadcastNotification("notify_history_changed", []interface{}{
		map[string]interface{}{
			"action": action,
			"job":    job,
		},
	})
}

// BroadcastGCodeResponse sends notify_gcode_response to all clients.
func (h *WSHub) BroadcastGCodeResponse(response string) {
	h.BroadcastNotification("notify_gcode_response", []interface{}{response})
}

// HandleWebSocket upgrades the HTTP connection to WebSocket and processes JSON-RPC.
func (h *WSHub) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}

	client := &WSClient{
		conn:       conn,
		subscribed: make(map[string]interface{}),
	}
	h.register(client)
	defer func() {
		h.unregister(client)
		conn.Close()
	}()

	log.Printf("WebSocket client connected from %s", r.RemoteAddr)

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("WebSocket read error: %v", err)
			}
			break
		}

		var req jsonRPCRequest
		if err := json.Unmarshal(message, &req); err != nil {
			client.send(jsonRPCResponse{
				JSONRPC: "2.0",
				Error:   &rpcError{Code: -32700, Message: "Parse error"},
				ID:      nil,
			})
			continue
		}

		h.handleRPC(client, &req)
	}
}

func (h *WSHub) handleRPC(client *WSClient, req *jsonRPCRequest) {
	log.Printf("WebSocket RPC: method=%s id=%v", req.Method, req.ID)

	var resp jsonRPCResponse
	resp.JSONRPC = "2.0"
	resp.ID = req.ID

	switch req.Method {
	case "server.info":
		resp.Result = h.server.serverInfo()

	case "server.connection.identify":
		resp.Result = map[string]interface{}{
			"connection_id": 1,
		}

	case "connection.register_remote_method":
		resp.Result = "ok"

	case "printer.info":
		resp.Result = h.server.printerInfo()

	case "printer.objects.list":
		objects := &PrinterObjects{}
		resp.Result = map[string]interface{}{
			"objects": objects.AvailableObjects(),
		}

	case "printer.objects.query":
		resp.Result = h.handleObjectsQuery(req)

	case "printer.objects.subscribe":
		resp.Result = h.handleObjectsSubscribe(client, req)

	case "printer.gcode.script":
		resp.Result = h.handleGCodeScript(req)

	case "printer.print.start":
		resp.Result = h.handlePrintStart(req)

	case "printer.print.pause":
		resp.Result = h.handlePrintControl("pause")

	case "printer.print.resume":
		resp.Result = h.handlePrintControl("resume")

	case "printer.print.cancel":
		resp.Result = h.handlePrintControl("cancel")

	case "printer.emergency_stop":
		resp.Result = h.handleEmergencyStop()

	case "server.files.list":
		root := extractStringParam(req.Params, "root")
		if root == "" {
			root = "gcodes"
		}
		resp.Result = h.server.fileManager.ListFiles(root)

	case "server.config":
		resp.Result = h.server.serverConfig()

	case "server.files.metadata":
		resp.Result = h.handleFileMetadata(req)

	case "server.files.get_directory":
		resp.Result = h.handleFilesGetDirectory(req.Params)

	case "server.files.post_directory":
		resp.Result = h.handleFilesPostDirectory(req.Params)

	case "server.files.delete_directory":
		resp.Result = h.handleFilesDeleteDirectory(req.Params)

	case "server.files.delete_file":
		resp.Result = h.handleFilesDeleteFile(req.Params)

	case "server.files.move":
		resp.Result = h.handleFilesMove(req.Params)

	case "server.files.roots":
		resp.Result = h.handleFilesRoots()

	case "machine.system_info":
		resp.Result = h.server.machineSystemInfo()

	case "machine.proc_stats":
		resp.Result = h.server.machineProcStats()

	case "machine.services.list":
		resp.Result = h.server.machineServicesList()

	case "machine.services.restart":
		resp.Result = h.handleMachineServiceAction("restart", req.Params)

	case "machine.services.stop":
		resp.Result = h.handleMachineServiceAction("stop", req.Params)

	case "machine.services.start":
		resp.Result = h.handleMachineServiceAction("start", req.Params)

	case "server.temperature_store":
		resp.Result = h.server.temperatureStore()

	case "server.gcode_store":
		resp.Result = h.server.gcodeStore()

	case "server.announcements.list":
		resp.Result = h.handleAnnouncementsList()

	case "server.announcements.update":
		resp.Result = h.handleAnnouncementsUpdate()

	case "server.webcams.list":
		resp.Result = h.server.getWebcamsList()

	// Database methods
	case "server.database.list":
		resp.Result = h.handleDatabaseList()

	case "server.database.get_item":
		resp.Result = h.handleDatabaseGetItem(req.Params)

	case "server.database.post_item":
		resp.Result = h.handleDatabasePostItem(req.Params)

	case "server.database.delete_item":
		resp.Result = h.handleDatabaseDeleteItem(req.Params)

	// History methods
	case "server.history.list":
		resp.Result = h.handleHistoryList(req.Params)

	case "server.history.get_job":
		resp.Result = h.handleHistoryGetJob(req.Params)

	case "server.history.delete_job":
		resp.Result = h.handleHistoryDeleteJob(req.Params)

	case "server.history.totals":
		resp.Result = h.handleHistoryTotals()

	case "server.history.reset_totals":
		resp.Result = h.handleHistoryResetTotals()

	// Spoolman methods
	case "server.spoolman.status":
		if h.server.spoolman == nil {
			resp.Error = &rpcError{Code: -32601, Message: "Spoolman not configured"}
		} else {
			resp.Result = h.handleSpoolmanStatus()
		}

	case "server.spoolman.get_spool_id":
		if h.server.spoolman == nil {
			resp.Error = &rpcError{Code: -32601, Message: "Spoolman not configured"}
		} else {
			resp.Result = h.handleSpoolmanGetSpoolID()
		}

	case "server.spoolman.post_spool_id":
		if h.server.spoolman == nil {
			resp.Error = &rpcError{Code: -32601, Message: "Spoolman not configured"}
		} else {
			resp.Result = h.handleSpoolmanSetSpoolID(req.Params)
		}

	case "server.spoolman.proxy":
		if h.server.spoolman == nil {
			resp.Error = &rpcError{Code: -32601, Message: "Spoolman not configured"}
		} else {
			resp.Result = h.handleSpoolmanProxy(req.Params)
		}

	default:
		log.Printf("WebSocket RPC: UNKNOWN method=%s", req.Method)
		resp.Error = &rpcError{
			Code:    -32601,
			Message: "Method not found: " + req.Method,
		}
	}

	if resp.Error != nil {
		log.Printf("WebSocket RPC error: method=%s code=%d msg=%s", req.Method, resp.Error.Code, resp.Error.Message)
	}

	if err := client.send(resp); err != nil {
		log.Printf("WebSocket response send error: %v", err)
	}
}

func (h *WSHub) handleObjectsQuery(req *jsonRPCRequest) interface{} {
	objects := &PrinterObjects{}
	snap := h.server.state.Snapshot()

	requested := extractObjectsParam(req.Params)
	status := objects.Query(snap, requested)

	return map[string]interface{}{
		"eventtime": 0.0,
		"status":    status,
	}
}

func (h *WSHub) handleObjectsSubscribe(client *WSClient, req *jsonRPCRequest) interface{} {
	objects := &PrinterObjects{}
	snap := h.server.state.Snapshot()

	requested := extractObjectsParam(req.Params)

	// Store subscription.
	client.subscribed = requested
	client.isSubscribed = true

	status := objects.Query(snap, requested)

	return map[string]interface{}{
		"eventtime": 0.0,
		"status":    status,
	}
}

func (h *WSHub) handleGCodeScript(req *jsonRPCRequest) interface{} {
	script := extractStringParam(req.Params, "script")
	if script == "" {
		return map[string]interface{}{}
	}

	// Intercept FIRMWARE_RESTART and RESTART to trigger printer reconnection.
	upperScript := strings.ToUpper(strings.TrimSpace(script))
	if upperScript == "FIRMWARE_RESTART" || upperScript == "RESTART" {
		go func() {
			if err := h.server.printerClient.Reconnect(); err != nil {
				log.Printf("Reconnect failed: %v", err)
				h.BroadcastNotification("notify_gcode_response", []interface{}{
					"Error: reconnect failed - " + err.Error(),
				})
			} else {
				h.BroadcastNotification("notify_gcode_response", []interface{}{
					"Reconnected to printer successfully",
				})
			}
		}()
		return map[string]interface{}{}
	}

	// Intercept ? and HELP — these are Klipper console commands, not real GCode.
	if upperScript == "?" || upperScript == "HELP" {
		h.BroadcastNotification("notify_gcode_response", []interface{}{gcodeHelpText()})
		return map[string]interface{}{}
	}

	result, err := h.server.printerClient.ExecuteGCode(script)
	if err != nil {
		log.Printf("GCode execution error: %v", err)
		h.BroadcastNotification("notify_gcode_response", []interface{}{
			"Error: " + err.Error(),
		})
		return map[string]interface{}{}
	}

	// Send gcode response notification.
	if result != "" {
		h.BroadcastNotification("notify_gcode_response", []interface{}{result})
	}

	return map[string]interface{}{}
}

func (h *WSHub) handlePrintStart(req *jsonRPCRequest) interface{} {
	filename := extractStringParam(req.Params, "filename")
	if filename == "" {
		return map[string]interface{}{}
	}

	data, err := h.server.fileManager.ReadFile("gcodes", filename)
	if err != nil {
		log.Printf("Error reading file for print: %v", err)
		return map[string]interface{}{}
	}

	if err := h.server.printerClient.Upload(filename, data); err != nil {
		log.Printf("Error uploading to printer: %v", err)
		return map[string]interface{}{}
	}

	h.server.StartSpoolmanTracking(filename)

	return map[string]interface{}{}
}

func (h *WSHub) handlePrintControl(action string) interface{} {
	var err error
	switch action {
	case "pause":
		err = h.server.printerClient.PausePrint()
	case "resume":
		err = h.server.printerClient.ResumePrint()
	case "cancel":
		err = h.server.printerClient.StopPrint()
	}

	if err != nil {
		log.Printf("Print %s error: %v", action, err)
	}

	return map[string]interface{}{}
}

func (h *WSHub) handleEmergencyStop() interface{} {
	if _, err := h.server.printerClient.ExecuteGCode("M112"); err != nil {
		log.Printf("Emergency stop error: %v", err)
	}
	return map[string]interface{}{}
}

func (h *WSHub) handleFileMetadata(req *jsonRPCRequest) interface{} {
	filename := extractStringParam(req.Params, "filename")
	if filename == "" {
		return map[string]interface{}{
			"filename": "",
			"size":     0,
			"modified": float64(0),
		}
	}

	meta, err := h.server.fileManager.GetMetadata("gcodes", filename)
	if err != nil {
		return map[string]interface{}{
			"filename": filename,
			"size":     0,
			"modified": float64(0),
		}
	}
	return meta
}

func (h *WSHub) handleFilesGetDirectory(params interface{}) interface{} {
	path := extractStringParam(params, "path")
	root := extractStringParam(params, "root")
	if root == "" {
		// Detect root from path prefix (e.g., path="config" or path="config/subdir").
		if path == "config" || strings.HasPrefix(path, "config/") {
			root = "config"
			path = strings.TrimPrefix(path, "config")
			path = strings.TrimPrefix(path, "/")
		} else {
			root = "gcodes"
		}
	}
	return h.server.fileManager.GetDirectory(root, path)
}

func (h *WSHub) handleFilesDeleteFile(params interface{}) interface{} {
	path := extractStringParam(params, "path")
	if path == "" {
		return map[string]interface{}{}
	}

	// Path comes as "root/filename" (e.g., "gcodes/wecreat_test.nc").
	root := "gcodes"
	filePath := path
	if strings.HasPrefix(path, "gcodes/") {
		filePath = strings.TrimPrefix(path, "gcodes/")
	}

	if err := h.server.fileManager.DeleteFile(root, filePath); err != nil {
		log.Printf("Delete file error: %v", err)
		return map[string]interface{}{}
	}

	h.BroadcastNotification("notify_filelist_changed", []interface{}{
		map[string]interface{}{
			"action": "delete_file",
			"item": map[string]interface{}{
				"root": root,
				"path": filePath,
			},
		},
	})

	return map[string]interface{}{
		"item": map[string]interface{}{
			"path": filePath,
			"root": root,
		},
		"action": "delete_file",
	}
}

func (h *WSHub) handleFilesPostDirectory(params interface{}) interface{} {
	path := extractStringParam(params, "path")
	if path == "" {
		return map[string]interface{}{}
	}

	root := "gcodes"
	dirPath := path
	if strings.HasPrefix(path, "gcodes/") {
		dirPath = strings.TrimPrefix(path, "gcodes/")
	} else if path == "gcodes" {
		dirPath = ""
	}

	if err := h.server.fileManager.CreateDirectory(root, dirPath); err != nil {
		log.Printf("Create directory error: %v", err)
		return map[string]interface{}{}
	}

	h.BroadcastNotification("notify_filelist_changed", []interface{}{
		map[string]interface{}{
			"action": "create_dir",
			"item": map[string]interface{}{
				"root": root,
				"path": dirPath,
			},
		},
	})

	return map[string]interface{}{
		"item": map[string]interface{}{
			"path": dirPath,
			"root": root,
		},
		"action": "create_dir",
	}
}

func (h *WSHub) handleFilesDeleteDirectory(params interface{}) interface{} {
	path := extractStringParam(params, "path")
	if path == "" {
		return map[string]interface{}{}
	}

	root := "gcodes"
	dirPath := path
	if strings.HasPrefix(path, "gcodes/") {
		dirPath = strings.TrimPrefix(path, "gcodes/")
	}

	if err := h.server.fileManager.DeleteDirectory(root, dirPath); err != nil {
		log.Printf("Delete directory error: %v", err)
		return map[string]interface{}{}
	}

	h.BroadcastNotification("notify_filelist_changed", []interface{}{
		map[string]interface{}{
			"action": "delete_dir",
			"item": map[string]interface{}{
				"root": root,
				"path": dirPath,
			},
		},
	})

	return map[string]interface{}{
		"item": map[string]interface{}{
			"path": dirPath,
			"root": root,
		},
		"action": "delete_dir",
	}
}

func (h *WSHub) handleFilesMove(params interface{}) interface{} {
	source := extractStringParam(params, "source")
	dest := extractStringParam(params, "dest")
	if source == "" || dest == "" {
		return map[string]interface{}{}
	}

	srcPath := h.server.fileManager.ResolvePath(source)
	dstPath := h.server.fileManager.ResolvePath(dest)

	if err := h.server.fileManager.MoveFile(srcPath, dstPath); err != nil {
		log.Printf("Move file error: %v", err)
		return map[string]interface{}{}
	}

	h.BroadcastNotification("notify_filelist_changed", []interface{}{
		map[string]interface{}{
			"action": "move_file",
			"item": map[string]interface{}{
				"path":        dest,
				"root":        "gcodes",
				"source_path": source,
			},
		},
	})

	return map[string]interface{}{
		"item": map[string]interface{}{
			"path":        dest,
			"root":        "gcodes",
			"source_path": source,
		},
		"action": "move_file",
	}
}

func (h *WSHub) handleFilesRoots() interface{} {
	return []map[string]interface{}{
		{
			"name":        "gcodes",
			"path":        h.server.fileManager.GetRootPath("gcodes"),
			"permissions": "rw",
		},
		{
			"name":        "config",
			"path":        h.server.fileManager.GetRootPath("config"),
			"permissions": "rw",
		},
	}
}

func (h *WSHub) handleAnnouncementsList() interface{} {
	return map[string]interface{}{
		"entries": []interface{}{},
		"feeds":   []interface{}{},
	}
}

func (h *WSHub) handleMachineServiceAction(action string, params interface{}) interface{} {
	service := extractStringParam(params, "service")
	if err := machineServiceAction(action, service); err != nil {
		log.Printf("Service %s error: %v", action, err)
		return map[string]interface{}{"error": err.Error()}
	}
	return "ok"
}

func (h *WSHub) handleAnnouncementsUpdate() interface{} {
	return map[string]interface{}{
		"modified": false,
	}
}

// extractObjectsParam pulls the "objects" map from params (handles both map and positional).
func extractObjectsParam(params interface{}) map[string]interface{} {
	if params == nil {
		return map[string]interface{}{}
	}

	switch p := params.(type) {
	case map[string]interface{}:
		if objects, ok := p["objects"].(map[string]interface{}); ok {
			return objects
		}
		return p
	case []interface{}:
		if len(p) > 0 {
			if objects, ok := p[0].(map[string]interface{}); ok {
				return objects
			}
		}
	}
	return map[string]interface{}{}
}

// extractStringParam pulls a string field from params.
func extractStringParam(params interface{}, key string) string {
	if params == nil {
		return ""
	}

	switch p := params.(type) {
	case map[string]interface{}:
		if v, ok := p[key].(string); ok {
			return v
		}
	}
	return ""
}
