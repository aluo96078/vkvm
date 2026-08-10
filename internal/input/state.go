package input

import "sync"

// KeyState identifies a key held by the forwarding state machine. ScanCode
// plus the extended bit distinguishes the left/right and E0 variants that
// share a virtual-key code.
type KeyState struct {
	KeyCode  uint16 `json:"keycode"`
	ScanCode uint16 `json:"scan_code,omitempty"`
	Extended bool   `json:"extended,omitempty"`
	E1       bool   `json:"e1,omitempty"`
}

type StateSnapshot struct {
	Keys      []KeyState `json:"keys,omitempty"`
	Buttons   []int      `json:"buttons,omitempty"`
	Modifiers uint16     `json:"modifiers,omitempty"`
}

// StateTracker keeps the last known physical state independently of any
// transport. It is used to recover from packet loss, reconnects and transport
// switches without synthesising a guessed modifier state.
type StateTracker struct {
	mu        sync.RWMutex
	keys      map[KeyState]struct{}
	buttons   map[int]struct{}
	modifiers uint16
}

func NewStateTracker() *StateTracker {
	return &StateTracker{keys: make(map[KeyState]struct{}), buttons: make(map[int]struct{})}
}

func (s *StateTracker) Apply(event InputEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch event.Type {
	case "key":
		// Mouse motion/button reports do not necessarily carry a modifier
		// snapshot. Only keyboard transitions are authoritative for the
		// modifier frontier; otherwise an ordinary mouse move while Shift is
		// held would erase the state used by recovery snapshots.
		s.modifiers = event.Modifiers
		key := KeyState{KeyCode: event.KeyCode, ScanCode: event.ScanCode, Extended: event.Extended, E1: event.E1}
		if event.Pressed {
			s.keys[key] = struct{}{}
		} else {
			delete(s.keys, key)
		}
	case "mouse_btn":
		if event.Pressed {
			s.buttons[event.Button] = struct{}{}
		} else {
			delete(s.buttons, event.Button)
		}
	}
}

func (s *StateTracker) Reset() {
	s.mu.Lock()
	s.keys = make(map[KeyState]struct{})
	s.buttons = make(map[int]struct{})
	s.modifiers = 0
	s.mu.Unlock()
}

func (s *StateTracker) Snapshot() StateSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := StateSnapshot{Modifiers: s.modifiers, Keys: make([]KeyState, 0, len(s.keys)), Buttons: make([]int, 0, len(s.buttons))}
	for key := range s.keys {
		out.Keys = append(out.Keys, key)
	}
	for button := range s.buttons {
		out.Buttons = append(out.Buttons, button)
	}
	return out
}
