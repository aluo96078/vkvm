package network

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type PacketTraceEvent struct {
	At        time.Time `json:"at"`
	Direction string    `json:"direction"`
	Type      uint8     `json:"type"`
	Sequence  uint32    `json:"sequence"`
	Peer      string    `json:"peer,omitempty"`
	DeviceID  string    `json:"device_id,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
}

type PacketTraceRecorder struct {
	mu       sync.Mutex
	capacity int
	events   []PacketTraceEvent
}

func NewPacketTraceRecorder(capacity int) *PacketTraceRecorder {
	if capacity <= 0 {
		capacity = 64
	}
	return &PacketTraceRecorder{capacity: capacity, events: make([]PacketTraceEvent, 0, capacity)}
}

func (r *PacketTraceRecorder) Record(event PacketTraceEvent) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if len(r.events) == r.capacity {
		copy(r.events, r.events[1:])
		r.events = r.events[:r.capacity-1]
	}
	r.events = append(r.events, event)
	r.mu.Unlock()
}

func (r *PacketTraceRecorder) Snapshot() []PacketTraceEvent {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]PacketTraceEvent(nil), r.events...)
}

func (r *PacketTraceRecorder) Save(path string) error {
	if r == nil || path == "" {
		return fmt.Errorf("packet trace recorder and path are required")
	}
	data, err := json.MarshalIndent(r.Snapshot(), "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".packet-trace-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// TraceRecorder stores a bounded coordinator trace suitable for S29 review.
// It is opt-in and can be written atomically only when an operator requests
// evidence, so discovery never performs periodic log/file work by itself.
type TraceRecorder struct {
	mu       sync.Mutex
	capacity int
	events   []TraceEvent
}

func NewTraceRecorder(capacity int) *TraceRecorder {
	if capacity < 32 {
		capacity = 32
	}
	return &TraceRecorder{capacity: capacity, events: make([]TraceEvent, 0, capacity)}
}

func (r *TraceRecorder) Record(event TraceEvent) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if len(r.events) == r.capacity {
		copy(r.events, r.events[1:])
		r.events = r.events[:r.capacity-1]
	}
	r.events = append(r.events, event)
	r.mu.Unlock()
}

func (r *TraceRecorder) Snapshot() []TraceEvent {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]TraceEvent(nil), r.events...)
}

func (r *TraceRecorder) Save(path string) error {
	if r == nil || path == "" {
		return fmt.Errorf("trace recorder and path are required")
	}
	data, err := json.MarshalIndent(r.Snapshot(), "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".coordinator-trace-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
