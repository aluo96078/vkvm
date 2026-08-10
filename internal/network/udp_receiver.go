package network

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"vkvm/internal/protocol"
	"vkvm/internal/security"
)

// UDPReceiver is the Agent-side UDP listener that receives binary input events
// from the Host with minimal latency.
type UDPReceiver struct {
	hostAddr       string // host address in "ip:port" format
	key            []byte
	keyMu          sync.RWMutex
	keyRing        *security.KeyRing
	hostIP         net.IP
	hostPort       int
	hostUDP        *net.UDPAddr
	localPort      int
	deviceID       string
	sessionID      string
	capabilities   uint32
	peerDeviceID   string
	peerSessionID  string
	expectedPeerID string
	peerKeys       [][]byte
	conn           *net.UDPConn
	done           chan struct{}
	stopOnce       sync.Once
	ready          chan struct{}
	readyOnce      sync.Once
	lastAck        int64
	lostOnce       sync.Once
	traceMu        sync.RWMutex
	trace          func(PacketTraceEvent)

	// OnInput is called for each received input event (same signature as WSClient.OnInput).
	OnInput      func(eventType string, deltaX, deltaY int, button int, pressed bool, keyCode uint16, modifiers uint16, wheelDelta int, timestamp int64)
	OnInputEvent func(protocol.InputPayload)
	OnInputState func(protocol.InputStatePayload)
	OnReleaseAll func()
	// OnTransportLost is called once when heartbeats from the host stop. A
	// caller can enable its WebSocket callback as a safe fallback.
	OnTransportLost func()

	// dedup ring buffer for redundant packets
	dedup seqDedup
}

// seqDedup tracks recently seen sequence numbers to discard redundant packets.
// Uses a fixed-size ring buffer — no allocation, O(1) lookup.
type seqDedup struct {
	ring [512]uint32
	pos  int
	seen map[uint32]struct{}
	max  uint32
}

func newSeqDedup() seqDedup {
	return seqDedup{seen: make(map[uint32]struct{}, 512)}
}

func (d *seqDedup) isDuplicate(seq uint32) bool {
	if seq == 0 {
		return true
	}
	// Reject packets that are older than the replay window even if their
	// sequence number has already been evicted from the ring.
	if d.max != 0 && d.max > seq && d.max-seq >= uint32(len(d.ring)) {
		return true
	}
	// Input injection is stateful: delivering an older key/button transition
	// after a newer one is worse than dropping a stale datagram. Keep a strict
	// monotonic receive frontier within the replay window.
	if d.max != 0 && d.max > seq {
		return true
	}
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
	if seq > d.max {
		d.max = seq
	}
	d.pos = (d.pos + 1) % len(d.ring)
	return false
}

// NewUDPReceiver creates a new UDP receiver for the agent.
// hostAddr should be "ip:port" matching the host's API address.
func NewUDPReceiver(hostAddr string, token ...string) *UDPReceiver {
	var key []byte
	if len(token) > 0 {
		key = []byte(token[0])
	}
	deviceID := ""
	if len(token) > 1 {
		deviceID = token[1]
	}
	return &UDPReceiver{
		hostAddr:     hostAddr,
		key:          key,
		keyRing:      security.NewKeyRing(string(key), 30*time.Second),
		deviceID:     deviceID,
		sessionID:    newUDPSessionID(),
		capabilities: protocol.CapabilityInputMetadata | protocol.CapabilityReliableControl | protocol.CapabilityStateSnapshot | protocol.CapabilityUDP,
		done:         make(chan struct{}),
		ready:        make(chan struct{}),
		dedup:        newSeqDedup(),
	}
}

