package network

import (
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"vkvm/internal/input"
	"vkvm/internal/protocol"
	"vkvm/internal/security"
)

// UDPSender is the Host-side UDP broadcaster that sends binary input events
// to all registered agents with minimal overhead.
type UDPSender struct {
	conn           *net.UDPConn
	port           int
	key            []byte
	keyMu          sync.RWMutex
	keyRing        *security.KeyRing
	agents         map[string]*udpAgent
	agentsMu       sync.RWMutex
	seq            uint32 // atomic, monotonically increasing
	done           chan struct{}
	stopOnce       sync.Once
	deviceID       string
	sessionID      string
	capabilities   uint32
	validateDevice func(deviceID string) bool
	pendingMu      sync.Mutex
	pending        map[string]map[uint32]*pendingPacket
	stateProvider  func() input.StateSnapshot
	trace          func(PacketTraceEvent)
}

type udpAgent struct {
	addr            *net.UDPAddr
	lastSeen        time.Time
	deviceID        string
	sessionID       string
	protocolVersion uint16
	capabilities    uint32
}

// TransportHealth is a read-only per-peer transport snapshot used by routing
// and diagnostics. A peer is healthy only while its authenticated heartbeat is
// inside the registration lease and it explicitly advertises UDP support.
type TransportHealth struct {
	DeviceID     string    `json:"device_id"`
	Endpoint     string    `json:"endpoint"`
	SessionID    string    `json:"session_id"`
	Capabilities uint32    `json:"capabilities"`
	LastSeen     time.Time `json:"last_seen"`
	Healthy      bool      `json:"healthy"`
}

type pendingPacket struct {
	data     []byte
	addr     *net.UDPAddr
	lastSend time.Time
	attempts int
}

// NewUDPSender creates a new UDP sender for the host.
// port should typically match the API port (TCP and UDP can share port numbers).

// NewUDPSender creates a sender. The optional token argument keeps source
// compatibility for callers that only construct the object; Start refuses to
// run without a non-empty key.
func NewUDPSender(port int, token ...string) *UDPSender {
	var key []byte
	if len(token) > 0 {
		key = []byte(token[0])
	}
	return &UDPSender{
		port:    port,
		key:     key,
		keyRing: security.NewKeyRing(string(key), 30*time.Second),
		agents:  make(map[string]*udpAgent),
		done:    make(chan struct{}),
		pending: make(map[string]map[uint32]*pendingPacket),
	}
}

// SetIdentity configures the host identity advertised to Agents during UDP
// registration. The identity is informational; authorization is performed by
// the token and the optional device validator.
func (s *UDPSender) SetIdentity(deviceID, sessionID string, capabilities uint32) {
	s.agentsMu.Lock()
	s.deviceID, s.sessionID, s.capabilities = deviceID, sessionID, capabilities
	s.agentsMu.Unlock()
}

// SetDeviceValidator installs the pairing policy for UDP registrations. A nil
// validator accepts any token-authenticated identity (legacy mode).
func (s *UDPSender) SetDeviceValidator(validator func(deviceID string) bool) {
	s.agentsMu.Lock()
	s.validateDevice = validator
	s.agentsMu.Unlock()
}

func (s *UDPSender) SetStateSnapshotProvider(provider func() input.StateSnapshot) {
	s.agentsMu.Lock()
	s.stateProvider = provider
	s.agentsMu.Unlock()
}

func (s *UDPSender) SetPacketTrace(trace func(PacketTraceEvent)) {
	s.agentsMu.Lock()
	s.trace = trace
	s.agentsMu.Unlock()
}

func (s *UDPSender) emitPacketTrace(event PacketTraceEvent) {
	s.agentsMu.RLock()
	trace := s.trace
	s.agentsMu.RUnlock()
	if trace != nil {
		trace(event)
	}
}

// Start binds the UDP socket and begins listening for agent registrations.
func (s *UDPSender) Start() error {
	if len(s.authKey()) == 0 {
		return fmt.Errorf("UDP sender requires a non-empty authentication token")
	}
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
	go s.retransmitLoop()

	return nil
}

