// Package service provides platform-neutral lifecycle primitives for the VKVM
// core. Platform launchers remain responsible for starting the process.
package service

import (
	"context"
	"errors"
	"sync"
)

type State string

const (
	StateStopped  State = "stopped"
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateStopping State = "stopping"
	StateFailed   State = "failed"
)

var ErrAlreadyRunning = errors.New("service lifecycle already running")
var ErrNotRunning = errors.New("service lifecycle not running")

type Worker func(context.Context) error

// Lifecycle owns one service run and makes cancellation observable. A worker
// must stop when its context is cancelled; Stop has a caller-controlled wait
// deadline so a broken component cannot hang shutdown indefinitely.
type Lifecycle struct {
	mu     sync.RWMutex
	worker Worker
	state  State
	err    error
	cancel context.CancelFunc
	done   chan struct{}
}

func NewLifecycle(worker Worker) *Lifecycle {
	return &Lifecycle{worker: worker, state: StateStopped}
}

func (l *Lifecycle) Start(parent context.Context) error {
	if parent == nil {
		parent = context.Background()
	}
	l.mu.Lock()
	if l.state == StateStarting || l.state == StateRunning {
		l.mu.Unlock()
		return ErrAlreadyRunning
	}
	if l.worker == nil {
		l.mu.Unlock()
		return errors.New("service lifecycle worker is nil")
	}
	ctx, cancel := context.WithCancel(parent)
	l.cancel = cancel
	l.done = make(chan struct{})
	l.err = nil
	l.state = StateStarting
	done := l.done
	worker := l.worker
	l.mu.Unlock()

	go func() {
		l.mu.Lock()
		if l.state == StateStarting {
			l.state = StateRunning
		}
		l.mu.Unlock()
		err := worker(ctx)
		l.mu.Lock()
		l.err = err
		if err != nil && !errors.Is(err, context.Canceled) {
			l.state = StateFailed
		} else {
			l.state = StateStopped
		}
		l.cancel = nil
		close(done)
		l.mu.Unlock()
	}()
	return nil
}

func (l *Lifecycle) Stop(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	l.mu.Lock()
	if l.state != StateStarting && l.state != StateRunning && l.state != StateFailed {
		l.mu.Unlock()
		return nil
	}
	l.state = StateStopping
	cancel := l.cancel
	done := l.done
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	select {
	case <-done:
		l.mu.Lock()
		if l.state == StateStopping || l.state == StateFailed {
			l.state = StateStopped
		}
		l.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *Lifecycle) Restart(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := l.Stop(ctx); err != nil {
		return err
	}
	return l.Start(ctx)
}

func (l *Lifecycle) State() State {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.state
}

func (l *Lifecycle) Err() error {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.err
}

func (l *Lifecycle) Done() <-chan struct{} {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.done
}
