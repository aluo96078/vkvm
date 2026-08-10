package protocol

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// MessageType defines the type of WebSocket message
type MessageType string

const (
	// TypeAuth is sent by client immediately after connection to authenticate
	TypeAuth MessageType = "auth"

	// TypeSwitch is sent to request a switch or notify of a switch
	TypeSwitch MessageType = "switch"

	// TypeSyncRequest is sent by client to request full config
	TypeSyncRequest MessageType = "sync_req"

	// TypeSyncResponse is sent by server with full config
	TypeSyncResponse MessageType = "sync_resp"

	// TypePing can be used for application-level heartbeats if needed
	TypePing MessageType = "ping"

	// TypeInput is sent to transmit keyboard and mouse input events
	TypeInput MessageType = "input"

	// TypeInputState carries a complete pressed-key/button snapshot after a
	// transport or session transition.
	TypeInputState MessageType = "input_state"

	// TypeUSBForwardingUpdate is sent to notify USB forwarding setting changes
	TypeUSBForwardingUpdate MessageType = "usb_forwarding_update"
	TypeReleaseAll          MessageType = "release_all"
)

// Message is the generic container for all WebSocket messages
type Message struct {
	Type    MessageType `json:"type"`
	Payload interface{} `json:"payload,omitempty"`
	// MAC authenticates the complete message body with the per-session key.
	// It is intentionally separate from the HTTP Bearer credential so a
	// captured authenticated connection cannot be replayed on another session.
	MAC string `json:"mac,omitempty"`
}

const MessageMACSize = 16

// DeriveSessionKey binds a transport session to the shared credential and the
// persistent peer/session identities. The returned key is never sent on wire.
func DeriveSessionKey(token, deviceID, sessionID string) []byte {
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = mac.Write([]byte("vkvm-session-v2\x00"))
	_, _ = mac.Write([]byte(deviceID))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(sessionID))
	return mac.Sum(nil)
}

