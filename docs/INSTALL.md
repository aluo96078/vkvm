# VKVM installation and configuration

## Prerequisites

- macOS: grant Accessibility and Input Monitoring to the fixed application
  path that runs VKVM. CoreGraphics injection is limited to the logged-in
  user session.
- Windows: run the input-capture build in the interactive user session and
  allow the API/UDP port through the private-network firewall when remote
  forwarding is enabled.

The current repository produces signed/unsigned binaries through the release
workflow; a `.dmg` and NSIS/MSI installer are not yet shipped.

## First run

1. Start VKVM and open `Settings...` from the tray.
2. Add one profile per display input and verify DDC/CI detection.
3. Leave `API enabled` and `USB forwarding enabled` off until both machines
   have been paired.
4. On the Host, enable the API and set a random high-entropy API token.
5. On the Agent, set the same token and the Host `Coordinator address`.
6. Set an explicit `Agent profile`; injection stays disabled when the profile
   is missing or the active monitor state cannot be verified.

## Startup and network behavior

`Start on boot` installs a per-user macOS LaunchAgent or Windows Run entry.
The entry passes `--companion`, so it does not start a second API/UDP listener;
the companion negotiates protocol version and polls authenticated core status
over local IPC.
Role, coordinator address, API port and API token changes take effect after a
service restart. An Agent waits for a network fingerprint change after a
failed coordinator connection; it does not continuously scan the LAN. UDP
heartbeat loss falls back to the authenticated WebSocket path.

For S29 evidence, set `VKVM_TRACE_DIR` to an owner-only directory during an
acceptance run and stop the service cleanly. The generated bounded coordinator
and packet traces can be checked with `packaging/network-trace.sh real`.

## Emergency stop

Use the configured escape hotkey to disable capture. On shutdown or transport
loss VKVM sends a release-all message and the macOS injector releases any
tracked keys and buttons.

On macOS the interactive component remains a per-user LaunchAgent. A
privileged LaunchDaemon is intentionally not installed: root cannot bypass
Accessibility/Input Monitoring TCC, and a system daemon cannot safely own the
tray, global hotkeys, or CoreGraphics event session. In split deployments the
core publishes an owner-only Unix socket plus a short-lived MAC secret; the
companion can call authenticated `ping`, `version`, `status`, and `subscribe`
(snapshot polling) requests without receiving arbitrary core control.

On Windows, `packaging/windows/install-service.ps1` is the SCM installation
path for the network core. It installs `vkvm.exe --service` with delayed
automatic start and bounded failure recovery; the per-user Run entry remains
responsible for the interactive companion. Hooks and tray code must never run
in the SCM session. The named pipe and secret hand-off use an explicit
Interactive Users/LocalSystem/Administrators ACL plus HMAC authentication;
SCM multi-user behavior still requires the S28 Windows hardware/session
acceptance matrix.