// readLoop listens for register and heartbeat packets from agents.
func (s *UDPSender) readLoop() {
	buf := make([]byte, 512)
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

		pkt, err := protocol.DecodeAuthenticatedUDPPacketWithKeys(buf[:n], s.authKeysForRemote(remoteAddr))
		if err != nil {
			continue
		}
		s.emitPacketTrace(PacketTraceEvent{At: time.Now().UTC(), Direction: "rx", Type: pkt.Type, Sequence: pkt.Seq, Peer: remoteAddr.String(), DeviceID: pkt.DeviceID, SessionID: pkt.SessionID})

		switch pkt.Type {
		case protocol.UDPPacketInputAck:
			s.acknowledge(remoteAddr, pkt.AckSeq)
		case protocol.UDPPacketRegister:
			if !s.acceptDevice(pkt.DeviceID) {
				log.Printf("UDP Sender: rejecting unpaired device %q from %s", pkt.DeviceID, remoteAddr)
				continue
			}
			key := remoteAddr.String()
			s.agentsMu.Lock()
			_, exists := s.agents[key]
			if !exists {
				log.Printf("UDP Sender: Agent registered from %s", key)
			}
			s.agents[key] = &udpAgent{addr: remoteAddr, lastSeen: time.Now(), deviceID: pkt.DeviceID, sessionID: pkt.SessionID, protocolVersion: pkt.ProtocolVersion, capabilities: pkt.Capabilities}
			s.agentsMu.Unlock()
			// Reply with Ack so agent can confirm UDP connectivity
			ack := &protocol.UDPPacket{
				Type:            protocol.UDPPacketAck,
				Timestamp:       time.Now().UnixMilli(),
				DeviceID:        s.hostDeviceID(),
				SessionID:       s.hostSessionID(),
				ProtocolVersion: protocol.ProtocolVersion,
				Capabilities:    s.hostCapabilities(),
			}
			_, _ = s.conn.WriteToUDP(protocol.EncodeAuthenticatedUDPPacket(ack, s.authKey()), remoteAddr)
			// The snapshot is deliberately sent after the ACK. The Agent derives
			// the per-session key only after processing that ACK; a registration
			// retry from the Agent covers UDP reordering of the first pair.
			s.sendStateSnapshot(remoteAddr)

		case protocol.UDPPacketHeartbeat:
			if !s.acceptDevice(pkt.DeviceID) {
				continue
			}
			key := remoteAddr.String()
			s.agentsMu.Lock()
			existing, exists := s.agents[key]
			if !exists {
				log.Printf("UDP Sender: Agent registered from %s (via heartbeat)", key)
			}
			s.agents[key] = &udpAgent{addr: remoteAddr, lastSeen: time.Now(), deviceID: pkt.DeviceID, sessionID: pkt.SessionID, protocolVersion: pkt.ProtocolVersion, capabilities: pkt.Capabilities}
			s.agentsMu.Unlock()
			ack := &protocol.UDPPacket{
				Type:            protocol.UDPPacketAck,
				Timestamp:       time.Now().UnixMilli(),
				DeviceID:        s.hostDeviceID(),
				SessionID:       s.hostSessionID(),
				ProtocolVersion: protocol.ProtocolVersion,
				Capabilities:    s.hostCapabilities(),
			}
			_, _ = s.conn.WriteToUDP(protocol.EncodeAuthenticatedUDPPacket(ack, s.authKey()), remoteAddr)
			// The Agent derives its session key from this ACK. Send a snapshot
			// only after the ACK so a fresh heartbeat session cannot drop the
			// recovery state during reconnect.
			if !exists || existing.sessionID != pkt.SessionID {
				s.sendStateSnapshot(remoteAddr)
			}
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
	s.SendInputEvent(input.InputEvent{Type: eventType, DeltaX: deltaX, DeltaY: deltaY, Button: button, Pressed: pressed, KeyCode: keyCode, Modifiers: modifiers, WheelDelta: wheelDelta, Timestamp: timestamp})
}

// SendInputEvent is the metadata-preserving input transport API.
func (s *UDPSender) SendInputEvent(event input.InputEvent) {
	seq := atomic.AddUint32(&s.seq, 1)
	redundancy := 1

	pkt := &protocol.UDPPacket{
		Seq:       seq,
		Timestamp: event.Timestamp,
		DeviceTag: protocol.HardwareTag(event.HardwareID),
	}

	switch event.Type {
	case "mouse_move":
		pkt.Type = protocol.UDPPacketMouseMove
		pkt.DeltaX = int32(event.DeltaX)
		pkt.DeltaY = int32(event.DeltaY)
	case "mouse_btn":
		pkt.Type = protocol.UDPPacketMouseButton
		pkt.Button = uint8(event.Button)
		if event.Pressed {
			pkt.Pressed = 1
		}
		redundancy = 3
	case "mouse_wheel":
		pkt.Type = protocol.UDPPacketMouseScroll
		pkt.WheelDelta = int32(event.WheelDelta)
		pkt.Axis = 0 // vertical
		redundancy = 2
	case "mouse_wheel_h":
		pkt.Type = protocol.UDPPacketMouseScroll
		pkt.WheelDelta = int32(event.WheelDelta)
		pkt.Axis = 1 // horizontal
		redundancy = 2
	case "key":
		pkt.Type = protocol.UDPPacketKeyEvent
		pkt.KeyCode = event.KeyCode
		pkt.ScanCode = event.ScanCode
		if event.Pressed {
			pkt.Pressed = 1
		}
		pkt.Modifiers = event.Modifiers
		if event.Extended {
			pkt.KeyFlags |= 0x01
		}
		if event.E1 {
			pkt.KeyFlags |= 0x02
		}
		if event.Repeat {
			pkt.KeyFlags |= 0x04
		}
		redundancy = 3
	default:
		return
	}

	s.broadcastPacket(pkt, pkt.Seq, isReliablePacket(pkt.Type), redundancy)
}

// SendReleaseAll tells every authenticated Agent to release any remote input
// state. It is used on capture disable, transport loss and shutdown.
func (s *UDPSender) SendReleaseAll() {
	pkt := &protocol.UDPPacket{
		Type:      protocol.UDPPacketReleaseAll,
		Seq:       atomic.AddUint32(&s.seq, 1),
		Timestamp: time.Now().UnixMilli(),
	}
	s.broadcastPacket(pkt, pkt.Seq, true, 3)
}

// SetKey rotates the shared transport key for future control and input
// packets. Existing agents must re-register/heartbeat with the new key.
func (s *UDPSender) SetKey(key string) {
	if s.keyRing != nil {
		s.keyRing.Set(key)
	}
	s.keyMu.Lock()
	s.key = append(s.key[:0], []byte(key)...)
	s.keyMu.Unlock()
}

func (s *UDPSender) authKey() []byte {
	if s.keyRing != nil {
		keys := s.keyRing.Keys()
		if len(keys) > 0 {
			return keys[0]
		}
	}
	s.keyMu.RLock()
	defer s.keyMu.RUnlock()
	return append([]byte(nil), s.key...)
}

func (s *UDPSender) authKeys() [][]byte {
	if s.keyRing != nil {
		return s.keyRing.Keys()
	}
	key := s.authKey()
	if len(key) == 0 {
		return nil
	}
	return [][]byte{key}
}

func isReliablePacket(packetType uint8) bool {
	return packetType == protocol.UDPPacketMouseButton || packetType == protocol.UDPPacketKeyEvent || packetType == protocol.UDPPacketReleaseAll || packetType == protocol.UDPPacketStateSnapshot
}

func (s *UDPSender) sendStateSnapshot(addr *net.UDPAddr) {
	s.agentsMu.RLock()
	provider := s.stateProvider
	s.agentsMu.RUnlock()
	if provider == nil || addr == nil || s.conn == nil {
		return
	}
	snapshot := provider()
	keys := make([]protocol.KeyState, 0, len(snapshot.Keys))
	for _, key := range snapshot.Keys {
		keys = append(keys, protocol.KeyState{KeyCode: key.KeyCode, ScanCode: key.ScanCode, Extended: key.Extended, E1: key.E1})
	}
	buttons := make([]uint8, 0, len(snapshot.Buttons))
	for _, button := range snapshot.Buttons {
		if button >= 0 && button <= 255 {
			buttons = append(buttons, uint8(button))
		}
	}
	pkt := &protocol.UDPPacket{
		Type: protocol.UDPPacketStateSnapshot, Seq: atomic.AddUint32(&s.seq, 1),
		Timestamp: time.Now().UnixMilli(), StateModifiers: snapshot.Modifiers,
		StateKeys: keys, StateButtons: buttons,
	}
	s.agentsMu.RLock()
	agent := s.agents[addr.String()]
	if agent != nil {
		agent = &udpAgent{deviceID: agent.deviceID, sessionID: agent.sessionID}
	}
	s.agentsMu.RUnlock()
	data := protocol.EncodeAuthenticatedUDPPacket(pkt, s.agentKey(agent))
	key := addr.String()
	s.pendingMu.Lock()
	if s.pending[key] == nil {
		s.pending[key] = make(map[uint32]*pendingPacket)
	}
	s.pending[key][pkt.Seq] = &pendingPacket{data: append([]byte(nil), data...), addr: addr, lastSend: time.Now(), attempts: 1}
	s.pendingMu.Unlock()
	for i := 0; i < 3; i++ {
		_, _ = s.conn.WriteToUDP(data, addr)
	}
}

func (s *UDPSender) broadcastPacket(pkt *protocol.UDPPacket, seq uint32, reliable bool, redundancy int) {
	s.agentsMu.RLock()
	if s.conn == nil {
		s.agentsMu.RUnlock()
		return
	}
	agents := make(map[string]*udpAgent, len(s.agents))
	for key, agent := range s.agents {
		agents[key] = agent
	}
	s.agentsMu.RUnlock()

	for key, agent := range agents {
		data := protocol.EncodeAuthenticatedUDPPacket(pkt, s.agentKey(agent))
		if reliable && seq != 0 {
			s.pendingMu.Lock()
			if s.pending[key] == nil {
				s.pending[key] = make(map[uint32]*pendingPacket)
			}
			s.pending[key][seq] = &pendingPacket{data: append([]byte(nil), data...), addr: agent.addr, lastSend: time.Now(), attempts: 1}
			s.pendingMu.Unlock()
		}
		for i := 0; i < redundancy; i++ {
			_, _ = s.conn.WriteToUDP(data, agent.addr)
		}
	}
}

func (s *UDPSender) agentKey(agent *udpAgent) []byte {
	keys := s.authKeys()
	if agent == nil || agent.deviceID == "" || agent.sessionID == "" || len(keys) == 0 {
		return s.authKey()
	}
	return protocol.DeriveSessionKey(string(keys[0]), agent.deviceID, agent.sessionID)
}

func (s *UDPSender) authKeysForRemote(remote *net.UDPAddr) [][]byte {
	keys := s.authKeys()
	if remote == nil {
		return keys
	}
	var agent *udpAgent
	s.agentsMu.RLock()
	if value := s.agents[remote.String()]; value != nil {
		agent = &udpAgent{deviceID: value.deviceID, sessionID: value.sessionID}
	}
	s.agentsMu.RUnlock()
	if agent == nil || agent.deviceID == "" || agent.sessionID == "" {
		return keys
	}
	for _, key := range keys {
		keys = append(keys, protocol.DeriveSessionKey(string(key), agent.deviceID, agent.sessionID))
	}
	return keys
}

func (s *UDPSender) acknowledge(addr *net.UDPAddr, seq uint32) {
	if seq == 0 || addr == nil {
		return
	}
	key := addr.String()
	s.pendingMu.Lock()
	if byAgent := s.pending[key]; byAgent != nil {
		delete(byAgent, seq)
		if len(byAgent) == 0 {
			delete(s.pending, key)
		}
	}
	s.pendingMu.Unlock()
}

func (s *UDPSender) retransmitLoop() {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			s.pendingMu.Lock()
			for agentKey, packets := range s.pending {
				for seq, pending := range packets {
					if now.Sub(pending.lastSend) < 20*time.Millisecond {
						continue
					}
					if pending.attempts >= 6 {
						// The heartbeat monitor will decide whether the peer is
						// stale; stop retrying this specific event to avoid an
						// unbounded queue.
						delete(packets, seq)
						continue
					}
					if s.conn != nil {
						_, _ = s.conn.WriteToUDP(pending.data, pending.addr)
					}
					pending.lastSend = now
					pending.attempts++
				}
				if len(packets) == 0 {
					delete(s.pending, agentKey)
				}
			}
			s.pendingMu.Unlock()
		case <-s.done:
			return
		}
	}
}

