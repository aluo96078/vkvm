package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"vkvm/internal/input"
	"vkvm/internal/protocol"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// Allow all origins for now as this is a local network tool
	CheckOrigin: func(r *http.Request) bool {
		// Native agents do not send an Origin header. Browser callers must be
		// same-origin; bearer authentication is still required by middleware.
		origin := r.Header.Get("Origin")
		return origin == "" || origin == "http://"+r.Host || origin == "https://"+r.Host
	},
}

// WSManager handles WebSocket connections and broadcasting
type WSManager struct {
	server        *Server
	clients       map[*WebSocketClient]bool
	clientsMu     sync.RWMutex
	stopped       bool
	broadcast     chan protocol.Message
	register      chan *WebSocketClient
	unregister    chan *WebSocketClient
	shutdown      chan struct{}
	lifecycleMu   sync.Mutex
	running       bool
	runDone       chan struct{}
	shutdownSent  bool
	seq           uint32
	sessionID     string
	stateProvider func() input.StateSnapshot
	udpSelector   func(deviceID string) bool
}

// WebSocketClient represents a connected agent
type WebSocketClient struct {
	manager         *WSManager
	conn            *websocket.Conn
	send            chan []byte
	ip              string
	deviceID        string
	sessionID       string
	authenticated   bool
	protocolVersion uint16
	capabilities    uint32
	sessionKeys     [][]byte
	identityMu      sync.RWMutex
	done            chan struct{}
	closeOnce       sync.Once
}

func newWSManager(s *Server) *WSManager {
	sessionID := make([]byte, 16)
	if _, err := rand.Read(sessionID); err != nil {
		// A process-local random fallback still prevents accidental cross-process
		// sequence reuse if the OS RNG is temporarily unavailable.
		sessionID = []byte(time.Now().UTC().Format(time.RFC3339Nano))
	}
	return &WSManager{
		server:     s,
		clients:    make(map[*WebSocketClient]bool),
		broadcast:  make(chan protocol.Message, 256),
		register:   make(chan *WebSocketClient, 64),
		unregister: make(chan *WebSocketClient, 64),
		shutdown:   make(chan struct{}),
		sessionID:  hex.EncodeToString(sessionID),
	}
}

func (m *WSManager) start() {
	m.startReady(nil)
}

func (m *WSManager) startReady(ready chan<- struct{}) {
	// A manager can be restarted after the API supervisor recycles its HTTP
	// listener. Channels belong to one run; never reuse a closed shutdown
	// signal or a hub goroutine that has already returned.
	m.lifecycleMu.Lock()
	if m.running {
		if ready != nil {
			close(ready)
		}
		m.lifecycleMu.Unlock()
		return
	}
	m.broadcast = make(chan protocol.Message, 256)
	m.register = make(chan *WebSocketClient, 64)
	m.unregister = make(chan *WebSocketClient, 64)
	m.shutdown = make(chan struct{})
	m.runDone = make(chan struct{})
	runDone := m.runDone
	register := m.register
	unregister := m.unregister
	broadcast := m.broadcast
	shutdown := m.shutdown
	m.running = true
	m.shutdownSent = false
	m.clientsMu.Lock()
	m.stopped = false
	m.clientsMu.Unlock()
	m.lifecycleMu.Unlock()
	if ready != nil {
		close(ready)
	}
	defer func() {
		m.lifecycleMu.Lock()
		m.running = false
		close(runDone)
		m.lifecycleMu.Unlock()
	}()

	for {
		select {
		case client := <-register:
			if client == nil {
				continue
			}
			m.clientsMu.Lock()
			if m.stopped {
				m.clientsMu.Unlock()
				client.stop()
				continue
			}
			m.clients[client] = true
			total := len(m.clients)
			m.clientsMu.Unlock()
			log.Printf("WS: New client registered from %s. Total clients: %d", client.ip, total)

		case client := <-unregister:
			if client == nil {
				continue
			}
			m.clientsMu.Lock()
			if _, ok := m.clients[client]; ok {
				delete(m.clients, client)
				total := len(m.clients)
				m.clientsMu.Unlock()
				client.stop()
				log.Printf("WS: Client unregistered from %s. Total clients: %d", client.ip, total)
			} else {
				m.clientsMu.Unlock()
			}

		case message := <-broadcast:
			m.broadcastMessage(message)

		case <-shutdown:
			return
		}
	}
}

