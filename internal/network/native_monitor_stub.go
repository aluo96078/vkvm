//go:build !darwin && !windows

package network

import "time"

func newNativePlatformNetworkSource(_ time.Duration) SnapshotSource { return nil }
