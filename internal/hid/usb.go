package hid

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrUSBUnavailable = errors.New("USB HID backend unavailable")

// USBHIDWriter is the only hardware-specific seam required by the optional
// USB gadget backend. A real ESP32-S3/RP2040 adapter writes these reports to
// its keyboard and mouse HID endpoints; tests can use MemoryUSBWriter.
type USBHIDWriter interface {
	WriteKeyboard(KeyboardReport) error
	WriteMouse(MouseReport) error
	Close() error
}

// USBHIDTransport adapts a report writer to the authenticated HID session
// lifecycle. It emits only validated boot reports and exposes a health state
// so the LAN/BLE router can fail over when the gadget disappears.
type USBHIDTransport struct {
	mu     sync.RWMutex
	writer USBHIDWriter
	state  BLEState
}

func NewUSBHIDTransport(writer USBHIDWriter) *USBHIDTransport {
	return &USBHIDTransport{writer: writer, state: BLEIdle}
}

func (t *USBHIDTransport) Start(_ context.Context, reportMap []byte) error {
	if t == nil || t.writer == nil || len(reportMap) == 0 {
		return ErrUSBUnavailable
	}
	t.mu.Lock()
	t.state = BLEConnected
	t.mu.Unlock()
	return nil
}

func (t *USBHIDTransport) Stop() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	writer := t.writer
	t.state = BLEIdle
	t.mu.Unlock()
	if writer == nil {
		return nil
	}
	return writer.Close()
}

func (t *USBHIDTransport) State() BLEState {
	if t == nil {
		return BLEIdle
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.state
}

func (t *USBHIDTransport) Send(report BLEReport) error {
	if t == nil {
		return ErrUSBUnavailable
	}
	t.mu.RLock()
	writer, state := t.writer, t.state
	t.mu.RUnlock()
	if writer == nil || state != BLEConnected {
		return ErrUSBUnavailable
	}
	switch report.Kind {
	case "keyboard", "release_all":
		if err := writer.WriteKeyboard(report.Keyboard); err != nil {
			return err
		}
		if report.Kind == "release_all" {
			return writer.WriteMouse(report.Mouse)
		}
		return nil
	case "mouse":
		return writer.WriteMouse(report.Mouse)
	default:
		return errors.New("unknown USB HID report kind")
	}
}

// USBHIDBackend is a complete report/session backend. It deliberately does
// not pretend to enumerate a device itself: the writer is supplied by a
// signed DriverKit, WinUSB, or external USB-gadget adapter.
type USBHIDBackend struct {
	mu      sync.RWMutex
	profile HIDProfile
	writer  *USBHIDTransport
	session *BLESession
	sink    *BLEInputSink
	timeout time.Duration
	cancel  context.CancelFunc
}

func NewUSBHIDBackend(writer USBHIDWriter, profile HIDProfile, timeout time.Duration) *USBHIDBackend {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	return &USBHIDBackend{profile: profile, writer: NewUSBHIDTransport(writer), timeout: timeout}
}

func (b *USBHIDBackend) Start(ctx context.Context) error {
	if b == nil || b.writer == nil {
		return ErrUSBUnavailable
	}
	if err := ValidateHIDProfile(b.profile); err != nil {
		return err
	}
	session := NewBLESession(b.writer, b.profile, b.timeout)
	if err := session.Start(ctx); err != nil {
		return err
	}
	sessionCtx, cancel := context.WithCancel(ctx)
	b.mu.Lock()
	if b.cancel != nil {
		b.cancel()
	}
	b.session, b.sink, b.cancel = session, NewBLEInputSink(session), cancel
	b.mu.Unlock()
	go session.Watchdog(sessionCtx)
	return nil
}

func (b *USBHIDBackend) Send(event InputEvent) error {
	if b == nil {
		return ErrUSBUnavailable
	}
	b.mu.RLock()
	sink := b.sink
	b.mu.RUnlock()
	if sink == nil {
		return ErrUSBUnavailable
	}
	return sink.Send(event)
}

func (b *USBHIDBackend) ReleaseAll() error {
	if b == nil {
		return ErrUSBUnavailable
	}
	b.mu.RLock()
	sink := b.sink
	b.mu.RUnlock()
	if sink == nil {
		return ErrUSBUnavailable
	}
	return sink.ReleaseAll()
}

func (b *USBHIDBackend) Healthy() bool {
	if b == nil || b.writer == nil {
		return false
	}
	return b.writer.State() == BLEConnected
}

func (b *USBHIDBackend) Close() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	session := b.session
	cancel := b.cancel
	b.session, b.sink, b.cancel = nil, nil, nil
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if session != nil {
		_ = session.Close()
	}
	return b.writer.Stop()
}

// MemoryUSBWriter is a deterministic report sink for the U19 contract test.
// It validates the same endpoint sizes a USB HID writer must consume.
type MemoryUSBWriter struct {
	mu        sync.Mutex
	keyboards []KeyboardReport
	mice      []MouseReport
	closed    bool
}

func (w *MemoryUSBWriter) WriteKeyboard(report KeyboardReport) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return ErrUSBUnavailable
	}
	w.keyboards = append(w.keyboards, report)
	return nil
}

func (w *MemoryUSBWriter) WriteMouse(report MouseReport) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return ErrUSBUnavailable
	}
	w.mice = append(w.mice, report)
	return nil
}

func (w *MemoryUSBWriter) Close() error {
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()
	return nil
}

func (w *MemoryUSBWriter) Reports() (keyboards []KeyboardReport, mice []MouseReport) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]KeyboardReport(nil), w.keyboards...), append([]MouseReport(nil), w.mice...)
}
