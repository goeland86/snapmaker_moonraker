package printer

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"github.com/john/snapmaker_moonraker/sacp"
)

const sacpTimeout = 10 * time.Second

// Client wraps a SACP connection to a Snapmaker printer.
type Client struct {
	ip    string
	token string
	model string

	mu      sync.Mutex
	conn    net.Conn
	router  *PacketRouter
	writeMu sync.Mutex // serializes writes to conn

	// Subscription data (updated asynchronously by the packet router).
	subMu         sync.RWMutex
	extruderData  []sacp.ExtruderData
	bedData       []sacp.BedZoneData
	machineStatus sacp.MachineStatus
	currentLine   uint32
	totalLines    uint32
	printTime     uint32 // elapsed seconds
	printFilename string
	fanData       []sacp.FanData
	coordData     sacp.CoordinateData
}

// NewClient creates a new printer client.
func NewClient(ip, token, model string) *Client {
	return &Client{
		ip:    ip,
		token: token,
		model: model,
	}
}

// Connect establishes a SACP TCP connection to the printer,
// starts the background packet router, and subscribes to data feeds.
func (c *Client) Connect() error {
	// Clean up any existing connection first.
	c.mu.Lock()
	needCleanup := c.conn != nil || c.router != nil
	c.mu.Unlock()
	if needCleanup {
		c.Disconnect()
	}

	conn, err := sacp.Connect(c.ip, sacpTimeout)
	if err != nil {
		return fmt.Errorf("SACP connect to %s: %w", c.ip, err)
	}

	router := NewPacketRouter(conn, c.handleSubscription, c.handleDisconnect)
	router.Start()

	c.mu.Lock()
	c.conn = conn
	c.router = router
	c.mu.Unlock()

	log.Printf("Connected to printer at %s:%d via SACP", c.ip, sacp.Port)

	// Subscribe to data feeds and do initial queries.
	go c.setupSubscriptions()
	return nil
}

// setupSubscriptions subscribes to SACP data feeds after connection.
func (c *Client) setupSubscriptions() {
	// Initial temperature query.
	c.QueryTemperatures()

	// Subscribe to status feeds via the SACP subscription mechanism.
	// Send subscribe request to CommandSet=0x01, CommandID=0x00 with
	// payload [target_cmdset, target_cmdid, interval_lo, interval_hi].
	subs := []struct {
		cmdSet, cmdID byte
		name          string
	}{
		{0x01, 0xA0, "heartbeat"},
		{0xAC, 0xA0, "current line"},
		{0xAC, 0xA5, "print time"},
		{0x10, 0xA3, "fan info"},
	}
	for _, s := range subs {
		if err := c.subscribeTo(s.cmdSet, s.cmdID, 2000); err != nil {
			log.Printf("Subscribe %s (0x%02x/0x%02x) failed: %v", s.name, s.cmdSet, s.cmdID, err)
		} else {
			log.Printf("Subscribed to %s", s.name)
		}
	}

	// One-shot coordinate query.
	c.queryCoordinates()
}

// subscribeTo sends a SACP subscription request via the generic mechanism
// (CommandSet 0x01, CommandID 0x00).
func (c *Client) subscribeTo(targetCmdSet, targetCmdID byte, intervalMs uint16) error {
	data := []byte{
		targetCmdSet,
		targetCmdID,
		byte(intervalMs & 0xFF),
		byte(intervalMs >> 8),
	}
	return c.sendCommand(0x01, 0x00, data)
}

// Token returns the current authentication token.
func (c *Client) Token() string {
	return c.token
}

// QueryTemperatures sends one-shot temperature queries for extruder and bed.
func (c *Client) QueryTemperatures() {
	c.mu.Lock()
	conn := c.conn
	router := c.router
	c.mu.Unlock()

	if conn == nil || router == nil {
		return
	}

	// Query extruder temperatures (CommandSet 0x10, CommandID 0xa0).
	if err := c.sendQuery(conn, router, 0x10, 0xa0); err != nil {
		log.Printf("Extruder query failed: %v", err)
	}

	// Query bed temperatures (CommandSet 0x14, CommandID 0xa0).
	if err := c.sendQuery(conn, router, 0x14, 0xa0); err != nil {
		log.Printf("Bed query failed: %v", err)
	}
}

