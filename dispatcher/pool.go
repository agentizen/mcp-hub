package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// subprocessFactory builds a fresh Subprocess from a config. Pluggable
// so tests can inject probes and shortened timeouts.
type subprocessFactory func(cfg SubprocessConfig, logger *slog.Logger) *Subprocess

// Pool owns the live set of subprocesses keyed by name. Lifecycle rules:
//   - Subprocesses are lazy: nothing is spawned until GetOrSpawn is called.
//   - A single Pool.mu protects the map only. Subprocess.mu protects the
//     state of one entry. The two locks are NEVER held simultaneously.
//   - On Stopping, waiters are allowed to respawn a fresh Subprocess once
//     the old one reaches Stopped.
type Pool struct {
	mu           sync.Mutex
	subprocesses map[string]*Subprocess
	configs      map[string]SubprocessConfig
	logger       *slog.Logger
	factory      subprocessFactory
}

// NewPool builds a Pool from the list of subprocess configs. It does NOT
// spawn anything.
func NewPool(cfgs []SubprocessConfig, logger *slog.Logger) *Pool {
	if logger == nil {
		logger = slog.Default()
	}
	configs := make(map[string]SubprocessConfig, len(cfgs))
	for _, c := range cfgs {
		configs[c.Name] = c
	}
	return &Pool{
		subprocesses: make(map[string]*Subprocess),
		configs:      configs,
		logger:       logger,
		factory:      NewSubprocess,
	}
}

// GetOrSpawn returns a Running subprocess for the given name, spawning it
// lazily on first access. Safely coordinates concurrent callers on the
// same name and handles the Starting / Stopping transitions.
func (p *Pool) GetOrSpawn(ctx context.Context, name string) (*Subprocess, error) {
	for attempt := 0; attempt < PoolRetryAttempts; attempt++ {
		sp, err := p.getOrCreateEntry(name)
		if err != nil {
			return nil, err
		}

		state, readyCh, stoppedCh := sp.snapshot()
		switch state {
		case StateRunning:
			sp.touch()
			return sp, nil

		case StateStarting:
			if err := waitCh(ctx, readyCh); err != nil {
				return nil, err
			}
			continue

		case StateStopping:
			if err := waitCh(ctx, stoppedCh); err != nil {
				return nil, err
			}
			// Evict the stale entry so next iteration creates a fresh one.
			p.mu.Lock()
			if p.subprocesses[name] == sp {
				delete(p.subprocesses, name)
			}
			p.mu.Unlock()
			continue

		case StateStopped:
			if err := sp.Start(ctx); err != nil {
				if errors.Is(err, ErrSubprocessBusy) {
					// Another caller won the race; re-snapshot and retry.
					continue
				}
				// Hard failure — evict so we don't keep a broken entry.
				p.mu.Lock()
				if p.subprocesses[name] == sp {
					delete(p.subprocesses, name)
				}
				p.mu.Unlock()
				return nil, fmt.Errorf("pool: subprocess %q: %w", name, err)
			}
			continue
		}
	}
	return nil, fmt.Errorf("pool: exceeded retry attempts for %q", name)
}

// getOrCreateEntry looks up (or inserts) the Subprocess for name under
// the pool lock only. Returns an error for unknown names.
func (p *Pool) getOrCreateEntry(name string) (*Subprocess, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	sp, ok := p.subprocesses[name]
	if ok {
		return sp, nil
	}
	cfg, known := p.configs[name]
	if !known {
		return nil, fmt.Errorf("pool: unknown subprocess %q", name)
	}
	sp = p.factory(cfg, p.logger)
	p.subprocesses[name] = sp
	return sp, nil
}

// Sweep stops any subprocess that has been idle longer than
// SubprocessIdleTTL. Safe to call from a goroutine.
func (p *Pool) Sweep() {
	p.sweepAt(time.Now())
}

// sweepAt is the testable variant of Sweep: it lets tests pass a
// controlled "now" value.
func (p *Pool) sweepAt(now time.Time) {
	// Snapshot idle candidates under lock, then release before stopping.
	p.mu.Lock()
	candidates := make([]*Subprocess, 0)
	for _, sp := range p.subprocesses {
		if sp.isIdle(now) {
			candidates = append(candidates, sp)
		}
	}
	p.mu.Unlock()

	for _, sp := range candidates {
		if err := sp.Stop(context.Background()); err != nil && !errors.Is(err, ErrSubprocessBusy) {
			p.logger.Warn("sweep stop failed", "name", sp.Name(), "err", err)
		}
	}
}

// RunSweeper ticks every SubprocessSweepInterval and runs Sweep until ctx
// is canceled.
func (p *Pool) RunSweeper(ctx context.Context) {
	ticker := time.NewTicker(SubprocessSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.Sweep()
		}
	}
}

// Shutdown stops every running subprocess. Honors the caller's ctx for
// the overall deadline; individual Stop calls are bounded by
// SubprocessStopTimeout.
func (p *Pool) Shutdown(ctx context.Context) {
	p.mu.Lock()
	targets := make([]*Subprocess, 0, len(p.subprocesses))
	for _, sp := range p.subprocesses {
		targets = append(targets, sp)
	}
	p.mu.Unlock()

	var wg sync.WaitGroup
	for _, sp := range targets {
		wg.Add(1)
		go func(s *Subprocess) {
			defer wg.Done()
			state, _, _ := s.snapshot()
			if state != StateRunning {
				return
			}
			if err := s.Stop(ctx); err != nil && !errors.Is(err, ErrSubprocessBusy) {
				p.logger.Warn("shutdown stop failed", "name", s.Name(), "err", err)
			}
		}(sp)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// waitCh blocks until ch is closed or ctx is done.
func waitCh(ctx context.Context, ch chan struct{}) error {
	if ch == nil {
		return nil
	}
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