func (m *WSManager) startAsync() {
	ready := make(chan struct{})
	go m.startReady(ready)
	<-ready
}

func (m *WSManager) isStopped() bool {
	m.lifecycleMu.Lock()
	running := m.running
	m.lifecycleMu.Unlock()
	if running {
		return false
	}
	m.clientsMu.RLock()
	defer m.clientsMu.RUnlock()
	return m.stopped
}

// stop closes the hub and all active client connections. It is safe to call
// more than once and is used by the HTTP server shutdown path.
func (m *WSManager) stop() {
	m.lifecycleMu.Lock()
	shutdown := m.shutdown
	runDone := m.runDone
	running := m.running
	if running && !m.shutdownSent {
		m.shutdownSent = true
		if shutdown != nil {
			close(shutdown)
		}
	}
	m.lifecycleMu.Unlock()
	m.clientsMu.Lock()
	if !m.stopped {
		m.stopped = true
	}
	clients := make([]*WebSocketClient, 0, len(m.clients))
	for client := range m.clients {
		clients = append(clients, client)
	}
	m.clients = make(map[*WebSocketClient]bool)
	m.clientsMu.Unlock()
	for _, client := range clients {
		client.stop()
	}
	if running && runDone != nil {
		<-runDone
	}
}

func (m *WSManager) broadcastMessage(message protocol.Message) {
	m.clientsMu.RLock()
	clients := make([]*WebSocketClient, 0, len(m.clients))
	for client := range m.clients {
		clients = append(clients, client)
	}
	m.clientsMu.RUnlock()

	for _, client := range clients {
		if message.Type == protocol.TypeInput && client.usesUDP() {
			continue
		}
		if !client.enqueueJSON(message) {
			select {
			case m.unregister <- client:
			default:
				client.stop()
			}
		}
	}
}

func (c *WebSocketClient) usesUDP() bool {
	c.identityMu.RLock()
	deviceID, capabilities := c.deviceID, c.capabilities
	c.identityMu.RUnlock()
	if c.manager == nil || deviceID == "" || capabilities&protocol.CapabilityUDP == 0 {
		return false
	}
	c.manager.clientsMu.RLock()
	selector := c.manager.udpSelector
	c.manager.clientsMu.RUnlock()
	return selector != nil && selector(deviceID)
}

func (m *WSManager) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WS: Failed to upgrade connection: %v", err)
		return
	}

	client := &WebSocketClient{
		manager: m,
		conn:    conn,
		send:    make(chan []byte, 256),
		ip:      r.RemoteAddr,
		done:    make(chan struct{}),
	}

	// Register client
	select {
	case m.register <- client:
	case <-m.shutdown:
		client.stop()
		return
	}

	// Start pump goroutines
	go client.writePump()
	go client.readPump()
}

// readPump pumps messages from the websocket connection to the hub.
func (c *WebSocketClient) readPump() {
	defer func() {
		select {
		case c.manager.unregister <- c:
		case <-c.manager.shutdown:
		}
		c.stop()
	}()

	c.conn.SetReadLimit(4096)
	c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error { c.conn.SetReadDeadline(time.Now().Add(60 * time.Second)); return nil })

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("WS: Read error: %v", err)
			}
			break
		}

		c.handleMessage(message)
	}
}

