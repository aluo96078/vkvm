//go:build windows

package service

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

type namedPipeListener struct {
	name   string
	mu     sync.Mutex
	closed bool
	active windows.Handle
}

type namedPipeConn struct {
	h      windows.Handle
	closed sync.Once
}

func listenCompanionIPC() (net.Listener, error) {
	descriptor := CompanionIPCDescriptor()
	if descriptor.Network != "namedpipe" || descriptor.Address == "" {
		return nil, fmt.Errorf("unsupported local IPC descriptor: %+v", descriptor)
	}
	return &namedPipeListener{name: descriptor.Address}, nil
}

func (l *namedPipeListener) Accept() (net.Conn, error) {
	name, err := windows.UTF16PtrFromString(l.name)
	if err != nil {
		return nil, err
	}
	securityAttributes, err := companionIPCSecurityAttributes()
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateNamedPipe(name, windows.PIPE_ACCESS_DUPLEX, windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT, windows.PIPE_UNLIMITED_INSTANCES, maxIPCFrame, maxIPCFrame, 0, securityAttributes)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		_ = windows.CloseHandle(h)
		return nil, net.ErrClosed
	}
	l.active = h
	l.mu.Unlock()
	err = windows.ConnectNamedPipe(h, nil)
	if err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
		_ = windows.CloseHandle(h)
		l.mu.Lock()
		l.active = 0
		closed := l.closed
		l.mu.Unlock()
		if closed {
			return nil, net.ErrClosed
		}
		return nil, err
	}
	l.mu.Lock()
	l.active = 0
	l.mu.Unlock()
	return &namedPipeConn{h: h}, nil
}

func (l *namedPipeListener) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	h := l.active
	l.active = 0
	l.mu.Unlock()
	if h != 0 {
		return windows.CloseHandle(h)
	}
	return nil
}

func (l *namedPipeListener) Addr() net.Addr { return pipeAddr(l.name) }

type pipeAddr string

func (a pipeAddr) Network() string { return "namedpipe" }
func (a pipeAddr) String() string  { return string(a) }

func (c *namedPipeConn) Read(p []byte) (int, error) {
	var read uint32
	err := windows.ReadFile(c.h, p, &read, nil)
	if err != nil {
		if errors.Is(err, windows.ERROR_BROKEN_PIPE) {
			return int(read), io.EOF
		}
		return int(read), err
	}
	return int(read), nil
}

func (c *namedPipeConn) Write(p []byte) (int, error) {
	var written uint32
	err := windows.WriteFile(c.h, p, &written, nil)
	return int(written), err
}

func (c *namedPipeConn) Close() error {
	var err error
	c.closed.Do(func() { err = windows.CloseHandle(c.h) })
	return err
}

func (c *namedPipeConn) LocalAddr() net.Addr                { return pipeAddr("local") }
func (c *namedPipeConn) RemoteAddr() net.Addr               { return pipeAddr("local-client") }
func (c *namedPipeConn) SetDeadline(_ time.Time) error      { return nil }
func (c *namedPipeConn) SetReadDeadline(_ time.Time) error  { return nil }
func (c *namedPipeConn) SetWriteDeadline(_ time.Time) error { return nil }
