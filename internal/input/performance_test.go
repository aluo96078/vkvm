package input

import (
	"testing"
	"time"

	"vkvm/internal/protocol"
)

func BenchmarkInputMetricsPayload(b *testing.B) {
	m := NewMetrics(4096)
	payload := protocol.InputPayload{DeviceID: "host", SessionID: "session", Sequence: 1, Timestamp: time.Now().UnixMilli(), Type: "mouse_move", DeltaX: 1, DeltaY: -1}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		payload.Sequence++
		m.ObservePayload(payload)
	}
}

func BenchmarkInjectionQueueSubmit(b *testing.B) {
	q := NewInjectionQueue(1024, func(protocol.InputPayload) {})
	defer q.Close()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q.Submit(protocol.InputPayload{Type: "mouse_move", DeltaX: 1, Sequence: uint32(i + 1), SessionID: "bench"})
	}
}