func newUDPSessionID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("udp-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

// SetIdentity updates the persistent device identity used by future control
// packets. It is safe to call before Start or when a connection is restarted.
func (r *UDPReceiver) SetIdentity(deviceID string) {
	r.keyMu.Lock()
	r.deviceID = deviceID
	r.keyMu.Unlock()
}

// SetExpectedPeerDeviceID pins the Host identity learned during pairing. The
// UDP source address and token remain required; this adds identity binding so
// another token-authenticated peer cannot inject on the same path.
func (r *UDPReceiver) SetExpectedPeerDeviceID(deviceID string) {
	r.keyMu.Lock()
	r.expectedPeerID = deviceID
	r.keyMu.Unlock()
}

func (r *UDPReceiver) SetPacketTrace(trace func(PacketTraceEvent)) {
	r.traceMu.Lock()
	r.trace = trace
	r.traceMu.Unlock()
}

func (r *UDPReceiver) emitPacketTrace(event PacketTraceEvent) {
	r.traceMu.RLock()
	trace := r.trace
	r.traceMu.RUnlock()
	if trace != nil {
		trace(event)
	}
}

// Probe tests whether UDP connectivity to the host is available.
// It sends register packets and waits for an Ack response.
// Returns true if the host replied within the timeout, false otherwise.
func (r *UDPReceiver) Probe() bool {
	if len(r.authKey()) == 0 {
		log.Printf("UDP Probe: no authentication token configured")
		return false
	}
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
	r.localPort = conn.LocalAddr().(*net.UDPAddr).Port

	// Try up to 3 times with 500ms timeout each (total max ~1.5s)
	buf := make([]byte, 512)
	for attempt := 0; attempt < 3; attempt++ {
		// Send register
		pkt := r.controlPacket(protocol.UDPPacketRegister)
		conn.WriteToUDP(protocol.EncodeAuthenticatedUDPPacket(pkt, r.authKey()), hostUDP)

		// Wait for ack
		conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			continue // timeout or error, retry
		}
		resp, err := protocol.DecodeAuthenticatedUDPPacketWithKeys(buf[:n], r.authKeys())
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
	if len(r.authKey()) == 0 {
		return fmt.Errorf("UDP receiver requires a non-empty authentication token")
	}
	// Resolve host UDP address
	hostUDP, err := net.ResolveUDPAddr("udp", r.hostAddr)
	if err != nil {
		return err
	}
	r.hostIP = append(net.IP(nil), hostUDP.IP...)
	r.hostPort = hostUDP.Port
	r.hostUDP = hostUDP

	// Bind to any available local port
	conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: r.localPort})
	if err != nil && r.localPort != 0 {
		// Another process may have claimed the probe port between Probe and
		// Start; use a fresh port and re-register it with the host.
		conn, err = net.ListenUDP("udp", &net.UDPAddr{Port: 0})
	}
	if err != nil {
		return err
	}
	r.conn = conn
	atomic.StoreInt64(&r.lastAck, time.Now().UnixMilli())
	r.readyOnce = sync.Once{}
	r.ready = make(chan struct{})
	r.lostOnce = sync.Once{}

	// Large read buffer for burst receives
	conn.SetReadBuffer(1 << 20) // 1 MB

	localAddr := conn.LocalAddr().(*net.UDPAddr)
	log.Printf("UDP Receiver: Listening on :%d, host=%s", localAddr.Port, r.hostAddr)

	// Periodic heartbeat
	go r.heartbeatLoop(hostUDP)
	go r.healthLoop()

	// Main receive loop
	go r.readLoop()

	// Send the initial registration only after the reader is running. Do not
	// report a usable UDP transport until the authenticated Host ACK has been
	// received and the per-session key has been derived; otherwise the first
	// input packet can be sent with a key the Agent has not learned yet.
	r.sendControl(protocol.UDPPacketRegister, hostUDP)
	select {
	case <-r.ready:
		return nil
	case <-time.After(1200 * time.Millisecond):
		r.Stop()
		return fmt.Errorf("UDP receiver handshake timed out")
	case <-r.done:
		return fmt.Errorf("UDP receiver stopped during handshake")
	}
}

func (r *UDPReceiver) healthLoop() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if time.Since(time.UnixMilli(atomic.LoadInt64(&r.lastAck))) <= 15*time.Second {
				continue
			}
			r.lostOnce.Do(func() {
				log.Printf("UDP Receiver: host heartbeat timed out; transport is unhealthy")
				if r.OnTransportLost != nil {
					r.OnTransportLost()
				}
			})
		case <-r.done:
			return
		}
	}
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
	pkt := r.controlPacket(pktType)
	data := protocol.EncodeAuthenticatedUDPPacket(pkt, r.authKey())
	r.conn.WriteToUDP(data, addr)
}

func (r *UDPReceiver) controlPacket(pktType uint8) *protocol.UDPPacket {
	r.keyMu.RLock()
	deviceID, sessionID, capabilities := r.deviceID, r.sessionID, r.capabilities
	r.keyMu.RUnlock()
	return &protocol.UDPPacket{
		Type:            pktType,
		Timestamp:       time.Now().UnixMilli(),
		DeviceID:        deviceID,
		SessionID:       sessionID,
		ProtocolVersion: protocol.ProtocolVersion,
		Capabilities:    capabilities,
	}
}