// writePump pumps messages from the hub to the websocket connection.
func (c *WebSocketClient) writePump() {
	ticker := time.NewTicker(50 * time.Second)
	defer func() {
		ticker.Stop()
		c.stop()
	}()

	for {
		select {
		case message := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))

			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(message)
			if err := w.Close(); err != nil {
				return
			}

			// Drain queued messages to reduce per-message overhead
			// Each message is still a separate WebSocket frame but sent in rapid succession
			n := len(c.send)
			for i := 0; i < n; i++ {
				msg := <-c.send
				c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				w2, err := c.conn.NextWriter(websocket.TextMessage)
				if err != nil {
					return
				}
				w2.Write(msg)
				if err := w2.Close(); err != nil {
					return
				}
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-c.done:
			return
		}
	}
}

func (c *WebSocketClient) enqueue(message []byte) bool {
	select {
	case c.send <- message:
		return true
	case <-c.done:
		return false
	default:
		return false
	}
}

func (c *WebSocketClient) enqueueJSON(message protocol.Message) bool {
	c.identityMu.RLock()
	key := []byte(nil)
	if len(c.sessionKeys) > 0 {
		key = append([]byte(nil), c.sessionKeys[0]...)
	}
	c.identityMu.RUnlock()
	if err := protocol.SignMessageInPlace(&message, key); err != nil {
		return false
	}
	data, err := json.Marshal(message)
	if err != nil {
		return false
	}
	return c.enqueue(data)
}

func (c *WebSocketClient) stop() {
	c.closeOnce.Do(func() {
		close(c.done)
		if c.conn != nil {
			_ = c.conn.Close()
		}
	})
}

func (c *WebSocketClient) handleMessage(data []byte) {
	var msg protocol.Message
	if err := json.Unmarshal(data, &msg); err != nil {
		log.Printf("WS: Invalid message format: %v", err)
		return
	}
	if !c.verifyMessage(msg) {
		log.Printf("WS: rejecting unauthenticated or invalid-MAC message from %s", c.ip)
		c.stop()
		return
	}

	switch msg.Type {
	case protocol.TypeAuth:
		var payload protocol.AuthPayload
		jsonBytes, _ := json.Marshal(msg.Payload)
		if err := json.Unmarshal(jsonBytes, &payload); err != nil {
			log.Printf("WS: Invalid auth payload from %s: %v", c.ip, err)
			return
		}
		if payload.DeviceID == "" || payload.SessionID == "" {
			log.Printf("WS: client %s omitted device/session identity", c.ip)
			return
		}
		if !c.manager.server.isDevicePaired(payload.DeviceID) {
			log.Printf("WS: rejecting unpaired device=%s from %s", payload.DeviceID, c.ip)
			c.stop()
			return
		}
		if expectedKey := c.manager.server.configMgr.PairedPublicKey(payload.DeviceID); expectedKey != "" && (payload.PublicKey == "" || subtle.ConstantTimeCompare([]byte(expectedKey), []byte(payload.PublicKey)) != 1) {
			log.Printf("WS: rejecting public-key mismatch for device=%s", payload.DeviceID)
			c.stop()
			return
		}
		if expectedRole := c.manager.server.configMgr.PairedRole(payload.DeviceID); expectedRole != "" && payload.Role != "" && expectedRole != payload.Role {
			log.Printf("WS: rejecting role mismatch for device=%s", payload.DeviceID)
			c.stop()
			return
		}
		if payload.ProtocolVersion == 0 {
			payload.ProtocolVersion = protocol.ProtocolVersion
		}
		if payload.ProtocolVersion > protocol.ProtocolVersion {
			log.Printf("WS: rejecting unsupported protocol version %d", payload.ProtocolVersion)
			c.stop()
			return
		}
		c.identityMu.Lock()
		c.deviceID = payload.DeviceID
		c.sessionID = payload.SessionID
		c.authenticated = true
		c.protocolVersion = payload.ProtocolVersion
		c.capabilities = payload.Capabilities
		c.identityMu.Unlock()
		log.Printf("WS: authenticated session device=%s session=%s from %s", payload.DeviceID, payload.SessionID, c.ip)
		c.manager.sendStateSnapshot(c)

	case protocol.TypeSwitch:
		if !c.authenticated {
			log.Printf("WS: rejecting switch before authentication from %s", c.ip)
			c.stop()
			return
		}
		var payload protocol.SwitchPayload
		jsonBytes, _ := json.Marshal(msg.Payload)
		if err := json.Unmarshal(jsonBytes, &payload); err != nil {
			log.Printf("WS: Invalid switch payload: %v", err)
			return
		}

		log.Printf("WS: Received switch request to '%s' from %s", payload.Profile, c.ip)

		// Execute switch
		// Note: We use SwitchLocalOnly because we will rebroadcast via WebSocket if needed,
		// but SwitchToProfile logic involves legacy propagation we want to avoid double-triggering.
		// Actually, we should use the switcher's method that knows what to do.
		// If we are Host, we should apply locally and broadcast to others.

		// Use a goroutine to avoid blocking the read pump
		go func() {
			if err := c.manager.server.switcher.SwitchLocalOnly(payload.Profile); err != nil {
				log.Printf("WS: Switch failed: %v", err)
			}
			// Note: We do NOT broadcast here effectively avoiding double broadcast if onSwitch is wired up.
			// The onSwitch callback in Switcher (wired in main.go) will trigger the broadcast.
			// However, if we rely on onSwitch, we lose the "Origin" information (it becomes "host").
			// But for now that's acceptable consistency.
		}()

	case protocol.TypeSyncRequest:
		if !c.authenticated {
			log.Printf("WS: rejecting sync before authentication from %s", c.ip)
			c.stop()
			return
		}
		// Send config back
		cfg := c.manager.server.configMgr.Get()
		resp := protocol.Message{
			Type: protocol.TypeSyncResponse,
			Payload: protocol.SyncResponsePayload{
				Profiles:             cfg.Profiles,
				USBForwardingEnabled: cfg.General.USBForwardingEnabled,
				DeviceID:             cfg.General.DeviceID,
				SessionID:            c.manager.sessionID,
			},
		}

		c.enqueueJSON(resp)
	}
}

