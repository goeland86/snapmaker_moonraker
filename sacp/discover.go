// Adapted from github.com/macdylan/sm2uploader (MIT License).
package sacp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// Printer holds information about a discovered Snapmaker printer.
type Printer struct {
	IP    string
	ID    string
	Model string
	Token string
	SACP  bool
}

// String returns a human-readable representation of the printer.
func (p *Printer) String() string {
	return fmt.Sprintf("%s@%s - %s", p.ID, p.IP, p.Model)
}

// ParsePrinter parses a discovery response into a Printer.
// Format: "Snapmaker J1X123P@192.168.1.201|model:Snapmaker J1|status:IDLE|SACP:1"
func ParsePrinter(resp []byte) (*Printer, error) {
	msg := string(resp)
	if !strings.Contains(msg, "|model:") || !strings.Contains(msg, "@") {
		return nil, errors.New("invalid discovery response")
	}

	parts := strings.Split(msg, "|")
	id := parts[0][:strings.LastIndex(parts[0], "@")]
	ip := parts[0][strings.LastIndex(parts[0], "@")+1:]
	model := parts[1][strings.Index(parts[1], ":")+1:]
	sacp := strings.Contains(msg, "SACP:1")

	return &Printer{
		IP:    ip,
		ID:    id,
		Model: model,
		SACP:  sacp,
	}, nil
}

// Discover finds Snapmaker printers on the local network via UDP broadcast on port 20054.
func Discover(timeout time.Duration) ([]*Printer, error) {
	var (
		mu       sync.Mutex
		printers []*Printer
	)

	addrs, err := getBroadcastAddresses()
	if err != nil {
		return nil, err
	}

	var wg sync.WaitGroup
	for _, addr := range addrs {
		wg.Add(1)
		go func(addr string) {
			defer wg.Done()

			broadcastAddr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("%s:%d", addr, 20054))
			if err != nil {
				return
			}

			conn, err := net.ListenUDP("udp4", nil)
			if err != nil {
				return
			}
			defer conn.Close()

			conn.SetDeadline(time.Now().Add(timeout))

			if _, err := conn.WriteTo([]byte("discover"), broadcastAddr); err != nil {
				return
			}

			buf := make([]byte, 1500)
			for {
				n, _, err := conn.ReadFromUDP(buf)
				if err != nil {
					if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
						break
					}
					return
				}

				printer, err := ParsePrinter(buf[:n])
				if err != nil {
					continue
				}

				mu.Lock()
				printers = append(printers, printer)
				mu.Unlock()
			}
		}(addr)
	}
	wg.Wait()

	return printers, nil
}

func getBroadcastAddresses() ([]string, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	addrMap := map[string]bool{}
	for _, iface := range ifs {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			if n, ok := addr.(*net.IPNet); ok && !n.IP.IsLoopback() {
				if v4addr := n.IP.To4(); v4addr != nil {
					baddr := make(net.IP, len(v4addr))
					binary.BigEndian.PutUint32(baddr, binary.BigEndian.Uint32(v4addr)|^binary.BigEndian.Uint32(n.IP.DefaultMask()))
					if s := baddr.String(); !addrMap[s] {
						addrMap[s] = true
					}
				}
			}
		}
	}

	addrs := make([]string, 0, len(addrMap))
	for addr := range addrMap {
		addrs = append(addrs, addr)
	}
	return addrs, nil
}
