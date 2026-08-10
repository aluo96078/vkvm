package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"vkvm/internal/config"
	"vkvm/internal/protocol"
)

func TestServerCanRestartAfterSupervisorStop(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.General.APIToken = "restart-secret"
	m, err := config.NewManagerAt(t.TempDir() + "/config.json")
	if err != nil {
		t.Fatal(err)
	}
	m.Set(cfg)
	s := NewServer(m, nil)

	start := func() <-chan error {
		done := make(chan error, 1)
		go func() { done <- s.Start(0) }()
		deadline := time.Now().Add(time.Second)
		for !s.Healthy() && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if !s.Healthy() {
			select {
			case err := <-done:
				if strings.Contains(strings.ToLower(err.Error()), "operation not permitted") {
					t.Skipf("sandbox disallows TCP listeners: %v", err)
				}
				t.Fatalf("API server failed before becoming healthy: %v", err)
			default:
				t.Fatal("API server did not become healthy")
			}
		}
		return done
	}

	first := start()
	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-first:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("first API server run did not stop")
	}

	second := start()
	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-second:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("restarted API server run did not stop")
	}
}

func TestWSManagerCanRestartAfterStop(t *testing.T) {
	m := newWSManager(nil)
	m.startAsync()
	deadline := time.Now().Add(time.Second)
	for m.isStopped() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if m.isStopped() {
		t.Fatal("WebSocket manager did not start")
	}
	m.stop()
	if !m.isStopped() {
		t.Fatal("WebSocket manager did not stop")
	}
	m.startAsync()
	deadline = time.Now().Add(time.Second)
	for m.isStopped() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if m.isStopped() {
		t.Fatal("WebSocket manager did not restart")
	}
	m.stop()
}

func TestValidBearerToken(t *testing.T) {
	tests := []struct {
		name, header, token string
		want                bool
	}{
		{"valid", "Bearer secret", "secret", true},
		{"wrong token", "Bearer other", "secret", false},
		{"missing scheme", "secret", "secret", false},
		{"empty token", "Bearer ", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validBearerToken(tt.header, tt.token); got != tt.want {
				t.Fatalf("validBearerToken(%q, %q) = %v, want %v", tt.header, tt.token, got, tt.want)
			}
		})
	}
}

func TestPairingEndpointRequiresBearerAndPersistsDevice(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.General.APIToken = "secret"
	m, err := config.NewManagerAt(t.TempDir() + "/config.json")
	if err != nil {
		t.Fatal(err)
	}
	m.Set(cfg)
	s := NewServer(m, nil)
	handler := s.authMiddleware(http.HandlerFunc(s.handlePairing))
	body, _ := json.Marshal(config.PairedDevice{DeviceID: "agent-1", Name: "Agent"})
	req := httptest.NewRequest(http.MethodPost, "/api/pairing", bytes.NewReader(body))
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", resp.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/pairing", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	resp = httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK || !m.IsPaired("agent-1") {
		t.Fatalf("pairing status=%d body=%s paired=%v", resp.Code, resp.Body.String(), m.IsPaired("agent-1"))
	}
}

func TestWebSocketSessionMACBindsPairedIdentity(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.General.APIToken = "secret"
	cfg.General.PairedDevices = []config.PairedDevice{{DeviceID: "agent-1", Enabled: true}}
	m, err := config.NewManagerAt(t.TempDir() + "/config.json")
	if err != nil {
		t.Fatal(err)
	}
	m.Set(cfg)
	s := NewServer(m, nil)
	client := &WebSocketClient{manager: s.wsMgr, ip: "test", done: make(chan struct{})}
	payload := protocol.AuthPayload{DeviceID: "agent-1", SessionID: "session-1", ProtocolVersion: protocol.ProtocolVersion}
	msg := protocol.Message{Type: protocol.TypeAuth, Payload: payload}
	key := protocol.DeriveSessionKey("secret", payload.DeviceID, payload.SessionID)
	if err := protocol.SignMessageInPlace(&msg, key); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(msg)
	client.handleMessage(data)
	client.identityMu.RLock()
	authenticated := client.authenticated
	client.identityMu.RUnlock()
	if !authenticated {
		t.Fatal("valid session MAC did not authenticate the paired client")
	}

	bad := protocol.Message{Type: protocol.TypeSyncRequest, Payload: nil, MAC: msg.MAC}
	if client.verifyMessage(bad) {
		t.Fatal("MAC from auth message verified a different payload")
	}
}