// readLoop reads and dispatches incoming binary input packets.
func (r *UDPReceiver) readLoop() {
	buf := make([]byte, 512)
	for {
		r.conn.SetReadDeadline(time.Time{}) // clear any deadline from probe
		n, remoteAddr, err := r.conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-r.done:
				return
			default:
				continue
			}
		}
		if r.hostIP != nil && (!remoteAddr.IP.Equal(r.hostIP) || (r.hostPort != 0 && remoteAddr.Port != r.hostPort)) {
			continue
		}

		pkt, err := protocol.DecodeAuthenticatedUDPPacketWithKeys(buf[:n], r.authKeys())
		if err != nil {
			continue
		}
		r.emitPacketTrace(PacketTraceEvent{At: time.Now().UTC(), Direction: "rx", Type: pkt.Type, Sequence: pkt.Seq, Peer: remoteAddr.String(), DeviceID: pkt.DeviceID, SessionID: pkt.SessionID})

		if pkt.Type == protocol.UDPPacketAck {
			r.keyMu.RLock()
			expectedPeerID := r.expectedPeerID
			r.keyMu.RUnlock()
			if expectedPeerID != "" && pkt.DeviceID != expectedPeerID {
				continue
			}
			atomic.StoreInt64(&r.lastAck, time.Now().UnixMilli())
			if pkt.DeviceID != "" && pkt.SessionID != "" {
				r.keyMu.Lock()
				if r.peerSessionID != pkt.SessionID {
					r.peerDeviceID = pkt.DeviceID
					r.peerSessionID = pkt.SessionID
					r.dedup = newSeqDedup()
				}
				localDeviceID, localSessionID := r.deviceID, r.sessionID
				if localDeviceID != "" && localSessionID != "" {
					r.peerKeys = make([][]byte, 0, len(r.keyRing.Keys()))
					for _, tokenKey := range r.keyRing.Keys() {
						r.peerKeys = append(r.peerKeys, protocol.DeriveSessionKey(string(tokenKey), localDeviceID, localSessionID))
					}
				}
				r.keyMu.Unlock()
			}
			// ready is closed only after identity checks and session-key setup,
			// so callers can safely send the first authenticated input packet.
			readySignal := false
			r.readyOnce.Do(func() {
				close(r.ready)
				readySignal = true
			})
			if readySignal {
				// Ask the Host to replay the snapshot after this Agent has
				// installed the derived session key. This closes the UDP
				// reordering window between the first ACK and snapshot.
				go r.sendControl(protocol.UDPPacketRegister, r.hostUDP)
			}
			continue
		}
		// Control packets originate from this Agent. Ignore unexpected control
		// frames from the Host rather than allowing them to alter peer state.
		if pkt.Type == protocol.UDPPacketRegister || pkt.Type == protocol.UDPPacketHeartbeat {
			continue
		}
		r.keyMu.RLock()
		expectedPeerID, authenticatedPeerID := r.expectedPeerID, r.peerDeviceID
		r.keyMu.RUnlock()
		if expectedPeerID != "" && authenticatedPeerID != expectedPeerID {
			continue
		}
		// Deduplicate redundant packets (same seq number)
		if pkt.Type != protocol.UDPPacketRegister && pkt.Type != protocol.UDPPacketHeartbeat {
			if r.dedup.isDuplicate(pkt.Seq) {
				if isReliableInputPacket(pkt.Type) {
					r.sendInputAck(pkt.Seq, pkt.Type)
				}
				continue
			}
		}

		r.dispatch(pkt)
		if isReliableInputPacket(pkt.Type) {
			r.sendInputAck(pkt.Seq, pkt.Type)
		}
	}
}

func isReliableInputPacket(packetType uint8) bool {
	return packetType == protocol.UDPPacketMouseButton || packetType == protocol.UDPPacketKeyEvent || packetType == protocol.UDPPacketReleaseAll || packetType == protocol.UDPPacketStateSnapshot
}

func (r *UDPReceiver) sendInputAck(seq uint32, packetType uint8) {
	if seq == 0 || r.conn == nil || r.hostUDP == nil {
		return
	}
	pkt := &protocol.UDPPacket{
		Type:      protocol.UDPPacketInputAck,
		Seq:       seq,
		AckSeq:    seq,
		AckType:   packetType,
		Timestamp: time.Now().UnixMilli(),
	}
	_, _ = r.conn.WriteToUDP(protocol.EncodeAuthenticatedUDPPacket(pkt, r.sessionAuthKey()), r.hostUDP)
}

func (r *UDPReceiver) sessionAuthKey() []byte {
	r.keyMu.RLock()
	if len(r.peerKeys) > 0 {
		key := append([]byte(nil), r.peerKeys[0]...)
		r.keyMu.RUnlock()
		return key
	}
	r.keyMu.RUnlock()
	return r.authKey()
}

