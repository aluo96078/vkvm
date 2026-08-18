package network

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"

	"vkvm/internal/protocol"

	"github.com/gorilla/websocket"
)

// WSClient handles WebSocket connection to Host
type WSClient struct {
	hostAddr       string
	token          string
	deviceID       string
	publicKey      string
	role           string
	expectedPeerID string
	sessionID      string
	capabilities   uint32
	conn           *websocket.Conn
	sessionKey     []byte
	send           chan protocol.Message
	done           chan struct{}
	reconnect      chan struct{}
	networkWake    chan struct{}
	networkAware   bool

	// Callbacks
	OnSwitch              func(profile string)
	OnSync                func(profiles interface{}, usbForwardingEnabled bool)
	OnInput               func(eventType string, deltaX, deltaY int, button int, pressed bool, keyCode uint16, modifiers uint16, wheelDelta int, timestamp int64)
	OnInputEvent          func(protocol.InputPayload)
	OnInputState          func(protocol.InputStatePayload)
	OnUSBForwardingUpdate func(enabled bool)
	OnReleaseAll          func()

	mu           sync.Mutex
	callbackMu   sync.RWMutex
	isConnected  bool
	state        ConnectionState
	lastError    string
	closed       bool
	closeOnce    sync.Once
	dialCancel   context.CancelFunc
	inputSeqMu   sync.Mutex
	inputSession string
	inputDedup   seqDedup
}

// ConnectionState describes the agent's current coordinator lifecycle.
// WaitingForNetworkChange is deliberately quiescent: no periodic LAN retry
// occurs until the network monitor reports a new fingerprint.
type ConnectionState string

const (
	ConnectionDisconnected      ConnectionState = "disconnected"
	ConnectionConnecting        ConnectionState = "connecting"
	ConnectionConnected         ConnectionState = "connected"
	ConnectionWaitingForNetwork ConnectionState = "waiting_for_network_change"
)

// NewWSClient creates a new WebSocket client

func NewWSClient(hostAddr, token string, deviceID ...string) *WSClient {
	id := ""
	if len(deviceID) > 0 {
		id = deviceID[0]
	}
	return &WSClient{
		hostAddr:     hostAddr,
		token:        token,
		deviceID:     id,
		send:         make(chan protocol.Message, 100),
		done:         make(chan struct{}),
		reconnect:    make(chan struct{}, 1),
		networkWake:  make(chan struct{}, 1),
		state:        ConnectionDisconnected,
		capabilities: protocol.CapabilityInputMetadata | protocol.CapabilityReliableControl | protocol.CapabilityStateSnapshot | protocol.CapabilityWebSocket,
		inputDedup:   newSeqDedup(),
	}
}

// Start begins the client loop (connect & process)
func (c *WSClient) Start() {
	go c.loop()
}

func (c *WSClient) loop() {
	networkRetries := 0
	for {
		select {
		case <-c.done:
			return
		default:
		}
		c.setState(ConnectionConnecting)
		c.connect()
		c.mu.Lock()
		connected := c.isConnected
		networkAware := c.networkAware
		c.mu.Unlock()
		if connected {
			networkRetries = 0
			continue
		}

		// If connect returns, it means we disconnected. Wait a bit and retry.
		if networkAware {
			// A short outage with an unchanged fingerprint gets a bounded set of
			// endpoint retries before entering the quiescent waiting state.
			if networkRetries < 3 {
				backoff := time.Duration(1<<networkRetries) * 250 * time.Millisecond
				networkRetries++
				timer := time.NewTimer(backoff)
				select {
				case <-timer.C:
					continue
				case <-c.done:
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					return
				}
			}
			c.setState(ConnectionWaitingForNetwork)
			select {
			case <-c.done:
				return
			case <-c.networkWake:
				networkRetries = 0
				log.Println("WS Client: network changed; retrying coordinator connection")
			}
		} else {
			c.setState(ConnectionDisconnected)
			select {
			case <-c.done:
				return
			case <-time.After(5 * time.Second):
				log.Println("WS Client: Attempting reconnection...")
			}
		}
	}
}

