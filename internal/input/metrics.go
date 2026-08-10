package input

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"vkvm/internal/protocol"
)

// Metrics collects bounded input-path observations. Samples are intentionally
// kept in memory only; the API exposes a snapshot for diagnostics without
// turning every input event into a disk write.
type Metrics struct {
	mu               sync.Mutex
	receiveLatencies []int64 // microseconds, bounded ring
	injectLatencies  []int64 // microseconds, bounded ring
	jitterSamples    []int64 // absolute change between adjacent receive latency samples
	clockOffsets     []int64 // absolute source-to-agent clock/path offset, microseconds
	receivePosition  int
	injectPosition   int
	jitterPosition   int
	clockPosition    int
	lastLatency      int64
	lastSequence     map[string]uint32
	received         atomic.Uint64
	injected         atomic.Uint64
	dropped          atomic.Uint64
	packetLost       atomic.Uint64
	queue            atomic.Int64
}

type MetricsSnapshot struct {
	Received               uint64 `json:"received"`
	Injected               uint64 `json:"injected"`
	Dropped                uint64 `json:"dropped"`
	QueueDepth             int64  `json:"queue_depth"`
	ReceiveSamples         uint64 `json:"receive_latency_samples"`
	ReceiveP50Micros       int64  `json:"receive_p50_us"`
	ReceiveP95Micros       int64  `json:"receive_p95_us"`
	ReceiveP99Micros       int64  `json:"receive_p99_us"`
	ReceiveJitterP95Micros int64  `json:"receive_jitter_p95_us"`
	ClockOffsetSamples     uint64 `json:"clock_offset_samples"`
	ClockOffsetP50Micros   int64  `json:"clock_offset_p50_us"`
	ClockOffsetP95Micros   int64  `json:"clock_offset_p95_us"`
	ClockOffsetP99Micros   int64  `json:"clock_offset_p99_us"`
	PacketLost             uint64 `json:"packet_lost"`
	InjectSamples          uint64 `json:"inject_latency_samples"`
	InjectP50Micros        int64  `json:"inject_p50_us"`
	InjectP95Micros        int64  `json:"inject_p95_us"`
	InjectP99Micros        int64  `json:"inject_p99_us"`
}

// MetricThresholds defines operator-selected real-device acceptance limits.
// Zero values leave a metric unbounded; portable runs must not invent these
// limits because U20 requires measuring them on the target hardware/network.
type MetricThresholds struct {
	MaxReceiveP95Micros     int64
	MaxReceiveP99Micros     int64
	MaxJitterP95Micros      int64
	MaxClockOffsetP95Micros int64
	MaxPacketLost           uint64
	MaxDropped              uint64
}

func (s MetricsSnapshot) Validate(thresholds MetricThresholds) error {
	checks := []struct {
		name string
		got  int64
		max  int64
	}{
		{"receive p95", s.ReceiveP95Micros, thresholds.MaxReceiveP95Micros},
		{"receive p99", s.ReceiveP99Micros, thresholds.MaxReceiveP99Micros},
		{"receive jitter p95", s.ReceiveJitterP95Micros, thresholds.MaxJitterP95Micros},
		{"clock offset p95", s.ClockOffsetP95Micros, thresholds.MaxClockOffsetP95Micros},
	}
	for _, check := range checks {
		if check.max > 0 && check.got > check.max {
			return fmt.Errorf("%s %d us exceeds %d us", check.name, check.got, check.max)
		}
	}
	if thresholds.MaxPacketLost > 0 && s.PacketLost > thresholds.MaxPacketLost {
		return fmt.Errorf("packet loss %d exceeds %d", s.PacketLost, thresholds.MaxPacketLost)
	}
	if thresholds.MaxDropped > 0 && s.Dropped > thresholds.MaxDropped {
		return fmt.Errorf("dropped events %d exceeds %d", s.Dropped, thresholds.MaxDropped)
	}
	return nil
}

// MetricBaseline is a portable, reviewable snapshot for performance
// acceptance. It is written only on explicit operator request; normal metrics
// collection remains memory bounded and does not perform disk I/O.
type MetricBaseline struct {
	CapturedAt time.Time       `json:"captured_at"`
	Platform   string          `json:"platform"`
	Metrics    MetricsSnapshot `json:"metrics"`
}

