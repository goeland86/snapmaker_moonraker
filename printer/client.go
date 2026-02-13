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
	subMu        sync.RWMutex
	extruderData []sacp.ExtruderData
	bedData      []sacp.BedZoneData
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
// starts the background packet router, and subscribes to temperature data.
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

	// Initial temperature query.
	go c.QueryTemperatures()
	return nil
}

// QueryTemperatures sends one-shot temperature queries for extruder and bed.
// The J1S responds with an ACK followed by a data push; the data push is
// handled asynchronously by the packet router's subscription handler.
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

// handleSubscription is called by the packet router when subscription/query data arrives.
func (c *Client) handleSubscription(commandSet, commandID byte, data []byte) {
	switch {
	case commandSet == 0x10 && commandID == 0xa0:
		extruders := sacp.ParseExtruderInfo(data)
		for _, e := range extruders {
			log.Printf("Extruder head=%d idx=%d: cur=%.1f°C target=%.1f°C", e.HeadID, e.Index, e.CurrentTemp, e.TargetTemp)
		}
		if len(extruders) > 0 {
			c.subMu.Lock()
			// J1S sends separate packets per nozzle (HeadID=0 for T0, HeadID=1 for T1).
			// Merge into the existing slice rather than replacing.
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
		zones := sacp.ParseBedInfo(data)
		log.Printf("Bed data: %d bytes, raw=%x, parsed %d zones", len(data), data, len(zones))
		for _, z := range zones {
			log.Printf("  Bed[%d]: cur=%.1f°C target=%.1f°C", z.Index, z.CurrentTemp, z.TargetTemp)
		}
		if len(zones) > 0 {
			c.subMu.Lock()
			c.bedData = zones
			c.subMu.Unlock()
		}
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

	go c.QueryTemperatures()

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

// GetStatus returns the current printer status from subscription data.
func (c *Client) GetStatus() (map[string]interface{}, error) {
	if !c.Connected() {
		return nil, fmt.Errorf("not connected")
	}

	c.subMu.RLock()
	defer c.subMu.RUnlock()

	result := map[string]interface{}{
		"status": "IDLE",
	}

	for _, e := range c.extruderData {
		// J1S sends separate packets per nozzle: HeadID=0 → T0 (left), HeadID=1 → T1 (right).
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
