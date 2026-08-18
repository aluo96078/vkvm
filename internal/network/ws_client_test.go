package network

import "testing"

func TestSendSwitchRequiresConnectedTransport(t *testing.T) {
	client := NewWSClient("192.0.2.1:18080", "test-token", "agent-1")
	defer client.Close()

	if err := client.SendSwitch("PC1"); err == nil {
		t.Fatal("SendSwitch succeeded while the WebSocket was disconnected")
	}
}
