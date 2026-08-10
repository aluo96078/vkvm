package security

import (
	"testing"
	"time"
)

func TestKeyRingGracefulRotation(t *testing.T) {
	ring := NewKeyRing("old", 50*time.Millisecond)
	if !ring.MatchBearer("Bearer old") {
		t.Fatal("initial token rejected")
	}
	ring.Set("new")
	if !ring.MatchBearer("Bearer new") || !ring.MatchBearer("Bearer old") {
		t.Fatal("rotation did not keep the old token during grace period")
	}
	time.Sleep(70 * time.Millisecond)
	if ring.MatchBearer("Bearer old") {
		t.Fatal("retired token remained valid after grace period")
	}
}
