package input

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"vkvm/internal/protocol"
)

func TestMetricsSnapshotPercentiles(t *testing.T) {
	m := NewMetrics(64)
	for i := 0; i < 10; i++ {
		m.ObserveReceived(time.Now().Add(-time.Duration(i+1) * time.Millisecond).UnixMilli())
	}
	m.ObserveInjected(time.Now().Add(-2 * time.Microsecond))
	m.RecordDropped()
	m.SetQueueDepth(7)
	snapshot := m.Snapshot()
	if snapshot.Received != 10 || snapshot.Injected != 1 || snapshot.Dropped != 1 || snapshot.QueueDepth != 7 {
		t.Fatalf("unexpected metrics counters: %+v", snapshot)
	}
	if snapshot.ReceiveSamples == 0 || snapshot.ReceiveP50Micros <= 0 || snapshot.ReceiveP99Micros < snapshot.ReceiveP50Micros {
		t.Fatalf("unexpected latency percentiles: %+v", snapshot)
	}
}

func TestMetricsTracksSequenceLossAndJitter(t *testing.T) {
	m := NewMetrics(64)
	for _, seq := range []uint32{1, 3, 4} {
		m.ObservePayload(protocol.InputPayload{DeviceID: "host", SessionID: "s", Sequence: seq, Timestamp: time.Now().UnixMilli()})
	}
	snapshot := m.Snapshot()
	if snapshot.PacketLost != 1 {
		t.Fatalf("packet loss = %d, want 1", snapshot.PacketLost)
	}
	if snapshot.ReceiveJitterP95Micros < 0 {
		t.Fatalf("invalid jitter = %d", snapshot.ReceiveJitterP95Micros)
	}
}

func TestMetricsTracksClockOffsetAndAcceptanceThresholds(t *testing.T) {
	m := NewMetrics(64)
	for seq := uint32(1); seq <= 4; seq++ {
		m.ObservePayload(protocol.InputPayload{
			DeviceID: "host", SessionID: "clock", Sequence: seq,
			Timestamp: time.Now().UnixMilli(), ClockNS: time.Now().Add(-time.Millisecond).UnixNano(),
		})
	}
	snapshot := m.Snapshot()
	if snapshot.ClockOffsetSamples == 0 || snapshot.ClockOffsetP95Micros <= 0 {
		t.Fatalf("clock offset samples missing: %+v", snapshot)
	}
	if err := snapshot.Validate(MetricThresholds{MaxClockOffsetP95Micros: snapshot.ClockOffsetP99Micros + 1}); err != nil {
		t.Fatalf("valid metric threshold rejected: %v", err)
	}
	if err := snapshot.Validate(MetricThresholds{MaxClockOffsetP95Micros: 1}); err == nil {
		t.Fatal("overly strict clock threshold accepted")
	}
}

func TestMetricsSaveBaselineIsAtomicAndPrivate(t *testing.T) {
	m := NewMetrics(64)
	m.ObserveInjected(time.Now().Add(-time.Microsecond))
	path := filepath.Join(t.TempDir(), "baseline.json")
	if err := m.SaveBaseline(path, "test"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("baseline mode = %o", info.Mode().Perm())
	}
	if _, err := os.ReadFile(path); err != nil {
		t.Fatal(err)
	}
}