// verifyMessage authenticates the first identity frame using the token key
// ring, then authenticates every subsequent frame with the derived session
// key. This binds the WebSocket stream to both the paired device and session.
func (c *WebSocketClient) verifyMessage(msg protocol.Message) bool {
	c.identityMu.RLock()
	authenticated := c.authenticated
	keys := make([][]byte, len(c.sessionKeys))
	for i, key := range c.sessionKeys {
		keys[i] = append([]byte(nil), key...)
	}
	c.identityMu.RUnlock()
	if authenticated {
		if len(keys) == 0 {
			return false
		}
		_, ok := protocol.VerifyMessageAny(msg, keys)
		return ok
	}
	if msg.Type != protocol.TypeAuth {
		return false
	}
	var payload protocol.AuthPayload
	encoded, err := json.Marshal(msg.Payload)
	if err != nil || json.Unmarshal(encoded, &payload) != nil || payload.DeviceID == "" || payload.SessionID == "" {
		return false
	}
	if c.manager == nil || c.manager.server == nil || c.manager.server.tokens == nil {
		return false
	}
	for _, tokenKey := range c.manager.server.tokens.Keys() {
		key := protocol.DeriveSessionKey(string(tokenKey), payload.DeviceID, payload.SessionID)
		if protocol.VerifyMessage(msg, key) {
			c.identityMu.Lock()
			c.sessionKeys = [][]byte{key}
			c.identityMu.Unlock()
			return true
		}
	}
	return false
}

// Public method to broadcast switch events from the Switcher (e.g. host triggered by hotkey)
func (m *WSManager) BroadcastSwitch(profile string, origin string) {
	msg := protocol.Message{
		Type: protocol.TypeSwitch,
		Payload: protocol.SwitchPayload{
			Profile:   profile,
			Origin:    origin,
			Propagate: true, // Tell receivers they should act on it
		},
	}
	m.enqueueBroadcast(msg)
}