// EnableNetworkAwareReconnect makes a failed connection dormant until
// NotifyNetworkChange is called. This avoids continuous LAN retries when the
// configured peer is absent from the current network.
func (c *WSClient) EnableNetworkAwareReconnect(enabled bool) {
	c.mu.Lock()
	c.networkAware = enabled
	c.mu.Unlock()
}

// SetToken updates the bearer credential used by the next connection and
// subsequent reconnects. Existing sockets are intentionally not rewritten.
func (c *WSClient) SetToken(token string) {
	c.mu.Lock()
	c.token = token
	c.mu.Unlock()
}

// SetHostAddr updates the coordinator endpoint used by future connection
// attempts. Existing sockets are left untouched; callers can request a
// network wake afterwards so a dormant client retries immediately.
func (c *WSClient) SetHostAddr(hostAddr string) {
	c.mu.Lock()
	c.hostAddr = hostAddr
	c.mu.Unlock()
}

func (c *WSClient) SetPublicKey(publicKey string) {
	c.mu.Lock()
	c.publicKey = publicKey
	c.mu.Unlock()
}

func (c *WSClient) SetRole(role string) {
	c.mu.Lock()
	c.role = role
	c.mu.Unlock()
}

func (c *WSClient) HostAddr() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hostAddr
}

// SetExpectedPeerDeviceID pins the coordinator identity learned during
// pairing. An empty value retains legacy token-only migration mode.
func (c *WSClient) SetExpectedPeerDeviceID(deviceID string) {
	c.mu.Lock()
	c.expectedPeerID = deviceID
	c.mu.Unlock()
}

// SetCapabilities updates the negotiated transport capabilities. When a
// session is already connected, re-send the authenticated identity message so
// the Host can switch input routing without opening a second socket.
func (c *WSClient) SetCapabilities(capabilities uint32) {
	c.mu.Lock()
	capabilitiesChanged := c.capabilities != capabilities
	c.capabilities = capabilities
	connected := c.isConnected
	deviceID, sessionID, publicKey, role := c.deviceID, c.sessionID, c.publicKey, c.role
	c.mu.Unlock()
	if !capabilitiesChanged || !connected {
		return
	}
	c.enqueue(protocol.Message{Type: protocol.TypeAuth, Payload: protocol.AuthPayload{
		DeviceID: deviceID, SessionID: sessionID, PublicKey: publicKey, Role: role,
		ProtocolVersion: protocol.ProtocolVersion, Capabilities: capabilities,
	}})
}

func (c *WSClient) NotifyNetworkChange() {
	select {
	case c.networkWake <- struct{}{}:
	default:
	}
}

func (c *WSClient) setState(state ConnectionState) {
	c.mu.Lock()
	c.state = state
	c.mu.Unlock()
}

func (c *WSClient) setLastError(err error) {
	c.mu.Lock()
	if err == nil {
		c.lastError = ""
	} else {
		c.lastError = err.Error()
	}
	c.mu.Unlock()
}

