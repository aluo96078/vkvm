package protocol

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
)

// Message is the generic container for all WebSocket messages
type Message struct {
	Type    MessageType     `json:"type"`
	Payload interface{}     `json:"payload,omitempty"`
}

// AuthPayload is the payload for TypeAuth
type AuthPayload struct {
	Token       string `json:"token"`
	AgentName   string `json:"agent_name"`
	AgentVersion string `json:"agent_version"`
}

// SwitchPayload is the payload for TypeSwitch
type SwitchPayload struct {
	Profile   string `json:"profile"`
	Origin    string `json:"origin"`    // "host" or agent ID/IP
	Propagate bool   `json:"propagate"` // Whether receivers should propagate further (usually false for broadcasts)
}

// SyncResponsePayload is the payload for TypeSyncResponse
type SyncResponsePayload struct {
	Profiles interface{} `json:"profiles"` // Using interface{} to avoid circular dependency with config package if possible, or we will move this to a shared location
}