func (s *UDPSender) acceptDevice(deviceID string) bool {
	s.agentsMu.RLock()
	validator := s.validateDevice
	s.agentsMu.RUnlock()
	return validator == nil || validator(deviceID)
}

func (s *UDPSender) hostDeviceID() string {
	s.agentsMu.RLock()
	defer s.agentsMu.RUnlock()
	return s.deviceID
}

func (s *UDPSender) hostSessionID() string {
	s.agentsMu.RLock()
	defer s.agentsMu.RUnlock()
	return s.sessionID
}

func (s *UDPSender) hostCapabilities() uint32 {
	s.agentsMu.RLock()
	defer s.agentsMu.RUnlock()
	return s.capabilities | protocol.CapabilityUDP
}

// HasAgents returns true if at least one agent is registered.
func (s *UDPSender) HasAgents() bool {
	s.agentsMu.RLock()
	defer s.agentsMu.RUnlock()
	return len(s.agents) > 0
}

// LocalPort returns the bound UDP port. It is primarily useful for portable
// loopback integration tests and for embedders that request an ephemeral port.
func (s *UDPSender) LocalPort() int {
	if s == nil || s.conn == nil {
		return 0
	}
	if addr, ok := s.conn.LocalAddr().(*net.UDPAddr); ok {
		return addr.Port
	}
	return 0
}