func (c *Client) sendQuery(conn net.Conn, router *PacketRouter, commandSet, commandID byte) error {
	data := &bytes.Buffer{}
	binary.Write(data, binary.LittleEndian, uint16(1000)) // interval (may be ignored by J1S)

	c.writeMu.Lock()
	seq, err := sacp.WritePacket(conn, commandSet, commandID, data.Bytes(), sacpTimeout)
	c.writeMu.Unlock()
	if err != nil {
		return err
	}

	resp, err := router.WaitForResponse(seq, sacpTimeout)
	if err != nil {
		return err
	}

	// The J1S includes query results directly in the ACK response
	// rather than sending a separate push packet (for bed data).
	if resp != nil && len(resp.Data) > 1 {
		c.handleSubscription(commandSet, commandID, resp.Data)
	}
	return nil
}

// queryCoordinates sends a one-shot coordinate query (CommandSet 0x01, CommandID 0x30).
func (c *Client) queryCoordinates() {
	c.mu.Lock()
	conn := c.conn
	router := c.router
	c.mu.Unlock()
	if conn == nil || router == nil {
		return
	}

	c.writeMu.Lock()
	seq, err := sacp.WritePacket(conn, 0x01, 0x30, nil, sacpTimeout)
	c.writeMu.Unlock()
	if err != nil {
		log.Printf("Coordinate query send failed: %v", err)
		return
	}

	resp, err := router.WaitForResponse(seq, sacpTimeout)
	if err != nil {
		log.Printf("Coordinate query timeout: %v", err)
		return
	}

	if resp != nil && len(resp.Data) > 4 {
		c.handleSubscription(0x01, 0x30, resp.Data)
	}
}

// queryFileInfo queries the current print file info (CommandSet 0xAC, CommandID 0x00).
func (c *Client) queryFileInfo() {
	c.mu.Lock()
	conn := c.conn
	router := c.router
	c.mu.Unlock()
	if conn == nil || router == nil {
		return
	}

	// Query basic file info from the controller.
	c.writeMu.Lock()
	seq, err := sacp.WritePacket(conn, 0xAC, 0x00, nil, sacpTimeout)
	c.writeMu.Unlock()
	if err != nil {
		log.Printf("File info query send failed: %v", err)
		return
	}

	resp, err := router.WaitForResponse(seq, sacpTimeout)
	if err != nil {
		log.Printf("File info query timeout: %v", err)
		return
	}

	if resp != nil && len(resp.Data) > 1 {
		fi, err := sacp.ParseFileInfo(resp.Data)
		if err != nil {
			log.Printf("File info parse error: %v (data=%x)", err, resp.Data)
			return
		}
		c.subMu.Lock()
		c.printFilename = fi.Filename
		c.subMu.Unlock()
		log.Printf("Print file: %s", fi.Filename)
	}

	// Also try to get total lines and estimated time from the screen (0xAC/0x1A).
	c.queryPrintingFileInfo()
}

// queryPrintingFileInfo queries extended file info from the screen MCU.
func (c *Client) queryPrintingFileInfo() {
	c.mu.Lock()
	conn := c.conn
	router := c.router
	c.mu.Unlock()
	if conn == nil || router == nil {
		return
	}

	c.writeMu.Lock()
	seq, err := sacp.WritePacketTo(conn, 2, 0xAC, 0x1A, nil, sacpTimeout)
	c.writeMu.Unlock()
	if err != nil {
		return
	}

	resp, err := router.WaitForResponse(seq, 3*time.Second)
	if err != nil {
		log.Printf("Printing file info not available (screen query): %v", err)
		return
	}

	if resp != nil && len(resp.Data) > 1 {
		fi, err := sacp.ParsePrintingFileInfo(resp.Data)
		if err != nil {
			log.Printf("Printing file info parse error: %v (data=%x)", err, resp.Data)
			return
		}
		c.subMu.Lock()
		if fi.Filename != "" {
			c.printFilename = fi.Filename
		}
		c.totalLines = fi.TotalLines
		c.subMu.Unlock()
		log.Printf("Print details: file=%s totalLines=%d estTime=%ds", fi.Filename, fi.TotalLines, fi.EstimatedTime)
	}
}