func (m *Metrics) SaveBaseline(path, platform string) error {
	if m == nil || path == "" {
		return fmt.Errorf("metrics and baseline path are required")
	}
	baseline := MetricBaseline{CapturedAt: time.Now().UTC(), Platform: platform, Metrics: m.Snapshot()}
	data, err := json.MarshalIndent(baseline, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".metrics-baseline-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func NewMetrics(capacity int) *Metrics {
	if capacity < 64 {
		capacity = 64
	}
	return &Metrics{
		receiveLatencies: make([]int64, capacity),
		injectLatencies:  make([]int64, capacity),
		jitterSamples:    make([]int64, capacity),
		clockOffsets:     make([]int64, capacity),
		lastSequence:     make(map[string]uint32),
	}
}

func (m *Metrics) ObserveReceived(timestamp int64) {
	m.received.Add(1)
	if timestamp <= 0 {
		return
	}
	delta := time.Since(time.UnixMilli(timestamp)).Microseconds()
	if delta < 0 {
		// Host and Agent clocks may not be synchronized. Do not report a
		// negative latency as a meaningful sample.
		return
	}
	m.mu.Lock()
	m.receiveLatencies[m.receivePosition] = delta
	m.receivePosition = (m.receivePosition + 1) % len(m.receiveLatencies)
	if m.lastLatency > 0 {
		jitter := delta - m.lastLatency
		if jitter < 0 {
			jitter = -jitter
		}
		m.jitterSamples[m.jitterPosition] = jitter
		m.jitterPosition = (m.jitterPosition + 1) % len(m.jitterSamples)
	}
	m.lastLatency = delta
	m.mu.Unlock()
}

// ObservePayload records a metadata-aware receive and counts sequence gaps
// within each authenticated session as a packet-loss diagnostic.
func (m *Metrics) ObservePayload(payload protocol.InputPayload) {
	m.ObserveReceived(payload.Timestamp)
	if payload.ClockNS > 0 {
		offset := (time.Now().UnixNano() - payload.ClockNS) / int64(time.Microsecond)
		if offset < 0 {
			offset = -offset
		}
		m.mu.Lock()
		m.clockOffsets[m.clockPosition] = offset
		m.clockPosition = (m.clockPosition + 1) % len(m.clockOffsets)
		m.mu.Unlock()
	}
	if payload.Sequence == 0 {
		return
	}
	key := payload.DeviceID + "\x00" + payload.SessionID
	m.mu.Lock()
	last := m.lastSequence[key]
	if last != 0 && payload.Sequence > last+1 {
		m.packetLost.Add(uint64(payload.Sequence - last - 1))
	}
	if payload.Sequence > last {
		m.lastSequence[key] = payload.Sequence
	}
	m.mu.Unlock()
}

func (m *Metrics) ObserveInjected(start time.Time) {
	m.injected.Add(1)
	if start.IsZero() {
		return
	}
	// Injection cost is a local operation and is kept separate from network
	// receive latency so the diagnostic percentiles remain interpretable.
	delta := time.Since(start).Microseconds()
	if delta < 0 {
		return
	}
	m.mu.Lock()
	m.injectLatencies[m.injectPosition] = delta
	m.injectPosition = (m.injectPosition + 1) % len(m.injectLatencies)
	m.mu.Unlock()
}

func (m *Metrics) RecordDropped() { m.dropped.Add(1) }

func (m *Metrics) SetQueueDepth(depth int) {
	if depth < 0 {
		depth = 0
	}
	m.queue.Store(int64(depth))
}

func (m *Metrics) Snapshot() MetricsSnapshot {
	snapshot := MetricsSnapshot{
		Received:   m.received.Load(),
		Injected:   m.injected.Load(),
		Dropped:    m.dropped.Load(),
		PacketLost: m.packetLost.Load(),
		QueueDepth: m.queue.Load(),
	}
	m.mu.Lock()
	receiveSamples := make([]int64, 0, len(m.receiveLatencies))
	for _, sample := range m.receiveLatencies {
		if sample > 0 {
			receiveSamples = append(receiveSamples, sample)
		}
	}
	injectSamples := make([]int64, 0, len(m.injectLatencies))
	for _, sample := range m.injectLatencies {
		if sample > 0 {
			injectSamples = append(injectSamples, sample)
		}
	}
	jitterSamples := make([]int64, 0, len(m.jitterSamples))
	for _, sample := range m.jitterSamples {
		if sample > 0 {
			jitterSamples = append(jitterSamples, sample)
		}
	}
	clockOffsets := make([]int64, 0, len(m.clockOffsets))
	for _, sample := range m.clockOffsets {
		if sample > 0 {
			clockOffsets = append(clockOffsets, sample)
		}
	}
	m.mu.Unlock()
	if len(receiveSamples) > 0 {
		sort.Slice(receiveSamples, func(i, j int) bool { return receiveSamples[i] < receiveSamples[j] })
		snapshot.ReceiveSamples = uint64(len(receiveSamples))
		snapshot.ReceiveP50Micros = percentile(receiveSamples, 0.50)
		snapshot.ReceiveP95Micros = percentile(receiveSamples, 0.95)
		snapshot.ReceiveP99Micros = percentile(receiveSamples, 0.99)
	}
	if len(injectSamples) > 0 {
		sort.Slice(injectSamples, func(i, j int) bool { return injectSamples[i] < injectSamples[j] })
		snapshot.InjectSamples = uint64(len(injectSamples))
		snapshot.InjectP50Micros = percentile(injectSamples, 0.50)
		snapshot.InjectP95Micros = percentile(injectSamples, 0.95)
		snapshot.InjectP99Micros = percentile(injectSamples, 0.99)
	}
	if len(jitterSamples) > 0 {
		sort.Slice(jitterSamples, func(i, j int) bool { return jitterSamples[i] < jitterSamples[j] })
		snapshot.ReceiveJitterP95Micros = percentile(jitterSamples, 0.95)
	}
	if len(clockOffsets) > 0 {
		sort.Slice(clockOffsets, func(i, j int) bool { return clockOffsets[i] < clockOffsets[j] })
		snapshot.ClockOffsetSamples = uint64(len(clockOffsets))
		snapshot.ClockOffsetP50Micros = percentile(clockOffsets, 0.50)
		snapshot.ClockOffsetP95Micros = percentile(clockOffsets, 0.95)
		snapshot.ClockOffsetP99Micros = percentile(clockOffsets, 0.99)
	}
	return snapshot
}

func percentile(samples []int64, fraction float64) int64 {
	index := int(float64(len(samples)-1) * fraction)
	return samples[index]
}