func (s *UDPSender) HasDevice(deviceID string) bool {
	if deviceID == "" {
		return false
	}
	s.agentsMu.RLock()
	defer s.agentsMu.RUnlock()
	for _, agent := range s.agents {
		if agent.deviceID == deviceID && agent.capabilities&protocol.CapabilityUDP != 0 && time.Since(agent.lastSeen) <= 15*time.Second {
			return true
		}
	}
	return false
}

// TransportSnapshot returns one entry per registered UDP endpoint, preserving
// separate health for agents that share a process but use different paths.
func (s *UDPSender) TransportSnapshot() []TransportHealth {
	s.agentsMu.RLock()
	defer s.agentsMu.RUnlock()
	out := make([]TransportHealth, 0, len(s.agents))
	for _, agent := range s.agents {
		lastSeen := agent.lastSeen
		endpoint := ""
		if agent.addr != nil {
			endpoint = agent.addr.String()
		}
		out = append(out, TransportHealth{
			DeviceID: agent.deviceID, Endpoint: endpoint, SessionID: agent.sessionID,
			Capabilities: agent.capabilities, LastSeen: lastSeen,
			Healthy: agent.capabilities&protocol.CapabilityUDP != 0 && time.Since(lastSeen) <= 15*time.Second,
		})
	}
	return out
}

// Stop shuts down the UDP sender.
func (s *UDPSender) Stop() {
	s.stopOnce.Do(func() {
		close(s.done)
		if s.conn != nil {
			s.conn.Close()
		}
	})
}