// Public method to broadcast input events from the Host to all Agents
func (m *WSManager) BroadcastInput(eventType string, deltaX, deltaY int, button int, pressed bool, keyCode uint16, modifiers uint16, wheelDelta int, timestamp int64) {
	m.BroadcastInputEvent(input.InputEvent{
		Type: eventType, DeltaX: deltaX, DeltaY: deltaY, Button: button,
		Pressed: pressed, KeyCode: keyCode, Modifiers: modifiers,
		WheelDelta: wheelDelta, Timestamp: timestamp,
	})
}

// BroadcastInputEvent preserves the physical input metadata needed for
// ordered/replay-safe injection while retaining the legacy positional API.
func (m *WSManager) BroadcastInputEvent(event input.InputEvent) {
	sequence := atomic.AddUint32(&m.seq, 1)
	if event.Sequence != 0 {
		sequence = event.Sequence
	}
	sessionID := event.SessionID
	if sessionID == "" {
		sessionID = m.sessionID
	}
	m.enqueueBroadcast(protocol.Message{
		Type: protocol.TypeInput,
		Payload: protocol.InputPayload{
			Type: event.Type, DeltaX: event.DeltaX, DeltaY: event.DeltaY,
			Button: event.Button, Pressed: event.Pressed, KeyCode: event.KeyCode,
			Modifiers: event.Modifiers, WheelDelta: event.WheelDelta,
			Timestamp: event.Timestamp, DeviceID: event.DeviceID,
			HardwareID: event.HardwareID,
			ScanCode:   event.ScanCode, Extended: event.Extended, E1: event.E1, Repeat: event.Repeat,
			Sequence: sequence, SessionID: sessionID, ClockNS: event.ClockNS,
		},
	})
}

// Public method to broadcast USB forwarding setting changes from the Host to all Agents
func (m *WSManager) BroadcastUSBForwarding(enabled bool) {
	msg := protocol.Message{
		Type: protocol.TypeUSBForwardingUpdate,
		Payload: protocol.USBForwardingUpdatePayload{
			Enabled: enabled,
		},
	}
	m.enqueueBroadcast(msg)
}

// BroadcastReleaseAll releases remote input state on every connected Agent.
func (m *WSManager) BroadcastReleaseAll() {
	m.enqueueBroadcast(protocol.Message{Type: protocol.TypeReleaseAll})
}

// SetInputStateProvider supplies the authoritative Host pressed-state snapshot
// used for newly authenticated sessions and recovery broadcasts.
func (m *WSManager) SetInputStateProvider(provider func() input.StateSnapshot) {
	m.clientsMu.Lock()
	m.stateProvider = provider
	m.clientsMu.Unlock()
}

func (m *WSManager) SetUDPTransportSelector(selector func(deviceID string) bool) {
	m.clientsMu.Lock()
	m.udpSelector = selector
	m.clientsMu.Unlock()
}

func (m *WSManager) sendStateSnapshot(client *WebSocketClient) {
	m.clientsMu.RLock()
	provider := m.stateProvider
	sessionID := m.sessionID
	m.clientsMu.RUnlock()
	if provider == nil || client == nil {
		return
	}
	snapshot := provider()
	keys := make([]protocol.KeyState, 0, len(snapshot.Keys))
	for _, key := range snapshot.Keys {
		keys = append(keys, protocol.KeyState{KeyCode: key.KeyCode, ScanCode: key.ScanCode, Extended: key.Extended, E1: key.E1})
	}
	sequence := atomic.AddUint32(&m.seq, 1)
	client.enqueueJSON(protocol.Message{Type: protocol.TypeInputState, Payload: protocol.InputStatePayload{
		DeviceID: m.server.configMgr.Get().General.DeviceID, SessionID: sessionID,
		Sequence: sequence, Keys: keys, Buttons: snapshot.Buttons, Modifiers: snapshot.Modifiers,
	}})
}

func (m *WSManager) enqueueBroadcast(message protocol.Message) {
	select {
	case m.broadcast <- message:
	case <-m.shutdown:
	default:
		log.Printf("WS: dropping broadcast %s because hub queue is full", message.Type)
	}
}
