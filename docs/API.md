# VKVM HTTP API

All endpoints except `/health` require `Authorization: Bearer <api_token>`.
The server binds IPv4 on the configured API port when `api_enabled` is true;
an empty token prevents startup.

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/health` | unauthenticated liveness check |
| GET | `/api/status` | current profile and profile names |
| GET | `/api/config` | redacted configuration (token omitted) |
| POST | `/api/config` | replace configuration; redacted token is preserved |
| POST | `/api/switch?profile=<name>` | transactionally switch a profile |
| GET | `/api/detect` | inspect monitor inputs and detected profile |
| GET | `/api/metrics` | bounded input diagnostics, when enabled |
| GET | `/api/permissions` | Accessibility/Input Monitoring status |
| GET | `/api/service` | lifecycle, restart, and authenticated IPC diagnostics |
| GET/POST | `/api/pairing` | inspect or update explicit peer allowlist |
| GET | `/api/discover` | bounded mDNS/DNS-SD discovery (short legacy fallback only when mDNS is unavailable) |
| GET/POST | `/api/autostart` | inspect or apply per-user startup registration |
| GET/WS | `/ws` | authenticated agent control channel |

When the core runs as a split platform service it also exposes a local
owner-scoped Unix socket (or Windows named pipe). The companion IPC protocol
uses HMAC-authenticated newline-delimited JSON envelopes with protocol version
`1`; supported read-only requests are `ping`, `version`, `status`, and
`subscribe` (the latter returns a current status snapshot for bounded polling).
The secret is provisioned for the process lifetime and removed during orderly
shutdown. It is never included in `/api/service` diagnostics.

Set `VKVM_TRACE_DIR` only during an acceptance run to persist bounded
`coordinator-trace.json` and `packet-trace.json` files on orderly shutdown;
normal service operation performs no trace-file I/O.

`/ws` requires the same bearer token in the HTTP upgrade request. Input frames
are delivered only when USB forwarding is enabled and the Agent's explicit
profile gate is satisfied. UDP input frames carry a truncated HMAC-SHA256 tag,
are restricted to the registered Host endpoint, and use a replay window.

State-changing UI endpoints use POST and same-origin CSRF validation. Remote
sync targets must be numeric private, link-local, or shared addresses with an
explicit port.

Configuration snapshots redact both `api_token` and the device identity
private key. Pairing may store an Ed25519 public-key credential; the private key
never leaves the local owner-readable configuration store.
