package input

import (
	"testing"
	"time"

	"vkvm/internal/protocol"
)

func TestInjectionQueueCoalescesMotionAndRejectsStaleSequence(t *testing.T) {
	got := make(chan protocol.InputPayload, 4)
	q := NewInjectionQueue(64, func(payload protocol.InputPayload) { got <- payload })
	defer q.Close()
	if !q.Submit(protocol.InputPayload{Type: "mouse_move", DeltaX: 1, Sequence: 1, SessionID: "s"}) ||
		!q.Submit(protocol.InputPayload{Type: "mouse_move", DeltaX: 2, Sequence: 2, SessionID: "s"}) {
		t.Fatal("motion submit failed")
	}
	if q.Submit(protocol.InputPayload{Type: "mouse_move", DeltaX: 99, Sequence: 2, SessionID: "s"}) {
		t.Fatal("stale sequence accepted")
	}
	select {
	case payload := <-got:
		if payload.DeltaX != 3 || payload.Sequence != 2 {
			t.Fatalf("coalesced payload = %+v", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("queue did not dispatch motion")
	}
}

func TestInjectionQueueKeepsCriticalTransitionsOrdered(t *testing.T) {
	got := make(chan protocol.InputPayload, 2)
	q := NewInjectionQueue(64, func(payload protocol.InputPayload) { got <- payload })
	defer q.Close()
	q.Submit(protocol.InputPayload{Type: "key", KeyCode: 30, Pressed: true, Sequence: 1, SessionID: "s"})
	q.Submit(protocol.InputPayload{Type: "key", KeyCode: 30, Pressed: false, Sequence: 2, SessionID: "s"})
	first, second := <-got, <-got
	if !first.Pressed || second.Pressed || first.Sequence >= second.Sequence {
		t.Fatalf("critical ordering lost: first=%+v second=%+v", first, second)
	}
}

func TestInjectionQueueRejectsOldSessionAfterExplicitHandover(t *testing.T) {
	q := NewInjectionQueue(64, nil)
	defer q.Close()
	q.SetActiveSession("new")
	if !q.Submit(protocol.InputPayload{Type: "key", Sequence: 1, SessionID: "new"}) {
		t.Fatal("active session was rejected")
	}
	if q.Submit(protocol.InputPayload{Type: "key", Sequence: 99, SessionID: "old"}) {
		t.Fatal("old session crossed handover fence")
	}
}
