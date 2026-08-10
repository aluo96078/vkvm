package input

import "math"

// Calibration describes the user/device-specific conversion from raw host
// deltas to the target platform's pointer and scroll units.
type Calibration struct {
	MouseSensitivity   float64
	WheelPixelsPerUnit float64
	NaturalScrolling   bool
	MouseAcceleration  string
}

// CalibrationState retains fractional pixels so low-sensitivity motion does
// not disappear when each event is converted to an integer CoreGraphics delta.
type CalibrationState struct {
	calibration Calibration
	fracX       float64
	fracY       float64
}

func NewCalibrationState(calibration Calibration) *CalibrationState {
	s := &CalibrationState{}
	s.Update(calibration)
	return s
}

func (s *CalibrationState) Update(calibration Calibration) {
	if calibration.MouseSensitivity <= 0 {
		calibration.MouseSensitivity = 1
	}
	if calibration.WheelPixelsPerUnit <= 0 {
		calibration.WheelPixelsPerUnit = 3
	}
	if calibration.MouseAcceleration == "" {
		calibration.MouseAcceleration = "none"
	}
	s.calibration = calibration
}

func (s *CalibrationState) Apply(event InputEvent) InputEvent {
	c := s.calibration
	if event.Type == "mouse_move" {
		x, y := float64(event.DeltaX), float64(event.DeltaY)
		switch c.MouseAcceleration {
		case "enhanced":
			magnitude := math.Hypot(x, y)
			if magnitude > 1 {
				factor := 1 + math.Min(magnitude/32, 1)
				x *= factor
				y *= factor
			}
		case "linear", "none":
		}
		s.fracX += x * c.MouseSensitivity
		s.fracY += y * c.MouseSensitivity
		event.DeltaX = takeInteger(&s.fracX)
		event.DeltaY = takeInteger(&s.fracY)
	}
	if event.Type == "mouse_wheel" || event.Type == "mouse_wheel_h" {
		wheel := math.Round(float64(event.WheelDelta) * c.WheelPixelsPerUnit)
		if c.NaturalScrolling {
			wheel = -wheel
		}
		event.WheelDelta = int(wheel)
	}
	return event
}

func takeInteger(value *float64) int {
	integer := math.Trunc(*value)
	*value -= integer
	return int(integer)
}
