package api

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"vkvm/internal/protocol"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// Allow all origins for now as this is a local network tool
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// WSManager handles WebSocket connections and broadcasting
type WSManager struct {
	server     *Server
	clients    map[*WebSocketClient]bool
	clientsMu  sync.RWMutex
	broadcast  chan protocol.Message
	register   chan *WebSocketClient
	unregister chan *WebSocketClient
	shutdown   chan struct{}
}

// WebSocketClient represents a connected agent
type WebSocketClient struct {
	manager *WSManager
	conn    *websocket.Conn
	send    chan []byte
	ip      string
}

func newWSManager(s *Server) *WSManager {
	return &WSManager{
		server:     s,
		clients:    make(map[*WebSocketClient]bool),
		broadcast:  make(chan protocol.Message),
		register:   make(chan *WebSocketClient),
		unregister: make(chan *WebSocketClient),
		shutdown:   make(chan struct{}),
	}
}

func (m *WSManager) start() {
	for {
		select {
		case client := <-m.register:
			m.clientsMu.Lock()
			m.clients[client] = true
			m.clientsMu.Unlock()
			log.Printf("WS: New client registered from %s. Total clients: %d", client.ip, len(m.clients))

		case client := <-m.unregister:
			m.clientsMu.Lock()
			if _, ok := m.clients[client]; ok {
				delete(m.clients, client)
				close(client.send)
				log.Printf("WS: Client unregistered from %s. Total clients: %d", client.ip, len(m.clients))
			}
			m.clientsMu.Unlock()

		case message := <-m.broadcast:
			m.broadcastMessage(message)

		case <-m.shutdown:
			return
		}
	}
}

func (m *WSManager) broadcastMessage(message protocol.Message) {
	jsonMsg, err := json.Marshal(message)
	if err != nil {
		log.Printf("WS: Failed to marshal broadcast message: %v", err)
		return
	}

	m.clientsMu.RLock()
	defer m.clientsMu.RUnlock()

	for client := range m.clients {
		select {
		case client.send <- jsonMsg:
		default:
			close(client.send)
			delete(m.clients, client)
		}
	}
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
	}

	// Register client
	m.register <- client

	// Start pump goroutines
	go client.writePump()
	go client.readPump()
}

// readPump pumps messages from the websocket connection to the hub.
func (c *WebSocketClient) readPump() {
	defer func() {
		c.manager.unregister <- c
		c.conn.Close()
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
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				// The hub closed the channel.
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(message)

			if err := w.Close(); err != nil {
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (c *WebSocketClient) handleMessage(data []byte) {
	var msg protocol.Message
	if err := json.Unmarshal(data, &msg); err != nil {
		log.Printf("WS: Invalid message format: %v", err)
		return
	}

	switch msg.Type {
	case protocol.TypeAuth:
		// TODO: Handle authentication if needed
		// For now we might just log it or verify token if present in payload
		log.Printf("WS: Received auth from client")

	case protocol.TypeSwitch:
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
		// Send config back
		cfg := c.manager.server.configMgr.Get()
		resp := protocol.Message{
			Type: protocol.TypeSyncResponse,
			Payload: protocol.SyncResponsePayload{
				Profiles: cfg.Profiles,
			},
		}

		respBytes, _ := json.Marshal(resp)
		c.send <- respBytes
	}
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
	m.broadcast <- msg
}

// Public method to broadcast input events from the Host to all Agents
func (m *WSManager) BroadcastInput(eventType string, deltaX, deltaY int, button int, pressed bool, keyCode uint16, modifiers uint16, wheelDelta int, timestamp int64) {
	msg := protocol.Message{
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
	}
	m.broadcast <- msg
}
