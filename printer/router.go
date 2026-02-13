package printer

import (
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/john/snapmaker_moonraker/sacp"
)

// SubscriptionHandler is called when subscription data arrives from the printer.
type SubscriptionHandler func(commandSet, commandID byte, data []byte)

// PacketRouter reads all incoming SACP packets from the printer connection
// and routes them: command responses go to waiting callers, subscription
// data goes to the subscription handler.
type PacketRouter struct {
	conn           net.Conn
	mu             sync.Mutex
	pending        map[uint16]chan *sacp.Packet
	onSubscription SubscriptionHandler
	onDisconnect   func()
	stopped        int32
	done           chan struct{}
}

// NewPacketRouter creates a new router for the given connection.
func NewPacketRouter(conn net.Conn, subHandler SubscriptionHandler, disconnectHandler func()) *PacketRouter {
	return &PacketRouter{
		conn:           conn,
		pending:        make(map[uint16]chan *sacp.Packet),
		onSubscription: subHandler,
		onDisconnect:   disconnectHandler,
		done:           make(chan struct{}),
	}
}

// Start begins the background read loop.
func (r *PacketRouter) Start() {
	go r.readLoop()
}

// Stop cleanly shuts down the read loop. Blocks until the loop exits.
func (r *PacketRouter) Stop() {
	atomic.StoreInt32(&r.stopped, 1)
	<-r.done
}

// Done returns a channel that is closed when the read loop exits.
func (r *PacketRouter) Done() <-chan struct{} {
	return r.done
}

func (r *PacketRouter) readLoop() {
	defer close(r.done)
	defer func() {
		// Drain all pending response channels.
		r.mu.Lock()
		for seq, ch := range r.pending {
			close(ch)
			delete(r.pending, seq)
		}
		r.mu.Unlock()
	}()

	for {
		if atomic.LoadInt32(&r.stopped) != 0 {
			return
		}

		p, err := sacp.Read(r.conn, 5*time.Second)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				// Timeout is normal - check stopped flag and retry.
				continue
			}
			// Real read error - connection is broken.
			if atomic.LoadInt32(&r.stopped) == 0 {
				log.Printf("PacketRouter: read error: %v", err)
				if r.onDisconnect != nil {
					r.onDisconnect()
				}
			}
			return
		}

		// Check if this is a response to a pending command.
		r.mu.Lock()
		ch, isPending := r.pending[p.Sequence]
		if isPending {
			delete(r.pending, p.Sequence)
		}
		r.mu.Unlock()

		if isPending {
			ch <- p
			continue
		}

		// Not a pending command response - subscription data or unsolicited packet.
		if r.onSubscription != nil {
			r.onSubscription(p.CommandSet, p.CommandID, p.Data)
		}
	}
}

// WaitForResponse registers for a response with the given sequence number
// and blocks until it arrives or times out.
func (r *PacketRouter) WaitForResponse(seq uint16, timeout time.Duration) (*sacp.Packet, error) {
	ch := make(chan *sacp.Packet, 1)
	r.mu.Lock()
	r.pending[seq] = ch
	r.mu.Unlock()

	select {
	case p, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("connection closed while waiting for response")
		}
		return p, nil
	case <-time.After(timeout):
		r.mu.Lock()
		delete(r.pending, seq)
		r.mu.Unlock()
		return nil, fmt.Errorf("timeout waiting for response seq=%d", seq)
	}
}
