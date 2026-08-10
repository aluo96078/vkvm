package input

import "testing"

func TestCalibrationPreservesFractionalMotionAndScrollDirection(t *testing.T) {
	s := NewCalibrationState(Calibration{MouseSensitivity: 0.5, WheelPixelsPerUnit: 3, NaturalScrolling: true})
	first := s.Apply(InputEvent{Type: "mouse_move", DeltaX: 1, DeltaY: 1})
	second := s.Apply(InputEvent{Type: "mouse_move", DeltaX: 1, DeltaY: 1})
	if first.DeltaX != 0 || second.DeltaX != 1 || second.DeltaY != 1 {
		t.Fatalf("fractional motion lost: first=%+v second=%+v", first, second)
	}
	wheel := s.Apply(InputEvent{Type: "mouse_wheel", WheelDelta: 2})
	if wheel.WheelDelta != -6 {
		t.Fatalf("natural scroll = %d, want -6", wheel.WheelDelta)
	}
}

func TestCalibrationEnhancedAccelerationIsOptIn(t *testing.T) {
	none := NewCalibrationState(Calibration{MouseSensitivity: 1, WheelPixelsPerUnit: 3, MouseAcceleration: "none"})
	enhanced := NewCalibrationState(Calibration{MouseSensitivity: 1, WheelPixelsPerUnit: 3, MouseAcceleration: "enhanced"})
	a := none.Apply(InputEvent{Type: "mouse_move", DeltaX: 32})
	b := enhanced.Apply(InputEvent{Type: "mouse_move", DeltaX: 32})
	if a.DeltaX >= b.DeltaX {
		t.Fatalf("enhanced acceleration did not increase delta: none=%d enhanced=%d", a.DeltaX, b.DeltaX)
	}
}