func (c *WSClient) connect() {
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.dialCancel = cancel
	c.mu.Unlock()
	defer func() {
		cancel()
		c.mu.Lock()
		if c.dialCancel != nil {
			c.dialCancel = nil
		}
		c.mu.Unlock()
	}()

	header := http.Header{}
	c.mu.Lock()
	token := c.token
	hostAddr := c.hostAddr
	c.mu.Unlock()
	if token == "" {
		c.setState(ConnectionDisconnected)
		err := fmt.Errorf("API token is empty")
		c.setLastError(err)
		log.Printf("WS Client: refusing unauthenticated connection to %s", hostAddr)
		return
	}
	if hostAddr == "" {
		c.setState(ConnectionWaitingForNetwork)
		c.setLastError(fmt.Errorf("coordinator address is empty"))
		return
	}
	u := url.URL{Scheme: "ws", Host: hostAddr, Path: "/ws"}
	header.Set("Authorization", "Bearer "+token)
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, u.String(), header)
	if err != nil {
		c.setState(ConnectionDisconnected)
		c.setLastError(fmt.Errorf("connect to %s failed: %w", hostAddr, err))
		log.Printf("WS Client: connection to %s failed: %v", hostAddr, err)
		return
	}
	defer conn.Close()

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = conn.Close()
		return
	}
	c.conn = conn
	c.isConnected = true
	c.state = ConnectionConnected
	c.sessionID = NewSessionID()
	sessionID := c.sessionID
	deviceID := c.deviceID
	publicKey := c.publicKey
	role := c.role
	capabilities := c.capabilities
	c.sessionKey = protocol.DeriveSessionKey(token, deviceID, sessionID)
	c.mu.Unlock()
	c.setLastError(nil)

	// Send Auth/Handshake immediately
	// For now we assume open or token header, but let's send an Identify if needed.
	// We'll immediately request Sync as well.
	c.enqueue(protocol.Message{Type: protocol.TypeAuth, Payload: protocol.AuthPayload{
		DeviceID: deviceID, SessionID: sessionID, PublicKey: publicKey, Role: role,
		ProtocolVersion: protocol.ProtocolVersion,
		Capabilities:    capabilities | protocol.CapabilityWebSocket,
	}})
	c.SendSyncRequest()

	// Start read/write pumps
	// specific done channel for this connection
	connDone := make(chan struct{})

	go func() {
		defer close(connDone)
		c.writePump(conn)
	}()

	readErr := c.readPump(conn)
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if readErr != nil && !closed {
		c.setLastError(fmt.Errorf("connection closed: %w", readErr))
		log.Printf("WS Client: connection to %s closed: %v", hostAddr, readErr)
	}

	// Cleanup
	// Closing the connection unblocks the writer if the reader noticed the
	// disconnect first.
	_ = conn.Close()
	c.mu.Lock()
	c.isConnected = false
	c.conn = nil
	c.sessionKey = nil
	c.state = ConnectionDisconnected
	c.mu.Unlock()
	c.callbackMu.RLock()
	onReleaseAll := c.OnReleaseAll
	c.callbackMu.RUnlock()
	if onReleaseAll != nil {
		onReleaseAll()
	}

	// Ensure write pump stops
	<-connDone
}

// NewSessionID returns a process-independent random transport session ID.
// It is shared by the UDP and WebSocket handshakes so sequence numbers can be
// scoped to a single authenticated connection.
func NewSessionID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(buf)
}

func (c *WSClient) readPump(conn *websocket.Conn) error {
	conn.SetReadLimit(4096)
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error { conn.SetReadDeadline(time.Now().Add(60 * time.Second)); return nil })

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}

		var msg protocol.Message
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		c.mu.Lock()
		key := append([]byte(nil), c.sessionKey...)
		c.mu.Unlock()
		if !protocol.VerifyMessage(msg, key) {
			log.Printf("WS Client: rejecting invalid or unauthenticated message")
			return fmt.Errorf("invalid or unauthenticated message")
		}

		c.handleMessage(msg)
	}
}

func (c *WSClient) writePump(conn *websocket.Conn) {
	ticker := time.NewTicker(30 * time.Second) // Ping ticker
	defer ticker.Stop()

	for {
		select {
		case msg := <-c.send:
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			jsonMsg, err := c.marshalSigned(msg)
			if err != nil {
				continue
			}
			if err := conn.WriteMessage(websocket.TextMessage, jsonMsg); err != nil {
				return
			}

			// Drain queued messages to reduce per-message overhead
			n := len(c.send)
			for i := 0; i < n; i++ {
				qMsg := <-c.send
				conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				qJson, err := c.marshalSigned(qMsg)
				if err != nil {
					continue
				}
				if err := conn.WriteMessage(websocket.TextMessage, qJson); err != nil {
					return
				}
			}

		case <-ticker.C:
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}

		case <-c.done:
			return
		}
	}
}

