package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLifecycleStartStopIsIdempotentByState(t *testing.T) {
	started := make(chan struct{})
	l := NewLifecycle(func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	ctx := context.Background()
	if err := l.Start(ctx); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := l.Start(ctx); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Start error = %v", err)
	}
	stopCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := l.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	if l.State() != StateStopped {
		t.Fatalf("state = %s", l.State())
	}
}

func TestSupervisorProtectsCrashLoop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s := NewSupervisor(func(context.Context) error { return errors.New("boom") }, nil, SupervisorConfig{
		MinBackoff: 1 * time.Millisecond, MaxBackoff: 2 * time.Millisecond, MaxRestarts: 2,
		CrashLoopWindow: time.Second,
	})
	err := s.Run(ctx)
	if err == nil || s.State() != StateFailed || s.RestartCount() <= 2 {
		t.Fatalf("supervisor error=%v state=%s restarts=%d", err, s.State(), s.RestartCount())
	}
}

func TestSupervisorRunsHealthChecksUntilWorkerStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	healthCalls := make(chan struct{}, 4)
	s := NewSupervisor(func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}, func(context.Context) error {
		healthCalls <- struct{}{}
		return nil
	}, SupervisorConfig{HealthInterval: time.Millisecond, MinBackoff: time.Millisecond, MaxRestarts: 1})
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	select {
	case <-healthCalls:
	case <-time.After(time.Second):
		t.Fatal("first health check did not run")
	}
	select {
	case <-healthCalls:
	case <-time.After(time.Second):
		t.Fatal("second health check did not run")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("supervisor did not stop")
	}
}

func TestLifecycleRejectsNilWorkerAndSupportsRestart(t *testing.T) {
	if err := NewLifecycle(nil).Start(context.Background()); err == nil {
		t.Fatal("nil lifecycle worker accepted")
	}
	starts := make(chan struct{}, 2)
	l := NewLifecycle(func(ctx context.Context) error {
		starts <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := l.Start(ctx); err != nil {
		t.Fatal(err)
	}
	<-starts
	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	if err := l.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	stopCancel()
	if err := l.Restart(ctx); err != nil {
		t.Fatal(err)
	}
	<-starts
	if err := l.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorRejectsNilWorkerAndNormalCompletion(t *testing.T) {
	if err := NewSupervisor(nil, nil, SupervisorConfig{}).Run(context.Background()); err == nil {
		t.Fatal("nil supervisor worker accepted")
	}
	s := NewSupervisor(func(context.Context) error { return nil }, nil, SupervisorConfig{MinBackoff: time.Millisecond})
	if err := s.Run(context.Background()); err != nil || s.State() != StateStopped {
		t.Fatalf("normal supervisor completion err=%v state=%s", err, s.State())
	}
}

func TestSupervisorConvertsWorkerPanicIntoCrashLoopFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s := NewSupervisor(func(context.Context) error { panic("boom") }, nil, SupervisorConfig{
		MinBackoff: time.Millisecond, MaxBackoff: time.Millisecond, MaxRestarts: 1,
	})
	if err := s.Run(ctx); err == nil || s.State() != StateFailed {
		t.Fatalf("panic was not supervised: err=%v state=%s", err, s.State())
	}
}
