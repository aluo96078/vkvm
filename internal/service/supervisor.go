package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

type HealthFunc func(context.Context) error

type SupervisorConfig struct {
	MinBackoff      time.Duration
	MaxBackoff      time.Duration
	MaxRestarts     int
	CrashLoopWindow time.Duration
	HealthInterval  time.Duration
	Status          *StatusStore
}

type Supervisor struct {
	worker   Worker
	health   HealthFunc
	cfg      SupervisorConfig
	mu       sync.RWMutex
	state    State
	lastErr  error
	restarts int
}

func NewSupervisor(worker Worker, health HealthFunc, cfg SupervisorConfig) *Supervisor {
	if cfg.MinBackoff <= 0 {
		cfg.MinBackoff = 250 * time.Millisecond
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = 30 * time.Second
	}
	if cfg.MaxBackoff < cfg.MinBackoff {
		cfg.MaxBackoff = cfg.MinBackoff
	}
	if cfg.MaxRestarts <= 0 {
		cfg.MaxRestarts = 5
	}
	if cfg.CrashLoopWindow <= 0 {
		cfg.CrashLoopWindow = 2 * time.Minute
	}
	return &Supervisor{worker: worker, health: health, cfg: cfg, state: StateStopped}
}

// Run supervises a core worker until cancellation or a crash-loop threshold is
// reached. A normally cancelled worker exits without being counted as a crash.
func (s *Supervisor) Run(ctx context.Context) error {
	if s.worker == nil {
		return errors.New("service supervisor worker is nil")
	}
	s.setState(StateRunning)
	if s.cfg.Status != nil {
		s.cfg.Status.SetState(StateRunning, nil, s.RestartCount())
	}
	backoff := s.cfg.MinBackoff
	var crashWindowStart time.Time
	for {
		if err := ctx.Err(); err != nil {
			s.setState(StateStopped)
			if s.cfg.Status != nil {
				s.cfg.Status.SetState(StateStopped, err, s.RestartCount())
			}
			return err
		}
		workerCtx, cancel := context.WithCancel(ctx)
		errCh := make(chan error, 1)
		go func() { errCh <- runWorkerSafely(s.worker, workerCtx) }()
		var healthTicker *time.Ticker
		var healthC <-chan time.Time
		if s.cfg.HealthInterval > 0 {
			healthTicker = time.NewTicker(s.cfg.HealthInterval)
			healthC = healthTicker.C
		}
		var err error
		workerFinished := false
		for !workerFinished {
			select {
			case err = <-errCh:
				workerFinished = true
			case <-ctx.Done():
				cancel()
				err = ctx.Err()
				workerFinished = true
			case <-healthC:
				if s.health == nil {
					continue
				}
				if healthErr := s.health(ctx); healthErr != nil {
					cancel()
					err = fmt.Errorf("health check failed: %w", healthErr)
				}
			}
		}
		if healthTicker != nil {
			healthTicker.Stop()
		}
		cancel()
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			s.setState(StateStopped)
			if s.cfg.Status != nil {
				s.cfg.Status.SetState(StateStopped, ctx.Err(), s.RestartCount())
			}
			return ctx.Err()
		}
		if err == nil {
			s.setState(StateStopped)
			if s.cfg.Status != nil {
				s.cfg.Status.SetState(StateStopped, nil, s.RestartCount())
			}
			return nil
		}
		s.mu.Lock()
		s.lastErr = err
		now := time.Now()
		if crashWindowStart.IsZero() || now.Sub(crashWindowStart) > s.cfg.CrashLoopWindow {
			crashWindowStart = now
			s.restarts = 0
		}
		s.restarts++
		restarts := s.restarts
		s.mu.Unlock()
		if restarts > s.cfg.MaxRestarts {
			s.setState(StateFailed)
			if s.cfg.Status != nil {
				s.cfg.Status.SetState(StateFailed, err, restarts)
			}
			return fmt.Errorf("crash loop protected after %d restarts: %w", restarts, err)
		}
		timer := time.NewTimer(backoff)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			s.setState(StateStopped)
			if s.cfg.Status != nil {
				s.cfg.Status.SetState(StateStopped, ctx.Err(), s.RestartCount())
			}
			return ctx.Err()
		}
		if backoff < s.cfg.MaxBackoff {
			backoff *= 2
			if backoff > s.cfg.MaxBackoff {
				backoff = s.cfg.MaxBackoff
			}
		}
	}
}

// runWorkerSafely converts a component panic into a supervised failure. A
// panic must not silently tear down the process and bypass crash-loop policy.
func runWorkerSafely(worker Worker, ctx context.Context) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("service worker panic: %v", recovered)
		}
	}()
	return worker(ctx)
}

func (s *Supervisor) setState(state State) {
	s.mu.Lock()
	s.state = state
	s.mu.Unlock()
}

func (s *Supervisor) State() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

func (s *Supervisor) LastError() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastErr
}

func (s *Supervisor) RestartCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.restarts
}
