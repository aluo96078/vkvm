package hid

import "testing"

func TestEncodeKeyboardPreservesModifiersAndRejectsRollover(t *testing.T) {
	report, err := EncodeKeyboard(0x05, []byte{4, 5, 4})
	if err != nil || report[0] != 0x05 || report[2] != 4 || report[3] != 5 || report[4] != 0 {
		t.Fatalf("unexpected keyboard report %#v, err=%v", report, err)
	}
	keys := []byte{1, 2, 3, 4, 5, 6, 7}
	if _, err := EncodeKeyboard(0, keys); err != ErrKeyboardRollover {
		t.Fatalf("rollover error = %v", err)
	}
}

func TestSplitRelativeDeltaHasNoOverflowOrLoss(t *testing.T) {
	reports := SplitRelativeDelta(300, -260)
	var sumX, sumY int
	for _, report := range reports {
		sumX += int(int8(report[1]))
		sumY += int(int8(report[2]))
	}
	if sumX != 300 || sumY != -260 {
		t.Fatalf("delta changed: (%d,%d)", sumX, sumY)
	}
}

func TestBootDescriptorContainsKeyboardAndMouseCollections(t *testing.T) {
	descriptor := BootKeyboardMouseDescriptor
	keyboard := false
	mouse := false
	for i := 0; i+3 < len(descriptor); i++ {
		if descriptor[i] == 0x09 && descriptor[i+1] == 0x06 && descriptor[i+2] == 0xA1 && descriptor[i+3] == 0x01 {
			keyboard = true
		}
		if descriptor[i] == 0x09 && descriptor[i+1] == 0x02 && descriptor[i+2] == 0xA1 && descriptor[i+3] == 0x01 {
			mouse = true
		}
	}
	if !keyboard || !mouse {
		t.Fatalf("descriptor missing keyboard=%v mouse=%v", keyboard, mouse)
	}
}
