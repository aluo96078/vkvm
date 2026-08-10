package service

import (
	"runtime"
	"sync"
	"time"
)

// DiagnosticStatus is a stable, JSON-friendly snapshot for the UI and service
// manager. It intentionally contains no secrets or raw error stack traces.
type DiagnosticStatus struct {
	Version        string        `json:"version"`
	Platform       string        `json:"platform"`
	State          State         `json:"state"`
	StartedAt      time.Time     `json:"started_at,omitempty"`
	LastTransition time.Time     `json:"last_transition,omitempty"`
	LastError      string        `json:"last_error,omitempty"`
	RestartCount   int           `json:"restart_count"`
	IPC            IPCDescriptor `json:"ipc"`
}

type StatusStore struct {
	mu     sync.RWMutex
	status DiagnosticStatus
}

func NewStatusStore(version string) *StatusStore {
	now := time.Now().UTC()
	return &StatusStore{status: DiagnosticStatus{Version: version, Platform: runtime.GOOS + "/" + runtime.GOARCH, State: StateStopped, LastTransition: now, IPC: CompanionIPCDescriptor()}}
}

func (s *StatusStore) SetState(state State, err error, restartCount int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	if s.status.StartedAt.IsZero() && state != StateStopped {
		s.status.StartedAt = now
	}
	s.status.State, s.status.LastTransition, s.status.RestartCount = state, now, restartCount
	s.status.LastError = ""
	if err != nil {
		s.status.LastError = err.Error()
	}
}

func (s *StatusStore) Snapshot() DiagnosticStatus {
	if s == nil {
		return DiagnosticStatus{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}