// dispatch converts a binary packet back to the callback parameters.
func (r *UDPReceiver) dispatch(pkt *protocol.UDPPacket) {
	r.keyMu.RLock()
	peerDeviceID, peerSessionID := r.peerDeviceID, r.peerSessionID
	r.keyMu.RUnlock()
	if pkt.Type == protocol.UDPPacketStateSnapshot {
		if r.OnInputState != nil {
			keys := make([]protocol.KeyState, len(pkt.StateKeys))
			copy(keys, pkt.StateKeys)
			buttons := make([]int, len(pkt.StateButtons))
			for i, button := range pkt.StateButtons {
				buttons[i] = int(button)
			}
			r.OnInputState(protocol.InputStatePayload{
				DeviceID: peerDeviceID, SessionID: peerSessionID, Sequence: pkt.Seq,
				Keys: keys, Buttons: buttons, Modifiers: pkt.StateModifiers,
			})
		}
		return
	}
	if r.OnInputEvent != nil && pkt.Type != protocol.UDPPacketReleaseAll {
		r.OnInputEvent(protocol.InputPayload{
			Type: func() string {
				switch pkt.Type {
				case protocol.UDPPacketMouseMove:
					return "mouse_move"
				case protocol.UDPPacketMouseButton:
					return "mouse_btn"
				case protocol.UDPPacketMouseScroll:
					if pkt.Axis == 1 {
						return "mouse_wheel_h"
					}
					return "mouse_wheel"
				case protocol.UDPPacketKeyEvent:
					return "key"
				default:
					return ""
				}
			}(),
			DeltaX: int(pkt.DeltaX), DeltaY: int(pkt.DeltaY),
			Button: int(pkt.Button), Pressed: pkt.Pressed == 1,
			KeyCode: pkt.KeyCode, ScanCode: pkt.ScanCode,
			Modifiers: pkt.Modifiers, WheelDelta: int(pkt.WheelDelta),
			Extended: pkt.KeyFlags&0x01 != 0, E1: pkt.KeyFlags&0x02 != 0,
			Repeat: pkt.KeyFlags&0x04 != 0, Sequence: pkt.Seq,
			DeviceID: peerDeviceID, SessionID: peerSessionID,
			HardwareID: protocol.HardwareIDFromTag(pkt.DeviceTag),
			Timestamp:  pkt.Timestamp,
		})
		return
	}

	switch pkt.Type {
	case protocol.UDPPacketReleaseAll:
		if r.OnReleaseAll != nil {
			r.OnReleaseAll()
		}
	case protocol.UDPPacketMouseMove:
		if r.OnInput == nil {
			return
		}
		r.OnInput("mouse_move", int(pkt.DeltaX), int(pkt.DeltaY), 0, false, 0, 0, 0, pkt.Timestamp)

	case protocol.UDPPacketMouseButton:
		if r.OnInput == nil {
			return
		}
		r.OnInput("mouse_btn", 0, 0, int(pkt.Button), pkt.Pressed == 1, 0, 0, 0, pkt.Timestamp)

	case protocol.UDPPacketMouseScroll:
		if r.OnInput == nil {
			return
		}
		eventType := "mouse_wheel"
		if pkt.Axis == 1 {
			eventType = "mouse_wheel_h"
		}
		r.OnInput(eventType, 0, 0, 0, false, 0, 0, int(pkt.WheelDelta), pkt.Timestamp)

	case protocol.UDPPacketKeyEvent:
		if r.OnInput == nil {
			return
		}
		r.OnInput("key", 0, 0, 0, pkt.Pressed == 1, pkt.KeyCode, pkt.Modifiers, 0, pkt.Timestamp)
	}
}

// Stop shuts down the UDP receiver.
func (r *UDPReceiver) Stop() {
	r.stopOnce.Do(func() {
		close(r.done)
		if r.conn != nil {
			r.conn.Close()
		}
	})
}

// PeerSessionID returns the authenticated Host session learned from ACK. An
// empty result means the handshake has not completed yet.
func (r *UDPReceiver) PeerSessionID() string {
	r.keyMu.RLock()
	defer r.keyMu.RUnlock()
	return r.peerSessionID
}

// SetKey rotates the shared transport key. The next heartbeat/register uses
// the new key, allowing a coordinated token rotation without process restart.
func (r *UDPReceiver) SetKey(key string) {
	if r.keyRing != nil {
		r.keyRing.Set(key)
	}
	r.keyMu.Lock()
	r.key = append(r.key[:0], []byte(key)...)
	r.keyMu.Unlock()
}

func (r *UDPReceiver) authKey() []byte {
	if r.keyRing != nil {
		keys := r.keyRing.Keys()
		if len(keys) > 0 {
			return keys[0]
		}
	}
	r.keyMu.RLock()
	defer r.keyMu.RUnlock()
	return append([]byte(nil), r.key...)
}

func (r *UDPReceiver) authKeys() [][]byte {
	var keys [][]byte
	if r.keyRing != nil {
		keys = r.keyRing.Keys()
	} else {
		key := r.authKey()
		if len(key) > 0 {
			keys = [][]byte{key}
		}
	}
	r.keyMu.RLock()
	for _, peerKey := range r.peerKeys {
		keys = append(keys, append([]byte(nil), peerKey...))
	}
	r.keyMu.RUnlock()
	return keys
}
