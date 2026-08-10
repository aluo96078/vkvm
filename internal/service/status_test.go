package service

import (
	"errors"
	"testing"
)

func TestStatusStoreTracksTransitionsWithoutSecrets(t *testing.T) {
	var nilStore *StatusStore
	if got := nilStore.Snapshot(); got.State != "" {
		t.Fatalf("nil status snapshot=%+v", got)
	}
	store := NewStatusStore("test")
	store.SetState(StateRunning, nil, 2)
	running := store.Snapshot()
	if running.State != StateRunning || running.RestartCount != 2 || running.StartedAt.IsZero() {
		t.Fatalf("running status=%+v", running)
	}
	store.SetState(StateFailed, errors.New("temporary failure"), 3)
	failed := store.Snapshot()
	if failed.State != StateFailed || failed.LastError != "temporary failure" || failed.RestartCount != 3 {
		t.Fatalf("failed status=%+v", failed)
	}
	store.SetState(StateStopped, nil, 3)
	if got := store.Snapshot(); got.LastError != "" {
		t.Fatalf("stale error remained: %+v", got)
	}
}
