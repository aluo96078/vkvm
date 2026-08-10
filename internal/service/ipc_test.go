package service

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type pipeListener struct {
	connections chan net.Conn
	closed      chan struct{}
}

func newPipeListener() *pipeListener {
	return &pipeListener{connections: make(chan net.Conn, 1), closed: make(chan struct{})}
}
func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.connections:
		return conn, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}
func (l *pipeListener) Close() error {
	select {
	case <-l.closed:
	default:
		close(l.closed)
	}
	return nil
}
func (l *pipeListener) Addr() net.Addr { return testPipeAddr("vkvm-test") }

type testPipeAddr string

func (a testPipeAddr) Network() string { return "pipe" }
func (a testPipeAddr) String() string  { return string(a) }

func TestIPCEnvelopeMACAndVersion(t *testing.T) {
	secret := []byte("session-secret")
	signed, err := SignIPC(secret, IPCEnvelope{Version: IPCProtocolVersion, Type: "status"})
	if err != nil || !VerifyIPC(secret, signed) {
		t.Fatalf("signed IPC message rejected: %v", err)
	}
	signed.Type = "mutated"
	if VerifyIPC(secret, signed) {
		t.Fatal("mutated IPC message accepted")
	}
}

func TestServeIPCAuthenticatesAndResponds(t *testing.T) {
	listener := newPipeListener()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- ServeIPC(ctx, listener, []byte("secret"), func(_ context.Context, request IPCEnvelope) (IPCEnvelope, error) {
			return IPCEnvelope{Type: "reply", RequestID: request.RequestID, Payload: json.RawMessage(`{"ok":true}`)}, nil
		})
	}()
	serverConn, conn := net.Pipe()
	listener.connections <- serverConn
	defer conn.Close()
	request, err := SignIPC([]byte("secret"), IPCEnvelope{Version: IPCProtocolVersion, Type: "status", RequestID: "1"})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(request)
	_, _ = conn.Write(append(data, '\n'))
	response, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var envelope IPCEnvelope
	if json.Unmarshal(response, &envelope) != nil || envelope.Type != "reply" || !VerifyIPC([]byte("secret"), envelope) {
		t.Fatalf("invalid IPC response: %s", response)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("IPC server did not stop")
	}
}

func TestIPCClientCallAuthenticatesRequest(t *testing.T) {
	listener := newPipeListener()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- ServeIPC(ctx, listener, []byte("client-secret"), func(_ context.Context, request IPCEnvelope) (IPCEnvelope, error) {
			return IPCEnvelope{Type: "reply", RequestID: request.RequestID}, nil
		})
	}()
	serverConn, clientConn := net.Pipe()
	listener.connections <- serverConn
	client := &IPCClient{conn: clientConn, secret: []byte("client-secret")}
	response, err := client.Call(context.Background(), IPCEnvelope{Type: "ping", RequestID: "client-1"})
	if err != nil || response.Type != "reply" || response.RequestID != "client-1" {
		t.Fatalf("IPC client response=%+v err=%v", response, err)
	}
	_ = client.Close()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("IPC client server did not stop")
	}
}

func TestServeIPCUnixLoopbackAndSecretLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix socket loopback is not a Windows transport")
	}
	t.Setenv("VKVM_IPC_SECRET_PATH", filepath.Join(t.TempDir(), "companion.secret"))
	secret, err := NewIPCSecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := ProvisionIPCSecret(secret); err != nil {
		t.Fatal(err)
	}
	defer RemoveIPCSecret()
	gotSecret, err := ReadIPCSecret()
	if err != nil || string(gotSecret) != string(secret) {
		t.Fatalf("secret round trip failed: %v", err)
	}
	info, err := os.Stat(IPCSecretPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("IPC secret mode = %o, want 600", info.Mode().Perm())
	}

	// Darwin's sockaddr_un has a short path limit; keep the temporary socket
	// directly under the system temp directory instead of t.TempDir().
	socketPath := filepath.Join(os.TempDir(), fmt.Sprintf("vkvm-ipc-%d.sock", time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.Remove(socketPath) })
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		if os.IsPermission(err) || strings.Contains(err.Error(), "operation not permitted") {
			t.Skipf("Unix loopback unavailable in sandbox: %v", err)
		}
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- ServeIPC(ctx, listener, secret, DiagnosticIPCHandler("test", func() DiagnosticStatus {
			return DiagnosticStatus{Version: "test", State: StateRunning}
		}))
	}()
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	request, err := SignIPC(secret, IPCEnvelope{Version: IPCProtocolVersion, Type: "status", RequestID: "loopback"})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(request)
	if _, err := conn.Write(append(data, '\n')); err != nil {
		t.Fatal(err)
	}
	responseData, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var response IPCEnvelope
	if err := json.Unmarshal(responseData, &response); err != nil || response.Type != "status" || !VerifyIPC(secret, response) {
		t.Fatalf("invalid loopback response: %s", responseData)
	}
	_ = conn.Close()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("loopback IPC server did not stop")
	}
}

func TestIPCClientRejectsMalformedSecret(t *testing.T) {
	t.Setenv("VKVM_IPC_SECRET_PATH", filepath.Join(t.TempDir(), "companion.secret"))
	if err := ProvisionIPCSecret([]byte("short")); err == nil {
		t.Fatal("short IPC secret accepted")
	}
	if err := os.WriteFile(IPCSecretPath(), []byte("not-a-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadIPCSecret(); err == nil {
		t.Fatal("malformed IPC secret accepted")
	}
}
