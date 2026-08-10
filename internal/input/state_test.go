package input

import "testing"

func TestStateTrackerPreservesPhysicalKeyVariants(t *testing.T) {
	tracker := NewStateTracker()
	tracker.Apply(InputEvent{Type: "key", KeyCode: 0x10, ScanCode: 0x2A, Pressed: true, Modifiers: 1})
	tracker.Apply(InputEvent{Type: "key", KeyCode: 0x10, ScanCode: 0x36, Extended: true, Pressed: true, Modifiers: 1})
	snapshot := tracker.Snapshot()
	if len(snapshot.Keys) != 2 {
		t.Fatalf("snapshot keys = %+v", snapshot.Keys)
	}
	tracker.Apply(InputEvent{Type: "key", KeyCode: 0x10, ScanCode: 0x2A, Pressed: false, Modifiers: 1})
	if got := len(tracker.Snapshot().Keys); got != 1 {
		t.Fatalf("after releasing one variant got %d keys", got)
	}
}

func TestStateTrackerResetClearsButtonsAndModifiers(t *testing.T) {
	tracker := NewStateTracker()
	tracker.Apply(InputEvent{Type: "mouse_btn", Button: 1, Pressed: true})
	tracker.Apply(InputEvent{Type: "key", KeyCode: 0x11, Pressed: true, Modifiers: 2})
	tracker.Reset()
	got := tracker.Snapshot()
	if len(got.Keys) != 0 || len(got.Buttons) != 0 || got.Modifiers != 0 {
		t.Fatalf("reset snapshot = %+v", got)
	}
}

func TestStateTrackerKeepsModifiersAcrossMouseMotion(t *testing.T) {
	tracker := NewStateTracker()
	tracker.Apply(InputEvent{Type: "key", KeyCode: 0x10, ScanCode: 0x2A, Pressed: true, Modifiers: 1})
	tracker.Apply(InputEvent{Type: "mouse_move", DeltaX: 4, DeltaY: -2})
	if got := tracker.Snapshot().Modifiers; got != 1 {
		t.Fatalf("mouse motion cleared modifier state: %d", got)
	}
}
