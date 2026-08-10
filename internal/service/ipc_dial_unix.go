//go:build !windows

package service

import (
	"context"
	"net"
)

func dialCompanionIPC(ctx context.Context) (net.Conn, error) {
	descriptor := CompanionIPCDescriptor()
	if descriptor.Network != "unix" || descriptor.Address == "" {
		return nil, net.UnknownNetworkError(descriptor.Network)
	}
	return (&net.Dialer{}).DialContext(ctx, "unix", descriptor.Address)
}
