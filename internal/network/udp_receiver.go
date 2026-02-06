package network

import (
	"log"
	"net"
	"time"

	"vkvm/internal/protocol"
)

// UDPReceiver is the Agent-side UDP listener that receives binary input events
// from the Host with minimal latency.
type UDPReceiver struct {
	hostAddr string // host address in "ip:port" format
	conn     *net.UDPConn
	done     chan struct{}

	// OnInput is called for each received input event (same signature as WSClient.OnInput).
	OnInput func(eventType string, deltaX, deltaY int, button int, pressed bool, keyCode uint16, modifiers uint16, wheelDelta int, timestamp int64)

	// dedup ring buffer for redundant packets
	dedup seqDedup
}

// seqDedup tracks recently seen sequence numbers to discard redundant packets.
// Uses a fixed-size ring buffer — no allocation, O(1) lookup.
type seqDedup struct {
	ring [512]uint32
	pos  int
	seen map[uint32]struct{}
}

func newSeqDedup() seqDedup {
	return seqDedup{seen: make(map[uint32]struct{}, 512)}
}

func (d *seqDedup) isDuplicate(seq uint32) bool {
	if _, ok := d.seen[seq]; ok {
		return true
	}
	// Evict oldest entry
	old := d.ring[d.pos]
	if old != 0 {
		delete(d.seen, old)
	}
	d.ring[d.pos] = seq
	d.seen[seq] = struct{}{}
	d.pos = (d.pos + 1) % len(d.ring)
	return false
}

// NewUDPReceiver creates a new UDP receiver for the agent.
// hostAddr should be "ip:port" matching the host's API address.
func NewUDPReceiver(hostAddr string) *UDPReceiver {
	return &UDPReceiver{
		hostAddr: hostAddr,
		done:     make(chan struct{}),
		dedup:    newSeqDedup(),
	}
}

// Probe tests whether UDP connectivity to the host is available.
// It sends register packets and waits for an Ack response.
// Returns true if the host replied within the timeout, false otherwise.
func (r *UDPReceiver) Probe() bool {
	hostUDP, err := net.ResolveUDPAddr("udp", r.hostAddr)
	if err != nil {
		log.Printf("UDP Probe: failed to resolve host: %v", err)
		return false
	}

	conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: 0})
	if err != nil {
		log.Printf("UDP Probe: failed to bind: %v", err)
		return false
	}

	// Try up to 3 times with 500ms timeout each (total max ~1.5s)
	buf := make([]byte, 64)
	for attempt := 0; attempt < 3; attempt++ {
		// Send register
		pkt := &protocol.UDPPacket{
			Type:      protocol.UDPPacketRegister,
			Timestamp: time.Now().UnixMilli(),
		}
		conn.WriteToUDP(protocol.EncodeUDPPacket(pkt), hostUDP)

		// Wait for ack
		conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			continue // timeout or error, retry
		}
		resp, err := protocol.DecodeUDPPacket(buf[:n])
		if err != nil {
			continue
		}
		if resp.Type == protocol.UDPPacketAck {
			conn.Close()
			log.Printf("UDP Probe: host replied with Ack (attempt %d), UDP path is open", attempt+1)
			return true
		}
	}

	conn.Close()
	log.Printf("UDP Probe: no Ack received after 3 attempts, UDP path blocked")
	return false
}

// Start opens a UDP socket, registers with the host, and begins receiving.
// Call Probe() first to verify UDP connectivity before calling Start().
func (r *UDPReceiver) Start() error {
	// Resolve host UDP address
	hostUDP, err := net.ResolveUDPAddr("udp", r.hostAddr)
	if err != nil {
		return err
	}

	// Bind to any available local port
	conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: 0})
	if err != nil {
		return err
	}
	r.conn = conn

	// Large read buffer for burst receives
	conn.SetReadBuffer(1 << 20) // 1 MB

	localAddr := conn.LocalAddr().(*net.UDPAddr)
	log.Printf("UDP Receiver: Listening on :%d, host=%s", localAddr.Port, r.hostAddr)

	// Send initial register
	r.sendControl(protocol.UDPPacketRegister, hostUDP)

	// Periodic heartbeat
	go r.heartbeatLoop(hostUDP)

	// Main receive loop
	go r.readLoop()

	return nil
}

// heartbeatLoop sends periodic heartbeat packets to keep the registration alive.
func (r *UDPReceiver) heartbeatLoop(hostAddr *net.UDPAddr) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			r.sendControl(protocol.UDPPacketHeartbeat, hostAddr)
		case <-r.done:
			return
		}
	}
}

// sendControl sends a register or heartbeat packet (header-only, no payload).
func (r *UDPReceiver) sendControl(pktType uint8, addr *net.UDPAddr) {
	pkt := &protocol.UDPPacket{
		Type:      pktType,
		Timestamp: time.Now().UnixMilli(),
	}
	data := protocol.EncodeUDPPacket(pkt)
	r.conn.WriteToUDP(data, addr)
}

// readLoop reads and dispatches incoming binary input packets.
func (r *UDPReceiver) readLoop() {
	buf := make([]byte, 64)
	for {
		r.conn.SetReadDeadline(time.Time{}) // clear any deadline from probe
		n, _, err := r.conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-r.done:
				return
			default:
				continue
			}
		}

		pkt, err := protocol.DecodeUDPPacket(buf[:n])
		if err != nil {
			continue
		}

		// Deduplicate redundant packets (same seq number)
		if pkt.Type != protocol.UDPPacketRegister && pkt.Type != protocol.UDPPacketHeartbeat {
			if r.dedup.isDuplicate(pkt.Seq) {
				continue
			}
		}

		r.dispatch(pkt)
	}
}

// dispatch converts a binary packet back to the callback parameters.
func (r *UDPReceiver) dispatch(pkt *protocol.UDPPacket) {
	if r.OnInput == nil {
		return
	}

	switch pkt.Type {
	case protocol.UDPPacketMouseMove:
		r.OnInput("mouse_move", int(pkt.DeltaX), int(pkt.DeltaY), 0, false, 0, 0, 0, pkt.Timestamp)

	case protocol.UDPPacketMouseButton:
		r.OnInput("mouse_btn", 0, 0, int(pkt.Button), pkt.Pressed == 1, 0, 0, 0, pkt.Timestamp)

	case protocol.UDPPacketMouseScroll:
		eventType := "mouse_wheel"
		if pkt.Axis == 1 {
			eventType = "mouse_wheel_h"
		}
		r.OnInput(eventType, 0, 0, 0, false, 0, 0, int(pkt.WheelDelta), pkt.Timestamp)

	case protocol.UDPPacketKeyEvent:
		r.OnInput("key", 0, 0, 0, pkt.Pressed == 1, pkt.KeyCode, pkt.Modifiers, 0, pkt.Timestamp)
	}
}

// Stop shuts down the UDP receiver.
func (r *UDPReceiver) Stop() {
	close(r.done)
	if r.conn != nil {
		r.conn.Close()
	}
}
