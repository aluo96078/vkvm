package input

import (
	"sync"

	"vkvm/internal/protocol"
)

// InjectionQueue serializes all remote input before it reaches the platform
// injector. Mouse motion may be coalesced while key/button transitions remain
// ordered and bounded. A session/sequence frontier rejects stale packets even
// when two transports overlap during a handover.
type InjectionQueue struct {
	mu            sync.Mutex
	cond          *sync.Cond
	queue         []protocol.InputPayload
	processing    int
	capacity      int
	closed        bool
	lastSession   string
	lastSeq       uint32
	activeSession string
	sessionLocked bool
	handler       func(protocol.InputPayload)
	done          chan struct{}
}

// SetActiveSession advances the explicit transport handover fence. Once a
// session is selected, packets from any other session are rejected until the
// caller deliberately selects another one. Passing an empty ID reopens the
// fence for the next authenticated session (used only before a handshake).
func (q *InjectionQueue) SetActiveSession(sessionID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.activeSession = sessionID
	q.sessionLocked = sessionID != ""
	q.lastSession = sessionID
	q.lastSeq = 0
}

func NewInjectionQueue(capacity int, handler func(protocol.InputPayload)) *InjectionQueue {
	if capacity < 64 {
		capacity = 64
	}
	q := &InjectionQueue{capacity: capacity, queue: make([]protocol.InputPayload, 0, capacity), handler: handler, done: make(chan struct{})}
	q.cond = sync.NewCond(&q.mu)
	go q.run()
	return q
}

// Submit queues an input payload. It returns false for stale, closed, or
// overflowing best-effort motion packets; critical transitions wait briefly
// for space and are never silently evicted in favour of newer motion.
func (q *InjectionQueue) Submit(payload protocol.InputPayload) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return false
	}
	if payload.SessionID != "" {
		if q.sessionLocked && payload.SessionID != q.activeSession {
			return false
		}
		if !q.sessionLocked {
			q.activeSession = payload.SessionID
			q.sessionLocked = true
			q.lastSession = payload.SessionID
			q.lastSeq = 0
		}
		if payload.SessionID != q.lastSession {
			return false
		}
	}
	if payload.Sequence != 0 && payload.Sequence <= q.lastSeq {
		return false
	}
	if payload.Sequence != 0 {
		q.lastSeq = payload.Sequence
	}
	critical := payload.Type != "mouse_move"
	for len(q.queue) >= q.capacity {
		if !critical {
			return false
		}
		q.cond.Wait()
		if q.closed {
			return false
		}
	}
	if !critical && len(q.queue) > 0 {
		last := &q.queue[len(q.queue)-1]
		if last.Type == "mouse_move" && last.SessionID == payload.SessionID {
			last.DeltaX += payload.DeltaX
			last.DeltaY += payload.DeltaY
			last.Timestamp = payload.Timestamp
			last.ClockNS = payload.ClockNS
			last.Sequence = payload.Sequence
			return true
		}
	}
	q.queue = append(q.queue, payload)
	q.cond.Signal()
	return true
}

// Flush waits until all queued events have reached the handler. It is used
// before a state snapshot/release-all so a transport handover cannot reorder a
// release behind an older input callback.
func (q *InjectionQueue) Flush() {
	q.mu.Lock()
	defer q.mu.Unlock()
	for (len(q.queue) > 0 || q.processing > 0) && !q.closed {
		q.cond.Wait()
	}
}

func (q *InjectionQueue) Depth() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.queue)
}

func (q *InjectionQueue) Close() {
	q.mu.Lock()
	if !q.closed {
		q.closed = true
		q.cond.Broadcast()
		close(q.done)
	}
	q.mu.Unlock()
}

func (q *InjectionQueue) run() {
	for {
		q.mu.Lock()
		for len(q.queue) == 0 && !q.closed {
			q.cond.Wait()
		}
		if len(q.queue) == 0 && q.closed {
			q.mu.Unlock()
			return
		}
		payload := q.queue[0]
		q.queue[0] = protocol.InputPayload{}
		q.queue = q.queue[1:]
		q.processing++
		q.cond.Broadcast()
		handler := q.handler
		q.mu.Unlock()
		if handler != nil {
			handler(payload)
		}
		q.mu.Lock()
		q.processing--
		q.cond.Broadcast()
		q.mu.Unlock()
	}
}
