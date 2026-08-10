# ESP32-S3 HID prototype

This directory is a hardware-gated prototype contract, not a claim of tested
firmware. The ESP32-S3 is the preferred bridge because it can expose native
USB HID and BLE HID reports. The host sends the same authenticated VKVM input
payloads used by the software Agent; firmware must validate the session MAC,
sequence/replay window, and release-all before emitting reports.

`vkvm_hid_protocol.h` defines boot keyboard (8-byte) and mouse (4-byte) report
layouts. The host-side `internal/hid.USBHIDBackend` accepts a hardware writer
that emits those exact endpoint reports and applies the same watchdog and
release-all rules as the LAN/BLE path. A board validation pass must cover USB
enumeration, BLE pairing, disconnect/reconnect, rollover, sleep/wake, and
stuck-key recovery on the target macOS version before enabling this backend in
a release build.

The writer is deliberately an adapter boundary: this repository does not
claim that a serial/USB file is an enumerated HID device. A production
ESP-IDF/TinyUSB implementation must authenticate the network frame before
calling `vkvm_hid_send_keyboard`/`vkvm_hid_send_mouse`, reject replayed
sequences, and emit zero reports on watchdog timeout.

`vkvm_hid_device.c` is the TinyUSB endpoint adapter behind
`VKVM_USE_TINYUSB`; it is intentionally excluded from the Go build and must
be compiled inside an ESP-IDF project with a descriptor that declares report
IDs 1 (keyboard) and 2 (mouse).
