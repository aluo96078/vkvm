# USB HID / BLE feasibility decision

This note records the implementation boundary for the input-forwarding
request. The current product path is Windows Raw Input → authenticated UDP or
WebSocket → macOS CoreGraphics. It is a low-latency software input path; it is
not USB/IP and it does not enumerate as a keyboard or mouse.

## macOS virtual HID

HIDDriverKit is the supported route for a virtual HID device, but it requires
a signed DriverKit system extension, entitlements, user approval in Privacy &
Security, and a distribution/update story. A user-space CoreGraphics event
tap is not equivalent: it is scoped to the logged-in session and does not
provide a HID descriptor or reliable access to Secure Input, the login window,
or applications that consume raw HID reports. We therefore do not advertise
CoreGraphics as a real-device substitute.

## External USB gadget

An ESP32-S3 or RP2040 running USB device firmware can expose standard boot
keyboard/mouse HID reports. The network side must then deliver ordered reports
to the board, with a watchdog that releases all keys/buttons when the link
times out. This is the most predictable way to obtain real HID enumeration,
but it needs hardware, firmware, a provisioning flow, and a user-visible
fallback when the board is absent. No such hardware is available in this
workspace, so it remains an optional backend rather than a claimed feature.

## Windows BLE Peripheral

The public Windows Bluetooth APIs support central/client scenarios but do not
provide a supported generic BLE HID peripheral implementation for this use
case. A custom dongle/driver or an external ESP32/Pi device is required for a
standards-compliant keyboard/mouse peripheral. An ESP32 BLE HID prototype is
the recommended next experiment; it cannot be validated or shipped from this
repository alone.

## Decision

1. Keep Route A (Raw Input, authenticated transport, ordered state and
   release-all) as the supported software mode.
2. Measure latency/loss on real Windows/macOS hardware before promising a
   tuning target.
3. Treat Route B (USB gadget) and BLE HID as hardware-gated optional work.
   Do not mark login-screen, Secure Input, game raw-input, or HID enumeration
   requirements complete until a physical prototype passes those tests.

The portable report/session contract is implemented by
`internal/hid.USBHIDBackend` and `hardware/esp32s3/vkvm_hid_device.c`. Run
`hardware/accept-hid.sh portable` for deterministic ABI/watchdog checks; use
`hardware/accept-hid.sh real` only with an enumerated target device. The real
mode intentionally fails when no matching device is present.
