package network

import (
	"encoding/json"
	"log"
	"net/url"
	"sync"
	"time"

	"vkvm/internal/protocol"

	"github.com/gorilla/websocket"
)

// WSClient handles WebSocket connection to Host
type WSClient struct {
	hostAddr  string
	token     string
	conn      *websocket.Conn
	send      chan protocol.Message
	done      chan struct{}
	reconnect chan struct{}

	// Callbacks
	OnSwitch func(profile string)
	OnSync   func(profiles interface{})
	OnInput  func(eventType string, deltaX, deltaY int, button int, pressed bool, keyCode uint16, modifiers uint16, timestamp int64)

	mu          sync.Mutex
	isConnected bool
}

// NewWSClient creates a new WebSocket client
func NewWSClient(hostAddr, token string) *WSClient {
	return &WSClient{
		hostAddr:  hostAddr,
		token:     token,
		send:      make(chan protocol.Message, 100),
		done:      make(chan struct{}),
		reconnect: make(chan struct{}, 1),
	}
}

// Start begins the client loop (connect & process)
func (c *WSClient) Start() {
	go c.loop()
}

func (c *WSClient) loop() {
	for {
		c.connect()

		// If connect returns, it means we disconnected. Wait a bit and retry.
		select {
		case <-c.done:
			return
		case <-time.After(5 * time.Second):
			log.Println("WS Client: Attempting reconnection...")
			continue
		}
	}
}

func (c *WSClient) connect() {
	u := url.URL{Scheme: "ws", Host: c.hostAddr, Path: "/ws"}
	log.Printf("WS Client: Connecting to %s", u.String())

	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		log.Printf("WS Client: Connection failed: %v", err)
		return
	}
	defer conn.Close()

	c.mu.Lock()
	c.conn = conn
	c.isConnected = true
	c.mu.Unlock()

	log.Println("WS Client: Connected to Host")

	// Send Auth/Handshake immediately
	// For now we assume open or token header, but let's send an Identify if needed.
	// We'll immediately request Sync as well.
	c.SendSyncRequest()

	// Start read/write pumps
	// specific done channel for this connection
	connDone := make(chan struct{})

	go func() {
		defer close(connDone)
		c.writePump(conn)
	}()

	c.readPump(conn)

	// Cleanup
	c.mu.Lock()
	c.isConnected = false
	c.conn = nil
	c.mu.Unlock()

	// Ensure write pump stops
	<-connDone
}

func (c *WSClient) readPump(conn *websocket.Conn) {
	conn.SetReadLimit(4096)
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error { conn.SetReadDeadline(time.Now().Add(60 * time.Second)); return nil })

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("WS Client: Read error: %v", err)
			}
			break
		}

		var msg protocol.Message
		if err := json.Unmarshal(data, &msg); err != nil {
			log.Printf("WS Client: Invalid message: %v", err)
			continue
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
			jsonMsg, err := json.Marshal(msg)
			if err != nil {
				log.Printf("WS Client: Marshal error: %v", err)
				continue
			}
			if err := conn.WriteMessage(websocket.TextMessage, jsonMsg); err != nil {
				log.Printf("WS Client: Write error: %v", err)
				return
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

func (c *WSClient) handleMessage(msg protocol.Message) {
	switch msg.Type {
	case protocol.TypeSwitch:
		var payload protocol.SwitchPayload
		bytes, _ := json.Marshal(msg.Payload)
		json.Unmarshal(bytes, &payload)

		log.Printf("WS Client: Received switch command for '%s'", payload.Profile)
		if c.OnSwitch != nil {
			c.OnSwitch(payload.Profile)
		}

	case protocol.TypeSyncResponse:
		var payload protocol.SyncResponsePayload
		bytes, _ := json.Marshal(msg.Payload)
		json.Unmarshal(bytes, &payload)

		log.Printf("WS Client: Received config sync")
		if c.OnSync != nil {
			c.OnSync(payload.Profiles)
		}

	case protocol.TypeInput:
		var payload protocol.InputPayload
		bytes, _ := json.Marshal(msg.Payload)
		json.Unmarshal(bytes, &payload)

		log.Printf("WS Client: Received input event: %s (dx:%d, dy:%d, btn:%d, pressed:%v, key:0x%X)",
			payload.Type, payload.DeltaX, payload.DeltaY, payload.Button, payload.Pressed, payload.KeyCode)
		if c.OnInput != nil {
			c.OnInput(
				payload.Type,
				payload.DeltaX, payload.DeltaY,
				payload.Button, payload.Pressed,
				payload.KeyCode, payload.Modifiers,
				payload.Timestamp,
			)
			log.Printf("WS Client: Input event handler executed successfully")
		} else {
			log.Printf("WS Client: No input event handler registered")
		}
	}
}

// SendSwitch sends a switch request to host
func (c *WSClient) SendSwitch(profile string) {
	c.send <- protocol.Message{
		Type: protocol.TypeSwitch,
		Payload: protocol.SwitchPayload{
			Profile: profile,
			Origin:  "agent", // Host will replace with actual IP if needed
		},
	}
}

// SendSyncRequest asks host for config
func (c *WSClient) SendSyncRequest() {
	c.send <- protocol.Message{
		Type:    protocol.TypeSyncRequest,
		Payload: nil,
	}
}

// SendInputEvent sends keyboard/mouse input events to host
func (c *WSClient) SendInputEvent(eventType string, deltaX, deltaY int, button int, pressed bool, keyCode uint16, modifiers uint16, timestamp int64) {
	c.send <- protocol.Message{
		Type: protocol.TypeInput,
		Payload: protocol.InputPayload{
			Type:      eventType,
			DeltaX:    deltaX,
			DeltaY:    deltaY,
			Button:    button,
			Pressed:   pressed,
			KeyCode:   keyCode,
			Modifiers: modifiers,
			Timestamp: timestamp,
		},
	}
}

// IsConnected returns true if client is connected to host
func (c *WSClient) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.isConnected
}

// Close stops the client
func (c *WSClient) Close() {
	close(c.done)
}
