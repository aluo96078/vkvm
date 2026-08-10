package network

import (
	"context"
	"errors"
	"time"
)

// SnapshotSource is the small abstraction consumed by the coordinator. Native
// implementations emit an event when the OS reports a route/interface change;
// the portable polling monitor remains the explicit fallback.
type SnapshotSource interface {
	Changes(context.Context) <-chan NetworkSnapshot
}

type nativeSnapshotSource struct {
	watch    func(context.Context, chan<- struct{}) error
	debounce time.Duration
	fallback time.Duration
}

func newNativeSnapshotSource(watch func(context.Context, chan<- struct{}) error, fallback time.Duration) SnapshotSource {
	if watch == nil {
		return nil
	}
	if fallback <= 0 {
		fallback = 2 * time.Second
	}
	return &nativeSnapshotSource{watch: watch, debounce: 2 * time.Second, fallback: fallback}
}

func (s *nativeSnapshotSource) Changes(ctx context.Context) <-chan NetworkSnapshot {
	out := make(chan NetworkSnapshot, 1)
	go func() {
		defer close(out)
		initial, err := CurrentNetworkSnapshot()
		if err == nil {
			out <- initial
		}
		events := make(chan struct{}, 1)
		watchErr := make(chan error, 1)
		go func() { watchErr <- s.watch(ctx, events) }()
		var timer *time.Timer
		var timerC <-chan time.Time
		lastKey := initial.Key()
		for {
			select {
			case <-ctx.Done():
				if timer != nil {
					timer.Stop()
				}
				return
			case err := <-watchErr:
				if err == nil || errors.Is(err, context.Canceled) || ctx.Err() != nil {
					return
				}
				// Native APIs can be unavailable in a restricted session (for
				// example a sandboxed app). Fall back to the existing bounded
				// polling source instead of silently losing reconnection.
				poll := NewNetworkMonitor(s.fallback)
				for snapshot := range poll.Changes(ctx) {
					select {
					case out <- snapshot:
					case <-ctx.Done():
						return
					}
				}
				return
			case <-events:
				if timer == nil {
					timer = time.NewTimer(s.debounce)
					timerC = timer.C
				} else {
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					timer.Reset(s.debounce)
				}
			case <-timerC:
				timerC = nil
				snapshot, err := CurrentNetworkSnapshot()
				if err != nil || snapshot.Key() == lastKey {
					continue
				}
				lastKey = snapshot.Key()
				select {
				case out <- snapshot:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out
}

// NewPlatformNetworkMonitor prefers an event-driven OS source and falls back
// to the portable monitor only when the native source cannot start.
func NewPlatformNetworkMonitor(interval time.Duration) SnapshotSource {
	if source := newNativePlatformNetworkSource(interval); source != nil {
		return source
	}
	return NewNetworkMonitor(interval)
}
