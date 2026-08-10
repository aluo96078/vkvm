# VKVM 0.2.0

## Highlights

- Safe-by-default authenticated API, WebSocket and UDP forwarding with paired
  device identities, session MACs, replay protection and release-all recovery.
- Ordered Raw Input forwarding with per-agent transport negotiation, calibration,
  latency/loss metrics and network-change-aware discovery.
- macOS LaunchAgent and Windows companion/service packaging foundations,
  permission diagnostics, authenticated core/companion IPC and bounded service
  supervision.
- Optional ESP32-S3 USB/BLE HID report ABI and a transport-neutral BLE HID
  profile/session prototype.

## Verification

The release gate runs formatting, vet, unit/race tests, coverage reporting and
macOS arm64/Windows amd64 builds. Physical HID/BLE, DDC multi-machine and
notarization/SmartScreen acceptance remain release-environment tests.