func TestConfigSnapshotNeverReturnsIdentityPrivateKey(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.General.APIToken = "secret"
	m, err := config.NewManagerAt(t.TempDir() + "/config.json")
	if err != nil {
		t.Fatal(err)
	}
	m.Set(cfg)
	s := NewServer(m, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	resp := httptest.NewRecorder()
	s.handleConfig(resp, req)
	var got config.Config
	if err := json.Unmarshal(resp.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.General.IdentityPrivateKey != "" {
		t.Fatal("config snapshot exposed identity private key")
	}
}

func TestPerAgentTransportSelectionSupportsMixedMachines(t *testing.T) {
	m := &WSManager{udpSelector: func(deviceID string) bool { return deviceID == "udp-agent" }}
	udpClient := &WebSocketClient{manager: m, deviceID: "udp-agent", authenticated: true, capabilities: protocol.CapabilityUDP}
	wsClient := &WebSocketClient{manager: m, deviceID: "ws-agent", authenticated: true, capabilities: protocol.CapabilityWebSocket}
	if !udpClient.usesUDP() || wsClient.usesUDP() {
		t.Fatalf("mixed transport selection udp=%v ws=%v", udpClient.usesUDP(), wsClient.usesUDP())
	}
}

func TestBroadcastInputReachesUDPAndWebSocketAgentsIndependently(t *testing.T) {
	m := &WSManager{
		clients:     make(map[*WebSocketClient]bool),
		udpSelector: func(deviceID string) bool { return deviceID == "udp-agent" },
	}
	udpClient := &WebSocketClient{manager: m, deviceID: "udp-agent", capabilities: protocol.CapabilityUDP, send: make(chan []byte, 1), done: make(chan struct{})}
	wsClient := &WebSocketClient{manager: m, deviceID: "ws-agent", capabilities: protocol.CapabilityWebSocket, send: make(chan []byte, 1), done: make(chan struct{})}
	m.clients[udpClient] = true
	m.clients[wsClient] = true
	m.broadcastMessage(protocol.Message{Type: protocol.TypeInput, Payload: protocol.InputPayload{Type: "key", KeyCode: 0x41}})
	select {
	case <-udpClient.send:
		t.Fatal("UDP-capable agent also received a duplicate WebSocket input")
	default:
	}
	select {
	case <-wsClient.send:
	case <-time.After(time.Second):
		t.Fatal("WebSocket-only agent did not receive input")
	}
}

func TestBroadcastSwitchReachesMultipleAuthenticatedAgents(t *testing.T) {
	m := &WSManager{clients: make(map[*WebSocketClient]bool), broadcast: make(chan protocol.Message, 4), shutdown: make(chan struct{})}
	clients := make([]*WebSocketClient, 0, 2)
	for _, id := range []string{"agent-a", "agent-b"} {
		client := &WebSocketClient{manager: m, deviceID: id, authenticated: true, capabilities: protocol.CapabilityWebSocket, send: make(chan []byte, 1), done: make(chan struct{})}
		clients = append(clients, client)
		m.clients[client] = true
	}
	m.BroadcastSwitch("Mac", "host")
	// BroadcastSwitch is asynchronous through the hub queue in production; run
	// one hub dispatch directly for this deterministic multi-peer harness.
	message := <-m.broadcast
	m.broadcastMessage(message)
	for i, client := range clients {
		select {
		case data := <-client.send:
			var got protocol.Message
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			payload, ok := got.Payload.(map[string]interface{})
			if !ok || payload["profile"] != "Mac" {
				t.Fatalf("agent %d switch payload=%+v", i, got.Payload)
			}
		case <-time.After(time.Second):
			t.Fatalf("agent %d did not receive switch", i)
		}
	}
}
