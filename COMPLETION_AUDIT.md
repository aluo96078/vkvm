# TODO completion audit

This audit records evidence for the remaining hardware/platform-gated items.
The portable implementation is complete, but the four real-environment gates
remain intentionally unchecked until their required external evidence exists.

| Item | Portable evidence | Real gate | Current result |
| --- | --- | --- | --- |
| U19b | `internal/hid.USBHIDBackend`, TinyUSB C adapter, report ABI, watchdog, `hardware/accept-hid.sh portable` | `hardware/accept-hid.sh real` with an enumerated VKVM HID device | blocked: no matching VKVM HID device is enumerated |
| U20b | bounded p50/p95/p99, jitter, clock/path offset, packet loss, thresholds, `performance-baseline.txt` | Windows/macOS transport and stuck-key measurements | blocked: no paired gadget/second-host measurement matrix is present |
| S28b | SCM/LaunchAgent metadata, `--companion`, ACL, Supervisor tests, `packaging/platform-acceptance.sh portable` | SCM/LaunchAgent/TCC/multi-user lifecycle | blocked: no installed LaunchAgent/Windows SCM session was available |
| S29b | native event sources, coordinator/packet trace recorders, exact-once/waiting tests, `packaging/network-trace.sh portable` | real platform event and packet trace files | blocked: `VKVM_TRACE_DIR` traces were not produced by a running paired service |

The real gates deliberately fail rather than silently converting a portable
smoke test into a hardware claim. Re-run them after supplying the external
device/session:

```sh
hardware/accept-hid.sh real
packaging/platform-acceptance.sh real
VKVM_COORDINATOR_TRACE=/path/coordinator.json \
VKVM_PACKET_TRACE=/path/packet.json packaging/network-trace.sh real
```
