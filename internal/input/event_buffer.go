package input

import "sync"

// EventBuffer preserves critical transitions while allowing stale mouse
// motion to be coalesced under load. Critical Push calls wait for capacity
// instead of silently dropping key-up/button-up transitions.
type EventBuffer struct {
	mu       sync.Mutex
	cond     *sync.Cond
	queue    []InputEvent
	capacity int
	out      chan InputEvent
	closed   bool
}

func NewEventBuffer(capacity int) *EventBuffer {
	if capacity < 64 {
		capacity = 64
	}
	b := &EventBuffer{capacity: capacity, queue: make([]InputEvent, 0, capacity), out: make(chan InputEvent, capacity)}
	b.cond = sync.NewCond(&b.mu)
	go b.run()
	return b
}

func (b *EventBuffer) Events() <-chan InputEvent { return b.out }

func (b *EventBuffer) Push(event InputEvent, critical bool) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for {
		if b.closed {
			return false
		}
		if len(b.queue) < b.capacity {
			if !critical && event.Type == "mouse_move" && len(b.queue) > 0 {
				last := &b.queue[len(b.queue)-1]
				if last.Type == "mouse_move" {
					last.DeltaX += event.DeltaX
					last.DeltaY += event.DeltaY
					last.Timestamp = event.Timestamp
					last.ClockNS = event.ClockNS
					return true
				}
			}
			b.queue = append(b.queue, event)
			b.cond.Signal()
			return true
		}
		if !critical {
			return false
		}
		// Wait until the dispatcher removes an event, or Close broadcasts a
		// wake-up. This is intentionally back-pressure for state transitions.
		b.cond.Wait()
	}
}

func (b *EventBuffer) Close() {
	b.mu.Lock()
	if !b.closed {
		b.closed = true
		b.cond.Broadcast()
	}
	b.mu.Unlock()
}

func (b *EventBuffer) run() {
	for {
		b.mu.Lock()
		for len(b.queue) == 0 && !b.closed {
			b.cond.Wait()
		}
		if len(b.queue) == 0 && b.closed {
			b.mu.Unlock()
			close(b.out)
			return
		}
		event := b.queue[0]
		b.queue[0] = InputEvent{}
		b.queue = b.queue[1:]
		b.cond.Broadcast()
		b.mu.Unlock()
		b.out <- event
	}
}
