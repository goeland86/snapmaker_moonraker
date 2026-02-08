package printer

import (
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

	mu   sync.Mutex
	conn net.Conn
}

// NewClient creates a new printer client.
func NewClient(ip, token, model string) *Client {
	return &Client{
		ip:    ip,
		token: token,
		model: model,
	}
}

// Connect establishes a SACP TCP connection to the printer.
func (c *Client) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}

	conn, err := sacp.Connect(c.ip, sacpTimeout)
	if err != nil {
		return fmt.Errorf("SACP connect to %s: %w", c.ip, err)
	}
	c.conn = conn
	log.Printf("Connected to printer at %s:%d via SACP", c.ip, sacp.Port)
	return nil
}

// Disconnect closes the SACP connection.
func (c *Client) Disconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return nil
	}

	err := sacp.Disconnect(c.conn, sacpTimeout)
	c.conn.Close()
	c.conn = nil
	return err
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

// Home sends a home-all-axes command.
func (c *Client) Home() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return fmt.Errorf("not connected")
	}
	return sacp.Home(c.conn, sacpTimeout)
}

// SetToolTemperature sets the extruder temperature.
func (c *Client) SetToolTemperature(toolID int, temp int) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return fmt.Errorf("not connected")
	}
	return sacp.SetToolTemperature(c.conn, uint8(toolID), uint16(temp), sacpTimeout)
}

// SetBedTemperature sets the heated bed temperature.
func (c *Client) SetBedTemperature(toolID int, temp int) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return fmt.Errorf("not connected")
	}
	return sacp.SetBedTemperature(c.conn, uint8(toolID), uint16(temp), sacpTimeout)
}

// Upload uploads gcode data to the printer.
func (c *Client) Upload(filename string, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return fmt.Errorf("not connected")
	}
	return sacp.StartUpload(c.conn, filename, data, sacpTimeout)
}

// UploadFile uploads a file from a reader.
func (c *Client) UploadFile(filename string, r io.Reader, size int64) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("reading file data: %w", err)
	}
	return c.Upload(filename, data)
}

// ExecuteGCode sends a GCode command string via the HTTP API.
// SACP doesn't have a direct gcode execution command, so we use HTTP.
func (c *Client) ExecuteGCode(gcode string) (string, error) {
	c.mu.Lock()
	ip := c.ip
	token := c.token
	c.mu.Unlock()

	return executeGCodeHTTP(ip, token, gcode)
}

// GetStatus fetches printer status via the HTTP API.
func (c *Client) GetStatus() (map[string]interface{}, error) {
	c.mu.Lock()
	ip := c.ip
	token := c.token
	c.mu.Unlock()

	return getStatusHTTP(ip, token)
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
