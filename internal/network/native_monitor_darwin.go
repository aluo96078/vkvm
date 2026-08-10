//go:build darwin

package network

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Network -framework Foundation
#include <Network/Network.h>
#include <dispatch/dispatch.h>
#include <stdint.h>

// Implemented by Go below. Network.framework invokes the callback on its
// private queue; the Go side only performs a non-blocking channel notification.
extern void vkvmDarwinNetworkEvent(uintptr_t);

static nw_path_monitor_t vkvm_path_monitor;
static dispatch_queue_t vkvm_path_queue;

static int vkvm_start_path_monitor(void) {
	if (vkvm_path_monitor != NULL) {
		return 0;
	}
	vkvm_path_monitor = nw_path_monitor_create();
	if (vkvm_path_monitor == NULL) {
		return -1;
	}
	vkvm_path_queue = dispatch_queue_create("com.vkvm.network-monitor", DISPATCH_QUEUE_SERIAL);
	if (vkvm_path_queue == NULL) {
		nw_path_monitor_cancel(vkvm_path_monitor);
		vkvm_path_monitor = NULL;
		return -1;
	}
	nw_path_monitor_set_queue(vkvm_path_monitor, vkvm_path_queue);
	nw_path_monitor_set_update_handler(vkvm_path_monitor, ^(nw_path_t path) {
		(void)path;
		vkvmDarwinNetworkEvent(0);
	});
	nw_path_monitor_start(vkvm_path_monitor);
	return 0;
}

static void vkvm_stop_path_monitor(void) {
	if (vkvm_path_monitor != NULL) {
		nw_path_monitor_cancel(vkvm_path_monitor);
		vkvm_path_monitor = NULL;
	}
	// The queue is owned by the monitor for the duration of the callback. It is
	// intentionally not dispatch_released because modern macOS uses ARC for the
	// Network.framework object graph and dispatch_release is unavailable there.
	vkvm_path_queue = NULL;
}
*/
import "C"

import (
	"context"
	"errors"
	"sync"
	"time"
)

func newNativePlatformNetworkSource(interval time.Duration) SnapshotSource {
	return newNativeSnapshotSource(watchDarwinNetworkEvents, interval)
}

var darwinNetworkWatch struct {
	sync.Mutex
	events chan<- struct{}
}

//export vkvmDarwinNetworkEvent
func vkvmDarwinNetworkEvent(_ C.uintptr_t) {
	darwinNetworkWatch.Lock()
	events := darwinNetworkWatch.events
	darwinNetworkWatch.Unlock()
	if events == nil {
		return
	}
	select {
	case events <- struct{}{}:
	default:
	}
}

func watchDarwinNetworkEvents(ctx context.Context, events chan<- struct{}) error {
	if ctx == nil {
		ctx = context.Background()
	}
	darwinNetworkWatch.Lock()
	if darwinNetworkWatch.events != nil {
		darwinNetworkWatch.Unlock()
		return errors.New("Network.framework monitor already running")
	}
	darwinNetworkWatch.events = events
	darwinNetworkWatch.Unlock()
	if C.vkvm_start_path_monitor() != 0 {
		darwinNetworkWatch.Lock()
		darwinNetworkWatch.events = nil
		darwinNetworkWatch.Unlock()
		return errors.New("Network.framework path monitor unavailable")
	}
	defer func() {
		C.vkvm_stop_path_monitor()
		darwinNetworkWatch.Lock()
		darwinNetworkWatch.events = nil
		darwinNetworkWatch.Unlock()
	}()
	<-ctx.Done()
	return ctx.Err()
}