func (c *WSClient) marshalSigned(msg protocol.Message) ([]byte, error) {
	c.mu.Lock()
	key := append([]byte(nil), c.sessionKey...)
	c.mu.Unlock()
	if err := protocol.SignMessageInPlace(&msg, key); err != nil {
		return nil, err
	}
	return json.Marshal(msg)
}

func (c *WSClient) handleMessage(msg protocol.Message) {
	switch msg.Type {
	case protocol.TypeSwitch:
		var payload protocol.SwitchPayload
		bytes, _ := json.Marshal(msg.Payload)
		json.Unmarshal(bytes, &payload)

		log.Printf("WS Client: Received switch command for '%s'", payload.Profile)
		c.callbackMu.RLock()
		onSwitch := c.OnSwitch
		c.callbackMu.RUnlock()
		if onSwitch != nil {
			onSwitch(payload.Profile)
		}

	case protocol.TypeSyncResponse:
		var payload protocol.SyncResponsePayload
		bytes, _ := json.Marshal(msg.Payload)
		json.Unmarshal(bytes, &payload)
		c.mu.Lock()
		expectedPeerID := c.expectedPeerID
		c.mu.Unlock()
		if expectedPeerID != "" && payload.DeviceID != expectedPeerID {
			log.Printf("WS Client: rejecting sync from unexpected coordinator %q", payload.DeviceID)
			return
		}

		c.callbackMu.RLock()
		onSync := c.OnSync
		c.callbackMu.RUnlock()
		if onSync != nil {
			onSync(payload.Profiles, payload.USBForwardingEnabled)
		}

	case protocol.TypeInput:
		var payload protocol.InputPayload
		bytes, _ := json.Marshal(msg.Payload)
		json.Unmarshal(bytes, &payload)
		if payload.Sequence != 0 {
			c.inputSeqMu.Lock()
			if payload.SessionID != "" && payload.SessionID != c.inputSession {
				c.inputSession = payload.SessionID
				c.inputDedup = newSeqDedup()
			}
			duplicate := c.inputDedup.isDuplicate(payload.Sequence)
			c.inputSeqMu.Unlock()
			if duplicate {
				return
			}
		}
		c.callbackMu.RLock()
		onInputEvent := c.OnInputEvent
		c.callbackMu.RUnlock()
		if onInputEvent != nil {
			onInputEvent(payload)
			return
		}

		c.callbackMu.RLock()
		onInput := c.OnInput
		c.callbackMu.RUnlock()
		if onInput != nil {
			onInput(
				payload.Type,
				payload.DeltaX, payload.DeltaY,
				payload.Button, payload.Pressed,
				payload.KeyCode, payload.Modifiers,
				payload.WheelDelta,
				payload.Timestamp,
			)
		}

	case protocol.TypeInputState:
		var payload protocol.InputStatePayload
		bytes, _ := json.Marshal(msg.Payload)
		if err := json.Unmarshal(bytes, &payload); err != nil {
			return
		}
		if payload.Sequence != 0 {
			c.inputSeqMu.Lock()
			if payload.SessionID != "" && payload.SessionID != c.inputSession {
				c.inputSession = payload.SessionID
				c.inputDedup = newSeqDedup()
			}
			duplicate := c.inputDedup.isDuplicate(payload.Sequence)
			c.inputSeqMu.Unlock()
			if duplicate {
				return
			}
		}
		c.callbackMu.RLock()
		onState := c.OnInputState
		c.callbackMu.RUnlock()
		if onState != nil {
			onState(payload)
		}

	case protocol.TypeUSBForwardingUpdate:
		var payload protocol.USBForwardingUpdatePayload
		bytes, _ := json.Marshal(msg.Payload)
		json.Unmarshal(bytes, &payload)

		log.Printf("WS Client: Received USB forwarding update: %v", payload.Enabled)
		c.callbackMu.RLock()
		onUSBUpdate := c.OnUSBForwardingUpdate
		c.callbackMu.RUnlock()
		if onUSBUpdate != nil {
			onUSBUpdate(payload.Enabled)
		}

	case protocol.TypeReleaseAll:
		c.callbackMu.RLock()
		onReleaseAll := c.OnReleaseAll
		c.callbackMu.RUnlock()
		if onReleaseAll != nil {
			onReleaseAll()
		}
	}
}

