package hid

import (
	"context"
	"errors"
	"sync"
	"time"
)

const (
	HIDServiceUUID           = "1812"
	HIDInformationUUID       = "2A4A"
	ReportMapUUID            = "2A4B"
	HIDControlPointUUID      = "2A4C"
	ReportUUID               = "2A4D"
	ProtocolModeUUID         = "2A4E"
	BatteryServiceUUID       = "180F"
	BatteryLevelUUID         = "2A19"
	BootProtocolMode    byte = 0
	ReportProtocolMode  byte = 1
)

var ErrBLEUnavailable = errors.New("BLE HID backend unavailable")

type BLEState string

const (
	BLEIdle        BLEState = "idle"
	BLEAdvertising BLEState = "advertising"
	BLEPairing     BLEState = "pairing"
	BLEConnected   BLEState = "connected"
	BLESuspended   BLEState = "suspended"
)

type BLEReport struct {
	Sequence uint32
	Keyboard KeyboardReport
	Mouse    MouseReport
	Kind     string // keyboard, mouse, release_all
}

type BLETransport interface {
	Start(context.Context, []byte) error
	Stop() error
	State() BLEState
	Send(BLEReport) error
}

// HIDProfile contains the GATT metadata needed by an external BLE HID
// peripheral (ESP32/Pi). The OS-specific advertiser owns the actual radio.
type HIDProfile struct {
	ServiceUUID  string
	ReportMap    []byte
	ReportUUID   string
	ProtocolMode byte
	BatteryLevel byte
	Appearance   uint16
	DeviceName   string
}

func DefaultBLEHIDProfile(deviceName string) HIDProfile {
	if deviceName == "" {
		deviceName = "VKVM HID"
	}
	return HIDProfile{ServiceUUID: HIDServiceUUID, ReportMap: append([]byte(nil), BootKeyboardMouseDescriptor...), ReportUUID: ReportUUID, ProtocolMode: BootProtocolMode, BatteryLevel: 100, Appearance: 0x03C1, DeviceName: deviceName}
}

type BLESession struct {
	transport BLETransport
	profile   HIDProfile
	timeout   time.Duration
	mu        sync.Mutex
	lastSend  time.Time
	sequence  uint32
	closed    bool
}

func NewBLESession(transport BLETransport, profile HIDProfile, timeout time.Duration) *BLESession {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	return &BLESession{transport: transport, profile: profile, timeout: timeout}
}

func (s *BLESession) Start(ctx context.Context) error {
	if s == nil || s.transport == nil {
		return ErrBLEUnavailable
	}
	if err := ValidateHIDProfile(s.profile); err != nil {
		return err
	}
	return s.transport.Start(ctx, s.profile.ReportMap)
}

func (s *BLESession) SendKeyboard(report KeyboardReport) error {
	return s.send(BLEReport{Kind: "keyboard", Keyboard: report})
}

func (s *BLESession) SendMouse(report MouseReport) error {
	return s.send(BLEReport{Kind: "mouse", Mouse: report})
}

func (s *BLESession) ReleaseAll() error {
	return s.send(BLEReport{Kind: "release_all", Keyboard: KeyboardReport{}, Mouse: MouseReport{}})
}

func (s *BLESession) send(report BLEReport) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("BLE session is closed")
	}
	s.sequence++
	report.Sequence = s.sequence
	s.lastSend = time.Now()
	s.mu.Unlock()
	return s.transport.Send(report)
}

func (s *BLESession) Watchdog(ctx context.Context) {
	if s == nil || s.transport == nil {
		return
	}
	ticker := time.NewTicker(s.timeout / 2)
	if s.timeout/2 <= 0 {
		ticker.Stop()
		return
	}
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = s.ReleaseAll()
			return
		case <-ticker.C:
			s.mu.Lock()
			stale := !s.lastSend.IsZero() && time.Since(s.lastSend) > s.timeout
			s.mu.Unlock()
			if stale {
				_ = s.ReleaseAll()
			}
		}
	}
}

func (s *BLESession) Close() error {
	if s == nil || s.transport == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	_ = s.ReleaseAll()
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return s.transport.Stop()
}

// ValidateHIDProfile checks the stable GATT/report contract before a platform
// adapter is asked to advertise it. Keeping this validation in the portable
// layer prevents a BLE implementation from advertising a descriptor that the
// report encoder cannot satisfy.
func ValidateHIDProfile(profile HIDProfile) error {
	if profile.ServiceUUID != HIDServiceUUID || profile.ReportUUID != ReportUUID {
		return errors.New("unsupported BLE HID service/report UUID")
	}
	if len(profile.ReportMap) == 0 {
		return errors.New("BLE HID report map is empty")
	}
	keyboard, mouse := false, false
	for i := 0; i+3 < len(profile.ReportMap); i++ {
		if profile.ReportMap[i] == 0x09 && profile.ReportMap[i+1] == 0x06 && profile.ReportMap[i+2] == 0xA1 && profile.ReportMap[i+3] == 0x01 {
			keyboard = true
		}
		if profile.ReportMap[i] == 0x09 && profile.ReportMap[i+1] == 0x02 && profile.ReportMap[i+2] == 0xA1 && profile.ReportMap[i+3] == 0x01 {
			mouse = true
		}
	}
	if !keyboard || !mouse {
		return errors.New("BLE HID report map must contain keyboard and mouse collections")
	}
	if profile.DeviceName == "" {
		return errors.New("BLE HID device name is empty")
	}
	return nil
}

type MemoryBLETransport struct {
	mu      sync.Mutex
	state   BLEState
	reports []BLEReport
}

func (m *MemoryBLETransport) Start(context.Context, []byte) error {
	m.mu.Lock()
	m.state = BLEAdvertising
	m.mu.Unlock()
	return nil
}
func (m *MemoryBLETransport) Stop() error     { m.mu.Lock(); m.state = BLEIdle; m.mu.Unlock(); return nil }
func (m *MemoryBLETransport) State() BLEState { m.mu.Lock(); defer m.mu.Unlock(); return m.state }
func (m *MemoryBLETransport) Send(report BLEReport) error {
	m.mu.Lock()
	m.reports = append(m.reports, report)
	m.state = BLEConnected
	m.mu.Unlock()
	return nil
}
func (m *MemoryBLETransport) Reports() []BLEReport {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]BLEReport(nil), m.reports...)
}
