package network

import (
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"vkvm/internal/protocol"
)

// UDPSender is the Host-side UDP broadcaster that sends binary input events
// to all registered agents with minimal overhead.
type UDPSender struct {
	conn     *net.UDPConn
	port     int
	agents   map[string]*udpAgent
	agentsMu sync.RWMutex
	seq      uint32 // atomic, monotonically increasing
	done     chan struct{}
}

type udpAgent struct {
	addr     *net.UDPAddr
	lastSeen time.Time
}

// NewUDPSender creates a new UDP sender for the host.
// port should typically match the API port (TCP and UDP can share port numbers).
func NewUDPSender(port int) *UDPSender {
	return &UDPSender{
		port:   port,
		agents: make(map[string]*udpAgent),
		done:   make(chan struct{}),
	}
}

// Start binds the UDP socket and begins listening for agent registrations.
func (s *UDPSender) Start() error {
	addr := &net.UDPAddr{Port: s.port}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return err
	}
	s.conn = conn

	// 1 MB write buffer for burst writes
	conn.SetWriteBuffer(1 << 20)
	// 64 KB read buffer for register/heartbeat
	conn.SetReadBuffer(1 << 16)

	log.Printf("UDP Sender: Listening on :%d", s.port)

	go s.readLoop()
	go s.cleanupLoop()

	return nil
}

// readLoop listens for register and heartbeat packets from agents.
func (s *UDPSender) readLoop() {
	buf := make([]byte, 64)
	for {
		n, remoteAddr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-s.done:
				return
			default:
				continue
			}
		}

		pkt, err := protocol.DecodeUDPPacket(buf[:n])
		if err != nil {
			continue
		}

		switch pkt.Type {
		case protocol.UDPPacketRegister:
			key := remoteAddr.String()
			s.agentsMu.Lock()
			if _, exists := s.agents[key]; !exists {
				log.Printf("UDP Sender: Agent registered from %s", key)
			}
			s.agents[key] = &udpAgent{addr: remoteAddr, lastSeen: time.Now()}
			s.agentsMu.Unlock()

			// Reply with Ack so agent can confirm UDP connectivity
			ack := &protocol.UDPPacket{
				Type:      protocol.UDPPacketAck,
				Timestamp: time.Now().UnixMilli(),
			}
			s.conn.WriteToUDP(protocol.EncodeUDPPacket(ack), remoteAddr)

		case protocol.UDPPacketHeartbeat:
			key := remoteAddr.String()
			s.agentsMu.Lock()
			if _, exists := s.agents[key]; !exists {
				log.Printf("UDP Sender: Agent registered from %s (via heartbeat)", key)
			}
			s.agents[key] = &udpAgent{addr: remoteAddr, lastSeen: time.Now()}
			s.agentsMu.Unlock()
		}
	}
}

// cleanupLoop removes agents that haven't sent a heartbeat recently.
func (s *UDPSender) cleanupLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.agentsMu.Lock()
			for key, agent := range s.agents {
				if time.Since(agent.lastSeen) > 30*time.Second {
					log.Printf("UDP Sender: Removing stale agent %s", key)
					delete(s.agents, key)
				}
			}
			s.agentsMu.Unlock()
		case <-s.done:
			return
		}
	}
}

// SendInput encodes an input event as a binary UDP packet and sends it to all
// registered agents. Critical events (key, mouse button) are sent multiple
// times for redundancy since UDP has no delivery guarantee.
func (s *UDPSender) SendInput(eventType string, deltaX, deltaY int, button int, pressed bool, keyCode uint16, modifiers uint16, wheelDelta int, timestamp int64) {
	seq := atomic.AddUint32(&s.seq, 1)
	redundancy := 1

	pkt := &protocol.UDPPacket{
		Seq:       seq,
		Timestamp: timestamp,
	}

	switch eventType {
	case "mouse_move":
		pkt.Type = protocol.UDPPacketMouseMove
		pkt.DeltaX = int32(deltaX)
		pkt.DeltaY = int32(deltaY)
	case "mouse_btn":
		pkt.Type = protocol.UDPPacketMouseButton
		pkt.Button = uint8(button)
		if pressed {
			pkt.Pressed = 1
		}
		redundancy = 3
	case "mouse_wheel":
		pkt.Type = protocol.UDPPacketMouseScroll
		pkt.WheelDelta = int32(wheelDelta)
		pkt.Axis = 0 // vertical
		redundancy = 2
	case "mouse_wheel_h":
		pkt.Type = protocol.UDPPacketMouseScroll
		pkt.WheelDelta = int32(wheelDelta)
		pkt.Axis = 1 // horizontal
		redundancy = 2
	case "key":
		pkt.Type = protocol.UDPPacketKeyEvent
		pkt.KeyCode = keyCode
		if pressed {
			pkt.Pressed = 1
		}
		pkt.Modifiers = modifiers
		redundancy = 3
	default:
		return
	}

	data := protocol.EncodeUDPPacket(pkt)
	s.broadcast(data, redundancy)
}

// broadcast sends data to all registered agents.
func (s *UDPSender) broadcast(data []byte, redundancy int) {
	s.agentsMu.RLock()
	defer s.agentsMu.RUnlock()

	for _, agent := range s.agents {
		for i := 0; i < redundancy; i++ {
			s.conn.WriteToUDP(data, agent.addr)
		}
	}
}

// HasAgents returns true if at least one agent is registered.
func (s *UDPSender) HasAgents() bool {
	s.agentsMu.RLock()
	defer s.agentsMu.RUnlock()
	return len(s.agents) > 0
}

// Stop shuts down the UDP sender.
func (s *UDPSender) Stop() {
	close(s.done)
	if s.conn != nil {
		s.conn.Close()
	}
}