// SetInputHandler replaces the input callback without racing the read pump.
func (c *WSClient) SetInputHandler(callback func(eventType string, deltaX, deltaY int, button int, pressed bool, keyCode uint16, modifiers uint16, wheelDelta int, timestamp int64)) {
	c.callbackMu.Lock()
	c.OnInput = callback
	c.callbackMu.Unlock()
}

// SetInputEventHandler installs the metadata-preserving input callback.
func (c *WSClient) SetInputEventHandler(callback func(protocol.InputPayload)) {
	c.callbackMu.Lock()
	c.OnInputEvent = callback
	c.callbackMu.Unlock()
}

func (c *WSClient) SetInputStateHandler(callback func(protocol.InputStatePayload)) {
	c.callbackMu.Lock()
	c.OnInputState = callback
	c.callbackMu.Unlock()
}

// SetReleaseHandler replaces the release-all callback without racing reads.
func (c *WSClient) SetReleaseHandler(callback func()) {
	c.callbackMu.Lock()
	c.OnReleaseAll = callback
	c.callbackMu.Unlock()
}

// SendSwitch sends a switch request to host
func (c *WSClient) SendSwitch(profile string) {
	c.enqueue(protocol.Message{
		Type: protocol.TypeSwitch,
		Payload: protocol.SwitchPayload{
			Profile: profile,
			Origin:  "agent", // Host will replace with actual IP if needed
		},
	})
}

// SendSyncRequest asks host for config
func (c *WSClient) SendSyncRequest() {
	c.enqueue(protocol.Message{
		Type:    protocol.TypeSyncRequest,
		Payload: nil,
	})
}

// SendInputEvent sends keyboard/mouse input events to host
func (c *WSClient) SendInputEvent(eventType string, deltaX, deltaY int, button int, pressed bool, keyCode uint16, modifiers uint16, wheelDelta int, timestamp int64) {
	c.enqueue(protocol.Message{
		Type: protocol.TypeInput,
		Payload: protocol.InputPayload{
			Type:       eventType,
			DeltaX:     deltaX,
			DeltaY:     deltaY,
			Button:     button,
			Pressed:    pressed,
			KeyCode:    keyCode,
			Modifiers:  modifiers,
			WheelDelta: wheelDelta,
			Timestamp:  timestamp,
		},
	})
}

func (c *WSClient) enqueue(message protocol.Message) {
	select {
	case c.send <- message:
	case <-c.done:
	default:
		log.Printf("WS Client: dropping outbound %s message because queue is full", message.Type)
	}
}

// IsConnected returns true if client is connected to host
func (c *WSClient) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.isConnected
}

// State returns a snapshot of the connection lifecycle state.
func (c *WSClient) State() ConnectionState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// LastError returns the most recent connection error, if any. It is intended
// for diagnostics and never contains authentication headers or token values.
func (c *WSClient) LastError() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastError
}

// SessionID returns the authenticated transport session currently used by the
// client. It is a diagnostic/handover fence, not an authentication secret.
func (c *WSClient) SessionID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionID
}

// Close stops the client
func (c *WSClient) Close() {
	c.closeOnce.Do(func() {
		close(c.done)
		c.mu.Lock()
		c.closed = true
		conn := c.conn
		dialCancel := c.dialCancel
		c.conn = nil
		c.isConnected = false
		c.state = ConnectionDisconnected
		c.mu.Unlock()
		if dialCancel != nil {
			dialCancel()
		}
		if conn != nil {
			_ = conn.Close()
		}
	})
}
