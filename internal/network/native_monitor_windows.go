//go:build windows

package network

import (
	"context"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var iphlpapi = windows.NewLazySystemDLL("iphlpapi.dll")
var notifyIPInterfaceChange = iphlpapi.NewProc("NotifyIpInterfaceChange")
var cancelMIBChangeNotify2 = iphlpapi.NewProc("CancelMibChangeNotify2")

func newNativePlatformNetworkSource(interval time.Duration) SnapshotSource {
	return newNativeSnapshotSource(watchWindowsNetworkEvents, interval)
}

// watchWindowsNetworkEvents subscribes to IP Helper's interface-change
// callback. Unlike the legacy NotifyAddrChange blocking call, the MIB handle
// can be cancelled when the service context stops, so a shutdown cannot leave
// a native watcher goroutine behind.
func watchWindowsNetworkEvents(ctx context.Context, events chan<- struct{}) error {
	if ctx == nil {
		ctx = context.Background()
	}
	callback := windows.NewCallback(func(_ uintptr, _ uintptr, _ uint32) uintptr {
		select {
		case events <- struct{}{}:
		default:
		}
		return 0
	})
	var handle windows.Handle
	ret, _, callErr := notifyIPInterfaceChange.Call(
		0, // AF_UNSPEC: IPv4 and IPv6 interface changes
		callback,
		0,
		0, // do not emit a duplicate initial notification
		uintptr(unsafe.Pointer(&handle)),
	)
	if ret != 0 {
		if callErr == nil {
			callErr = windows.Errno(ret)
		}
		return callErr
	}
	defer func() {
		if handle != 0 {
			_, _, _ = cancelMIBChangeNotify2.Call(uintptr(handle))
		}
	}()
	<-ctx.Done()
	return ctx.Err()
}