// handleSubscription is called by the packet router when subscription/query data arrives.
func (c *Client) handleSubscription(commandSet, commandID byte, data []byte) {
	switch {
	case commandSet == 0x10 && commandID == 0xa0:
		// Extruder temperature data.
		extruders := sacp.ParseExtruderInfo(data)
		if len(extruders) > 0 {
			c.subMu.Lock()
			for _, e := range extruders {
				found := false
				for i, existing := range c.extruderData {
					if existing.HeadID == e.HeadID {
						c.extruderData[i] = e
						found = true
						break
					}
				}
				if !found {
					c.extruderData = append(c.extruderData, e)
				}
			}
			c.subMu.Unlock()
		}

	case commandSet == 0x14 && commandID == 0xa0:
		// Bed temperature data.
		zones := sacp.ParseBedInfo(data)
		if len(zones) > 0 {
			c.subMu.Lock()
			c.bedData = zones
			c.subMu.Unlock()
		}

	case commandSet == 0x01 && commandID == 0xa0:
		// Heartbeat - machine status.
		status, err := sacp.ParseHeartbeat(data)
		if err != nil {
			log.Printf("Heartbeat parse error: %v (data=%x)", err, data)
			return
		}

		c.subMu.Lock()
		prevStatus := c.machineStatus
		c.machineStatus = status
		c.subMu.Unlock()

		if status != prevStatus {
			log.Printf("Machine status: %s -> %s", prevStatus, status)
		}

		// When transitioning to printing, query file info.
		if (status == sacp.MachineStatusPrinting || status == sacp.MachineStatusStarting) &&
			prevStatus != sacp.MachineStatusPrinting && prevStatus != sacp.MachineStatusStarting {
			go c.queryFileInfo()
		}

		// When idle/completed/stopped, clear print data.
		if status == sacp.MachineStatusIdle || status == sacp.MachineStatusCompleted || status == sacp.MachineStatusStopped {
			c.subMu.Lock()
			c.printFilename = ""
			c.currentLine = 0
			c.totalLines = 0
			c.printTime = 0
			c.subMu.Unlock()
		}

	case commandSet == 0xAC && commandID == 0xa0:
		// Current print line number.
		line, err := sacp.ParseCurrentLine(data)
		if err != nil {
			return
		}
		c.subMu.Lock()
		c.currentLine = line
		c.subMu.Unlock()

	case commandSet == 0xAC && commandID == 0xa5:
		// Elapsed print time.
		secs, err := sacp.ParsePrintTime(data)
		if err != nil {
			return
		}
		c.subMu.Lock()
		c.printTime = secs
		c.subMu.Unlock()

	case commandSet == 0x10 && commandID == 0xa3:
		// Fan info.
		fans, err := sacp.ParseFanInfo(data)
		if err != nil {
			return
		}
		c.subMu.Lock()
		for _, f := range fans {
			found := false
			for i, existing := range c.fanData {
				if existing.HeadID == f.HeadID && existing.FanIndex == f.FanIndex {
					c.fanData[i] = f
					found = true
					break
				}
			}
			if !found {
				c.fanData = append(c.fanData, f)
			}
		}
		c.subMu.Unlock()

	case commandSet == 0x01 && commandID == 0x30:
		// Coordinate info.
		cd, err := sacp.ParseCoordinateInfo(data)
		if err != nil {
			log.Printf("Coordinate parse error: %v (data=%x)", err, data)
			return
		}
		c.subMu.Lock()
		c.coordData = cd
		c.subMu.Unlock()
	}
}

