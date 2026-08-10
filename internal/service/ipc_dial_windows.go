//go:build windows

package service

import (
	"context"
	"errors"
	"net"

	"golang.org/x/sys/windows"
)

func dialCompanionIPC(ctx context.Context) (net.Conn, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	descriptor := CompanionIPCDescriptor()
	if descriptor.Network != "namedpipe" || descriptor.Address == "" {
		return nil, errors.New("unsupported named-pipe descriptor")
	}
	name, err := windows.UTF16PtrFromString(descriptor.Address)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return nil, err
	}
	return &namedPipeConn{h: h}, nil
}
