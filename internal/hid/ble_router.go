package hid

import "sync"

type InputSink interface {
	Send(InputEvent) error
	ReleaseAll() error
}

// InputEvent is the transport-neutral event used by the BLE/LAN router.
type InputEvent struct {
	Type      string
	DeltaX    int
	DeltaY    int
	Button    int
	Pressed   bool
	KeyCode   uint16
	ScanCode  uint16
	Extended  bool
	Modifiers byte
	Wheel     int
}

type BLERouter struct {
	mu         sync.RWMutex
	ble        InputSink
	lan        InputSink
	preferBLE  bool
	bleHealthy func() bool
}

func NewBLERouter(ble, lan InputSink, preferBLE bool, bleHealthy func() bool) *BLERouter {
	if bleHealthy == nil {
		bleHealthy = func() bool { return ble != nil }
	}
	return &BLERouter{ble: ble, lan: lan, preferBLE: preferBLE, bleHealthy: bleHealthy}
}

func (r *BLERouter) Send(event InputEvent) error {
	r.mu.RLock()
	ble, lan, prefer, healthy := r.ble, r.lan, r.preferBLE, r.bleHealthy
	r.mu.RUnlock()
	if prefer && ble != nil && healthy() {
		if err := ble.Send(event); err == nil {
			return nil
		}
	}
	if lan == nil {
		return ErrBLEUnavailable
	}
	return lan.Send(event)
}

func (r *BLERouter) ReleaseAll() error {
	r.mu.RLock()
	ble, lan := r.ble, r.lan
	r.mu.RUnlock()
	var firstErr error
	for _, sink := range []InputSink{ble, lan} {
		if sink != nil {
			if err := sink.ReleaseAll(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}