// handleDisconnect is called by the packet router when the connection breaks unexpectedly.
func (c *Client) handleDisconnect() {
	c.mu.Lock()
	if c.conn != nil {
		c.conn.Close()
	}
	c.conn = nil
	c.router = nil
	c.mu.Unlock()

	c.subMu.Lock()
	c.extruderData = nil
	c.bedData = nil
	c.fanData = nil
	c.machineStatus = sacp.MachineStatusIdle
	c.currentLine = 0
	c.totalLines = 0
	c.printTime = 0
	c.printFilename = ""
	c.subMu.Unlock()

	log.Printf("Printer connection lost")
}

// Reconnect drops any existing connection and establishes a new one.
func (c *Client) Reconnect() error {
	log.Printf("Reconnecting to printer at %s...", c.ip)
	c.Disconnect()
	return c.Connect()
}

// Disconnect closes the SACP connection.
func (c *Client) Disconnect() error {
	c.mu.Lock()
	conn := c.conn
	router := c.router
	c.conn = nil
	c.router = nil
	c.mu.Unlock()

	if router != nil {
		router.Stop()
	}
	if conn != nil {
		sacp.Disconnect(conn, sacpTimeout)
		conn.Close()
	}

	c.subMu.Lock()
	c.extruderData = nil
	c.bedData = nil
	c.fanData = nil
	c.machineStatus = sacp.MachineStatusIdle
	c.subMu.Unlock()

	return nil
}

// Connected returns true if a SACP connection is active.
func (c *Client) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn != nil
}

// IP returns the printer's IP address.
func (c *Client) IP() string {
	return c.ip
}

// Model returns the printer model string.
func (c *Client) Model() string {
	return c.model
}

// sendCommand sends a SACP command via the router and waits for the response.
func (c *Client) sendCommand(commandSet, commandID byte, data []byte) error {
	c.mu.Lock()
	conn := c.conn
	router := c.router
	c.mu.Unlock()

	if conn == nil || router == nil {
		return fmt.Errorf("not connected")
	}

	c.writeMu.Lock()
	seq, err := sacp.WritePacket(conn, commandSet, commandID, data, sacpTimeout)
	c.writeMu.Unlock()
	if err != nil {
		return err
	}

	p, err := router.WaitForResponse(seq, sacpTimeout)
	if err != nil {
		return err
	}

	if len(p.Data) >= 1 && p.Data[0] == 0 {
		return nil
	}
	if len(p.Data) >= 1 {
		return fmt.Errorf("command 0x%02x/0x%02x failed: code %d", commandSet, commandID, p.Data[0])
	}
	return nil
}

// Home sends a home-all-axes command.
func (c *Client) Home() error {
	data := &bytes.Buffer{}
	data.WriteByte(0x00)
	return c.sendCommand(0x01, 0x35, data.Bytes())
}

// SetToolTemperature sets the extruder temperature.
func (c *Client) SetToolTemperature(toolID int, temp int) error {
	data := &bytes.Buffer{}
	data.WriteByte(0x08)
	data.WriteByte(byte(toolID))
	binary.Write(data, binary.LittleEndian, uint16(temp))
	return c.sendCommand(0x10, 0x02, data.Bytes())
}

// SetBedTemperature sets the heated bed temperature.
func (c *Client) SetBedTemperature(toolID int, temp int) error {
	data := &bytes.Buffer{}
	data.WriteByte(0x05)
	data.WriteByte(byte(toolID))
	binary.Write(data, binary.LittleEndian, uint16(temp))
	return c.sendCommand(0x14, 0x02, data.Bytes())
}

// Upload uploads gcode data to the printer.
// Temporarily stops the router to take direct control of the connection.
func (c *Client) Upload(filename string, data []byte) error {
	c.mu.Lock()
	conn := c.conn
	router := c.router
	c.router = nil
	c.mu.Unlock()

	if conn == nil {
		return fmt.Errorf("not connected")
	}

	// Stop the router so we can use the connection directly for multi-packet upload.
	if router != nil {
		router.Stop()
	}

	err := sacp.StartUpload(conn, filename, data, sacpTimeout)

	// Restart the router and re-subscribe.
	newRouter := NewPacketRouter(conn, c.handleSubscription, c.handleDisconnect)
	newRouter.Start()

	c.mu.Lock()
	c.router = newRouter
	c.mu.Unlock()

	go c.setupSubscriptions()

	return err
}