type messageMACBody struct {
	Type    MessageType     `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

func messageMACBytes(msg Message) ([]byte, error) {
	var payload json.RawMessage
	var err error
	if msg.Payload != nil {
		payload, err = canonicalJSON(msg.Payload)
		if err != nil {
			return nil, err
		}
	}
	return json.Marshal(messageMACBody{Type: msg.Type, Payload: payload})
}

// canonicalJSON normalizes typed structs and decoded map payloads to the same
// deterministic JSON representation. WebSocket peers decode Payload into
// interface{} before verification, so signing the original struct field order
// would otherwise produce a different MAC on the receiving side.
func canonicalJSON(value interface{}) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var normalized interface{}
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return nil, err
	}
	return json.Marshal(normalized)
}

// SignMessage returns a compact hex HMAC for the message body. The MAC field
// itself is excluded from the signed bytes to avoid recursive encoding.
func SignMessage(msg Message, key []byte) (string, error) {
	if len(key) == 0 {
		return "", nil
	}
	body, err := messageMACBytes(msg)
	if err != nil {
		return "", err
	}
	h := hmac.New(sha256.New, key)
	_, _ = h.Write(body)
	return hex.EncodeToString(h.Sum(nil)[:MessageMACSize]), nil
}

// SignMessageInPlace authenticates a message before it is queued for wire
// transmission.
func SignMessageInPlace(msg *Message, key []byte) error {
	if msg == nil {
		return nil
	}
	mac, err := SignMessage(*msg, key)
	if err != nil {
		return err
	}
	msg.MAC = mac
	return nil
}

// VerifyMessage checks a message against one session key using constant-time
// comparison. Empty keys/MACs are rejected for protocol v2 sessions.
func VerifyMessage(msg Message, key []byte) bool {
	if len(key) == 0 || msg.MAC == "" {
		return false
	}
	expected, err := SignMessage(msg, key)
	if err != nil || len(expected) != len(msg.MAC) {
		return false
	}
	presented, err := hex.DecodeString(msg.MAC)
	if err != nil {
		return false
	}
	want, err := hex.DecodeString(expected)
	if err != nil || len(presented) != len(want) {
		return false
	}
	return hmac.Equal(presented, want)
}

// VerifyMessageAny accepts the current key and the bounded token-rotation
// grace keys. It returns the key that authenticated the message.
func VerifyMessageAny(msg Message, keys [][]byte) ([]byte, bool) {
	for _, key := range keys {
		if VerifyMessage(msg, key) {
			return append([]byte(nil), key...), true
		}
	}
	return nil, false
}

// AuthPayload is the payload for TypeAuth
type AuthPayload struct {
	Token           string `json:"token"`
	AgentName       string `json:"agent_name"`
	AgentVersion    string `json:"agent_version"`
	DeviceID        string `json:"device_id,omitempty"`
	PublicKey       string `json:"public_key,omitempty"`
	Role            string `json:"role,omitempty"`
	SessionID       string `json:"session_id,omitempty"`
	ProtocolVersion uint16 `json:"protocol_version,omitempty"`
	Capabilities    uint32 `json:"capabilities,omitempty"`
}

// ProtocolVersion is the wire version used by the metadata-aware transport.
// Older peers may omit this field and are handled only by the legacy APIs.
const ProtocolVersion uint16 = 2

// Capability bits are advertised during the authenticated handshake. They
// describe what a peer can safely receive; they are not authorization grants.
const (
	CapabilityInputMetadata uint32 = 1 << iota
	CapabilityReliableControl
	CapabilityMouseCoalescing
	CapabilityStateSnapshot
	CapabilityUDP
	CapabilityWebSocket
)

// SwitchPayload is the payload for TypeSwitch
type SwitchPayload struct {
	Profile   string `json:"profile"`
	Origin    string `json:"origin"`    // "host" or agent ID/IP
	Propagate bool   `json:"propagate"` // Whether receivers should propagate further (usually false for broadcasts)
}

// SyncResponsePayload is the payload for TypeSyncResponse
type SyncResponsePayload struct {
	Profiles             interface{} `json:"profiles"` // Using interface{} to avoid circular dependency with config package if possible, or we will move this to a shared location
	USBForwardingEnabled bool        `json:"usb_forwarding_enabled"`
	DeviceID             string      `json:"device_id,omitempty"`
	SessionID            string      `json:"session_id,omitempty"`
}

// InputPayload is the payload for TypeInput
type InputPayload struct {
	Type       string `json:"type"` // "mouse_move", "mouse_btn", "mouse_wheel", "key"
	DeltaX     int    `json:"dx,omitempty"`
	DeltaY     int    `json:"dy,omitempty"`
	Button     int    `json:"btn,omitempty"` // 1=left, 2=right, 3=middle, 4=xbutton1, 5=xbutton2
	Pressed    bool   `json:"pressed,omitempty"`
	KeyCode    uint16 `json:"keycode,omitempty"`
	Modifiers  uint16 `json:"modifiers,omitempty"`
	WheelDelta int    `json:"wheel_delta,omitempty"` // Positive=up/right, Negative=down/left
	Timestamp  int64  `json:"ts"`                    // Unix ms timestamp
	DeviceID   string `json:"device_id,omitempty"`
	HardwareID string `json:"hardware_id,omitempty"`
	ScanCode   uint16 `json:"scan_code,omitempty"`
	Extended   bool   `json:"extended,omitempty"`
	E1         bool   `json:"e1,omitempty"`
	Repeat     bool   `json:"repeat,omitempty"`
	Sequence   uint32 `json:"sequence,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	ClockNS    int64  `json:"clock_ns,omitempty"`
}

type InputStatePayload struct {
	DeviceID  string     `json:"device_id,omitempty"`
	SessionID string     `json:"session_id,omitempty"`
	Sequence  uint32     `json:"sequence,omitempty"`
	Keys      []KeyState `json:"keys,omitempty"`
	Buttons   []int      `json:"buttons,omitempty"`
	Modifiers uint16     `json:"modifiers,omitempty"`
}

type KeyState struct {
	KeyCode  uint16 `json:"keycode"`
	ScanCode uint16 `json:"scan_code,omitempty"`
	Extended bool   `json:"extended,omitempty"`
	E1       bool   `json:"e1,omitempty"`
}

// USBForwardingUpdatePayload is the payload for TypeUSBForwardingUpdate
type USBForwardingUpdatePayload struct {
	Enabled bool `json:"enabled"`
}
