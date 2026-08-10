package service

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
)

// NewIPCSecret creates a process-local secret for the companion handshake.
// Callers must provision it to the companion through an owner-scoped channel;
// it is never sent over the IPC stream itself.
func NewIPCSecret() ([]byte, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("generate IPC secret: %w", err)
	}
	return secret, nil
}

// StartCompanionIPC binds the platform owner-scoped endpoint and starts the
// authenticated core/companion protocol. The listener is returned so service
// shutdown can close it explicitly; context cancellation also closes it.
func StartCompanionIPC(ctx context.Context, secret []byte, handler IPCHandler) (net.Listener, error) {
	if len(secret) == 0 || handler == nil {
		return nil, fmt.Errorf("IPC secret and handler are required")
	}
	listener, err := listenCompanionIPC()
	if err != nil {
		return nil, err
	}
	go func() {
		_ = ServeIPC(ctx, listener, secret, handler)
	}()
	return listener, nil
}

// StartCoreCompanionIPC creates a fresh credential, publishes it to the
// owner-scoped hand-off file, and starts the core listener. The returned
// secret is kept by the caller only for the lifetime of the listener; callers
// should invoke RemoveIPCSecret when the service exits.
func StartCoreCompanionIPC(ctx context.Context, handler IPCHandler) (net.Listener, []byte, error) {
	secret, err := NewIPCSecret()
	if err != nil {
		return nil, nil, err
	}
	if err := ProvisionIPCSecret(secret); err != nil {
		return nil, nil, err
	}
	listener, err := StartCompanionIPC(ctx, secret, handler)
	if err != nil {
		_ = RemoveIPCSecret()
		return nil, nil, err
	}
	return listener, secret, nil
}

// DiagnosticIPCHandler exposes only read-only companion operations. The
// subscribe request returns the current snapshot and is intentionally safe to
// poll; a future event stream can be added without granting the companion
// arbitrary control over the core.
func DiagnosticIPCHandler(version string, status func() DiagnosticStatus) IPCHandler {
	return func(_ context.Context, request IPCEnvelope) (IPCEnvelope, error) {
		if request.Version != IPCProtocolVersion {
			return IPCEnvelope{}, fmt.Errorf("unsupported IPC protocol version %d", request.Version)
		}
		payload := func(value interface{}) json.RawMessage {
			data, _ := json.Marshal(value)
			return data
		}
		switch request.Type {
		case "ping":
			return IPCEnvelope{Type: "pong", RequestID: request.RequestID, Payload: payload(map[string]bool{"ok": true})}, nil
		case "version":
			return IPCEnvelope{Type: "version", RequestID: request.RequestID, Payload: payload(map[string]interface{}{"version": version, "protocol": IPCProtocolVersion})}, nil
		case "status", "subscribe":
			if status == nil {
				return IPCEnvelope{}, fmt.Errorf("service status unavailable")
			}
			return IPCEnvelope{Type: "status", RequestID: request.RequestID, Payload: payload(status())}, nil
		default:
			return IPCEnvelope{}, fmt.Errorf("unsupported IPC request %q", request.Type)
		}
	}
}