// UploadFile uploads a file from a reader.
func (c *Client) UploadFile(filename string, r io.Reader, size int64) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("reading file data: %w", err)
	}
	return c.Upload(filename, data)
}

// ExecuteGCode sends a GCode command via SACP.
func (c *Client) ExecuteGCode(gcode string) (string, error) {
	c.mu.Lock()
	conn := c.conn
	router := c.router
	c.mu.Unlock()

	if conn == nil || router == nil {
		return "", fmt.Errorf("not connected")
	}

	// Build GCode payload: length-prefixed string.
	payload := &bytes.Buffer{}
	binary.Write(payload, binary.LittleEndian, uint16(len(gcode)))
	payload.WriteString(gcode)

	c.writeMu.Lock()
	seq, err := sacp.WritePacket(conn, 0x01, 0x02, payload.Bytes(), sacpTimeout)
	c.writeMu.Unlock()
	if err != nil {
		return "", err
	}

	p, err := router.WaitForResponse(seq, sacpTimeout)
	if err != nil {
		return "", err
	}

	if len(p.Data) < 1 {
		return "", nil
	}
	if p.Data[0] != 0 {
		return "", fmt.Errorf("gcode execution failed with code %d", p.Data[0])
	}
	if len(p.Data) > 1 {
		return string(p.Data[1:]), nil
	}
	return "", nil
}

// GetStatus returns the current printer status entirely from SACP subscription data.
func (c *Client) GetStatus() (map[string]interface{}, error) {
	if !c.Connected() {
		return nil, fmt.Errorf("not connected")
	}

	c.subMu.RLock()
	defer c.subMu.RUnlock()

	// Map machine status to the string format expected by parseStatus.
	status := "IDLE"
	switch c.machineStatus {
	case sacp.MachineStatusIdle, sacp.MachineStatusCompleted, sacp.MachineStatusStopped:
		status = "IDLE"
	case sacp.MachineStatusPrinting, sacp.MachineStatusStarting,
		sacp.MachineStatusFinishing, sacp.MachineStatusResuming:
		status = "RUNNING"
	case sacp.MachineStatusPaused, sacp.MachineStatusPausing:
		status = "PAUSED"
	case sacp.MachineStatusStopping:
		status = "RUNNING"
	case sacp.MachineStatusRecovering:
		status = "PAUSED"
	}

	// Calculate progress from current/total lines.
	progress := 0.0
	if c.totalLines > 0 {
		progress = float64(c.currentLine) / float64(c.totalLines) * 100.0
		if progress > 100 {
			progress = 100
		}
	}

	// Get part fan speed (fan type 0 = part fan). Convert 0-255 to 0-100%.
	fanSpeed := 0.0
	for _, f := range c.fanData {
		if f.FanType == 0 {
			fanSpeed = float64(f.Speed) / 255.0 * 100.0
			break
		}
	}

	result := map[string]interface{}{
		"status":      status,
		"progress":    progress,
		"elapsedTime": float64(c.printTime),
		"fileName":    c.printFilename,
		"x":           c.coordData.X,
		"y":           c.coordData.Y,
		"z":           c.coordData.Z,
		"fanSpeed":    fanSpeed,
		"homed":       c.coordData.Homed,
	}

	// Temperature data.
	for _, e := range c.extruderData {
		switch e.HeadID {
		case 0:
			result["t0Temp"] = e.CurrentTemp
			result["t0Target"] = e.TargetTemp
		case 1:
			result["t1Temp"] = e.CurrentTemp
			result["t1Target"] = e.TargetTemp
		}
	}

	for _, z := range c.bedData {
		if z.Index == 0 {
			result["heatbedTemp"] = z.CurrentTemp
			result["heatbedTarget"] = z.TargetTemp
		}
	}

	return result, nil
}

// Ping checks if the printer is reachable.
func (c *Client) Ping() bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", c.ip, sacp.Port), 3*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
