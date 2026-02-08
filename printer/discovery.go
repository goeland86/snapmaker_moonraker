package printer

import (
	"fmt"
	"time"

	sacppkg "github.com/john/snapmaker_moonraker/sacp"
)

// DiscoveredPrinter holds information about a printer found on the network.
type DiscoveredPrinter struct {
	IP    string `json:"ip"`
	ID    string `json:"id"`
	Model string `json:"model"`
	SACP  bool   `json:"sacp"`
}

// Discover finds Snapmaker printers on the local network via UDP broadcast.
func Discover(timeout time.Duration) ([]DiscoveredPrinter, error) {
	printers, err := sacppkg.Discover(timeout)
	if err != nil {
		return nil, fmt.Errorf("discovery: %w", err)
	}

	var result []DiscoveredPrinter
	for _, p := range printers {
		result = append(result, DiscoveredPrinter{
			IP:    p.IP,
			ID:    p.ID,
			Model: p.Model,
			SACP:  p.SACP,
		})
	}
	return result, nil
}
