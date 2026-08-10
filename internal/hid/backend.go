package hid

import (
	"context"
	"errors"
	"sync"
	"time"
)

// PairFunc and ConnectFunc are platform/hardware seams. A macOS CoreBluetooth
// helper, blueutil wrapper, or ESP32 network bridge can implement them without
// changing the report/session/router code.
type PairFunc func(context.Context, string) error
type ConnectFunc func(context.Context, string, HIDProfile) (BLETransport, error)

// BLEBackend owns one pairing/transport/session lifecycle and exposes the same
// InputSink contract used by BLERouter. It intentionally has no OS-specific
// Bluetooth calls, so tests can use MemoryBLETransport while production uses a
// signed native or external hardware adapter.
type BLEBackend struct {
	mu      sync.RWMutex
	address string
	profile HIDProfile
	timeout time.Duration
	pair    PairFunc
	connect ConnectFunc
	session *BLESession
	sink    *BLEInputSink
	ctx     context.Context
	cancel  context.CancelFunc
}

func NewBLEBackend(address string, profile HIDProfile, pair PairFunc, connect ConnectFunc, timeout time.Duration) *BLEBackend {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	return &BLEBackend{address: address, profile: profile, pair: pair, connect: connect, timeout: timeout}
}

// NewMemoryBLEBackend is a deterministic integration seam for CI and protocol
// tests. It exercises the exact same profile/session/sink lifecycle as a real
// BLE adapter without claiming radio or hardware support.
func NewMemoryBLEBackend(profile HIDProfile, timeout time.Duration) (*BLEBackend, *MemoryBLETransport) {
	transport := &MemoryBLETransport{}
	backend := NewBLEBackend("memory", profile, nil, func(context.Context, string, HIDProfile) (BLETransport, error) {
		return transport, nil
	}, timeout)
	return backend, transport
}

func (b *BLEBackend) Start(ctx context.Context) error {
	if b == nil {
		return ErrBLEUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ValidateHIDProfile(b.profile); err != nil {
		return err
	}
	if b.connect == nil {
		return ErrBLEUnavailable
	}
	if b.pair != nil {
		if err := b.pair(ctx, b.address); err != nil {
			return err
		}
	}
	transport, err := b.connect(ctx, b.address, b.profile)
	if err != nil || transport == nil {
		if err != nil {
			return err
		}
		return ErrBLEUnavailable
	}
	session := NewBLESession(transport, b.profile, b.timeout)
	if err := session.Start(ctx); err != nil {
		return err
	}
	sessionCtx, cancel := context.WithCancel(ctx)
	b.mu.Lock()
	if b.cancel != nil {
		b.cancel()
	}
	b.session, b.sink, b.ctx, b.cancel = session, NewBLEInputSink(session), sessionCtx, cancel
	b.mu.Unlock()
	go session.Watchdog(sessionCtx)
	return nil
}

func (b *BLEBackend) Send(event InputEvent) error {
	b.mu.RLock()
	sink := b.sink
	b.mu.RUnlock()
	if sink == nil {
		return ErrBLEUnavailable
	}
	return sink.Send(event)
}

func (b *BLEBackend) ReleaseAll() error {
	b.mu.RLock()
	sink := b.sink
	b.mu.RUnlock()
	if sink == nil {
		return ErrBLEUnavailable
	}
	return sink.ReleaseAll()
}

// Healthy reports whether a backend has a live transport. Advertising is
// considered available because the peer may connect asynchronously; a failed
// Send still causes BLERouter to fall back to LAN.
func (b *BLEBackend) Healthy() bool {
	b.mu.RLock()
	session := b.session
	b.mu.RUnlock()
	if session == nil || session.transport == nil {
		return false
	}
	state := session.transport.State()
	return state == BLEAdvertising || state == BLEPairing || state == BLEConnected
}

func (b *BLEBackend) Close() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	session, cancel := b.session, b.cancel
	b.session, b.sink, b.cancel = nil, nil, nil
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if session == nil {
		return nil
	}
	return session.Close()
}

func (b *BLEBackend) Profile() (HIDProfile, error) {
	if b == nil {
		return HIDProfile{}, errors.New("BLE backend is nil")
	}
	return b.profile, nil
}
