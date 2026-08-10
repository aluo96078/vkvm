// Package security contains small, transport-independent security helpers.
package security

import (
	"crypto/subtle"
	"strings"
	"sync"
	"time"
)

type retiredKey struct {
	value     []byte
	expiresAt time.Time
}

// KeyRing keeps the current credential and a short-lived previous credential.
// The grace window is intentionally bounded so rotation does not become a
// permanent second authentication key.
type KeyRing struct {
	mu      sync.RWMutex
	current []byte
	retired []retiredKey
	grace   time.Duration
}

func NewKeyRing(key string, grace time.Duration) *KeyRing {
	if grace <= 0 {
		grace = 30 * time.Second
	}
	return &KeyRing{current: []byte(key), grace: grace}
}

func (r *KeyRing) Set(key string) {
	next := []byte(key)
	r.mu.Lock()
	defer r.mu.Unlock()
	if string(r.current) == key {
		return
	}
	if len(r.current) > 0 {
		r.retired = append([]retiredKey{{value: append([]byte(nil), r.current...), expiresAt: time.Now().Add(r.grace)}}, r.retired...)
	}
	r.current = next
	r.pruneLocked(time.Now())
}

func (r *KeyRing) Keys() [][]byte {
	now := time.Now()
	r.mu.Lock()
	r.pruneLocked(now)
	keys := make([][]byte, 0, 1+len(r.retired))
	if len(r.current) > 0 {
		keys = append(keys, append([]byte(nil), r.current...))
	}
	for _, old := range r.retired {
		keys = append(keys, append([]byte(nil), old.value...))
	}
	r.mu.Unlock()
	return keys
}

func (r *KeyRing) MatchBearer(header string) bool {
	if !strings.HasPrefix(header, "Bearer ") {
		return false
	}
	presented := []byte(header[len("Bearer "):])
	if len(presented) == 0 {
		return false
	}
	for _, key := range r.Keys() {
		if len(key) == len(presented) && subtle.ConstantTimeCompare(key, presented) == 1 {
			return true
		}
	}
	return false
}

func (r *KeyRing) pruneLocked(now time.Time) {
	kept := r.retired[:0]
	for _, old := range r.retired {
		if now.Before(old.expiresAt) {
			kept = append(kept, old)
		}
	}
	r.retired = kept
}
