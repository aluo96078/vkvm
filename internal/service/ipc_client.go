package service

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"
)

// IPCClient is the minimal login-companion client. It authenticates every
// request and response, and never exposes the secret through diagnostics.
type IPCClient struct {
	conn   net.Conn
	secret []byte
}

// DialCompanionIPC reads the owner-scoped hand-off credential and connects to
// the core endpoint. A missing/expired secret is reported as a normal startup
// condition so a companion can wait for the next core restart.
func DialCompanionIPC(ctx context.Context) (*IPCClient, error) {
	secret, err := ReadIPCSecret()
	if err != nil {
		return nil, fmt.Errorf("read companion IPC secret: %w", err)
	}
	conn, err := dialCompanionIPC(ctx)
	if err != nil {
		return nil, err
	}
	return &IPCClient{conn: conn, secret: secret}, nil
}

// Call performs one bounded request/response exchange. The server may close
// the stream after any authentication failure; callers should reconnect on a
// later lifecycle/network event.
func (c *IPCClient) Call(ctx context.Context, request IPCEnvelope) (IPCEnvelope, error) {
	if c == nil || c.conn == nil || len(c.secret) == 0 {
		return IPCEnvelope{}, errors.New("IPC client is not connected")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if request.Version == 0 {
		request.Version = IPCProtocolVersion
	}
	signed, err := SignIPC(c.secret, request)
	if err != nil {
		return IPCEnvelope{}, err
	}
	data, err := json.Marshal(signed)
	if err != nil {
		return IPCEnvelope{}, err
	}
	deadline := time.Now().Add(5 * time.Second)
	if requested, ok := ctx.Deadline(); ok && requested.Before(deadline) {
		deadline = requested
	}
	if err := c.conn.SetDeadline(deadline); err != nil {
		return IPCEnvelope{}, err
	}
	if _, err := c.conn.Write(append(data, '\n')); err != nil {
		return IPCEnvelope{}, err
	}
	line, err := bufio.NewReaderSize(c.conn, maxIPCFrame).ReadBytes('\n')
	if err != nil {
		return IPCEnvelope{}, err
	}
	if len(line) > maxIPCFrame {
		return IPCEnvelope{}, errors.New("IPC response is too large")
	}
	var response IPCEnvelope
	if err := json.Unmarshal(line, &response); err != nil || !VerifyIPC(c.secret, response) {
		return IPCEnvelope{}, errors.New("invalid authenticated IPC response")
	}
	return response, nil
}

func (c *IPCClient) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}
