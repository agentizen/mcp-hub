package main

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fastFactory returns a factory building subprocesses with an immediate
// readiness probe and a short start timeout — suitable for tests.
func fastFactory(probeCount *atomic.Int32, ready <-chan struct{}) subprocessFactory {
	return func(cfg SubprocessConfig, logger *slog.Logger) *Subprocess {
		sp := NewSubprocess(cfg, logger)
		sp.probe = func(ctx context.Context, port int) error {
			if probeCount != nil {
				probeCount.Add(1)
			}
			if ready == nil {
				return nil
			}
			select {
			case <-ready:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		sp.startTimeout = 2 * time.Second
		sp.stopTimeout = 2 * time.Second
		return sp
	}
}

func TestPool_GetOrSpawn_Concurrent_SpawnsOnce(t *testing.T) {
	var probeCount atomic.Int32
	ready := make(chan struct{})
	cfg := SubprocessConfig{Name: "only", Port: 9100, Command: []string{"sleep", "30"}}
	pool := NewPool([]SubprocessConfig{cfg}, newTestLogger())
	pool.factory = fastFactory(&probeCount, ready)
	defer pool.Shutdown(context.Background())

	const N = 50
	var wg sync.WaitGroup
	results := make([]*Subprocess, N)
	errs := make([]error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sp, err := pool.GetOrSpawn(context.Background(), "only")
			results[i] = sp
			errs[i] = err
		}(i)
	}

	// Give goroutines time to all converge on the Starting state.
	time.Sleep(50 * time.Millisecond)
	close(ready)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("g%d: %v", i, err)
		}
	}
	if got := probeCount.Load(); got != 1 {
		t.Errorf("probe invocations = %d, want 1", got)
	}
	for i := 1; i < N; i++ {
		if results[i] != results[0] {
			t.Errorf("g%d returned a different subprocess than g0", i)
		}
	}
}

func TestPool_GetOrSpawn_DuringStopping_WaitsAndRespawns(t *testing.T) {
	cfg := SubprocessConfig{Name: "s", Port: 9200, Command: []string{"sleep", "30"}}
	pool := NewPool([]SubprocessConfig{cfg}, newTestLogger())

	// Preload a stale Subprocess frozen in StateStopping.
	stale := NewSubprocess(cfg, newTestLogger())
	stoppedCh := make(chan struct{})
	stale.mu.Lock()
	stale.state = StateStopping
	stale.stoppedCh = stoppedCh
	stale.mu.Unlock()
	pool.mu.Lock()
	pool.subprocesses["s"] = stale
	pool.mu.Unlock()

	var spawned atomic.Int32
	pool.factory = func(cfg SubprocessConfig, logger *slog.Logger) *Subprocess {
		spawned.Add(1)
		sp := NewSubprocess(cfg, logger)
		sp.probe = func(ctx context.Context, port int) error { return nil }
		sp.startTimeout = 2 * time.Second
		return sp
	}

	var got *Subprocess
	var gotErr error
	done := make(chan struct{})
	go func() {
		got, gotErr = pool.GetOrSpawn(context.Background(), "s")
		close(done)
	}()

	// Give the goroutine time to enter the stoppedCh wait.
	time.Sleep(50 * time.Millisecond)

	// Release Stopping → Stopped transition.
	stale.mu.Lock()
	stale.state = StateStopped
	stale.stoppedCh = nil
	close(stoppedCh)
	stale.mu.Unlock()

	<-done
	defer pool.Shutdown(context.Background())

	if gotErr != nil {
		t.Fatalf("GetOrSpawn: %v", gotErr)
	}
	if got == stale {
		t.Errorf("GetOrSpawn returned stale Subprocess, want respawned instance")
	}
	if spawned.Load() != 1 {
		t.Errorf("factory called %d times, want exactly 1 respawn", spawned.Load())
	}
}

func TestPool_Sweep_KillsIdleSubprocesses(t *testing.T) {
	cfg := SubprocessConfig{Name: "idle", Port: 9300, Command: []string{"sleep", "30"}}
	pool := NewPool([]SubprocessConfig{cfg}, newTestLogger())
	pool.factory = fastFactory(nil, nil)
	sp, err := pool.GetOrSpawn(context.Background(), "idle")
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	sp.mu.Lock()
	sp.lastUsed = time.Now().Add(-2 * SubprocessIdleTTL)
	sp.mu.Unlock()

	pool.Sweep()

	state, _, _ := sp.snapshot()
	if state != StateStopped {
		t.Errorf("state after sweep = %s, want Stopped", state)
	}
}

func TestPool_Sweep_SkipsActiveSubprocesses(t *testing.T) {
	cfg := SubprocessConfig{Name: "active", Port: 9301, Command: []string{"sleep", "30"}}
	pool := NewPool([]SubprocessConfig{cfg}, newTestLogger())
	pool.factory = fastFactory(nil, nil)
	defer pool.Shutdown(context.Background())

	sp, err := pool.GetOrSpawn(context.Background(), "active")
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	pool.Sweep()

	state, _, _ := sp.snapshot()
	if state != StateRunning {
		t.Errorf("state after sweep = %s, want Running (fresh lastUsed)", state)
	}
}

func TestPool_Shutdown_StopsAllRunningSubprocesses(t *testing.T) {
	cfgs := []SubprocessConfig{
		{Name: "a", Port: 9400, Command: []string{"sleep", "30"}},
		{Name: "b", Port: 9401, Command: []string{"sleep", "30"}},
	}
	pool := NewPool(cfgs, newTestLogger())
	pool.factory = fastFactory(nil, nil)

	spA, err := pool.GetOrSpawn(context.Background(), "a")
	if err != nil {
		t.Fatalf("GetOrSpawn a: %v", err)
	}
	spB, err := pool.GetOrSpawn(context.Background(), "b")
	if err != nil {
		t.Fatalf("GetOrSpawn b: %v", err)
	}

	pool.Shutdown(context.Background())

	for name, sp := range map[string]*Subprocess{"a": spA, "b": spB} {
		state, _, _ := sp.snapshot()
		if state != StateStopped {
			t.Errorf("%s state after Shutdown = %s, want Stopped", name, state)
		}
	}
}

func TestPool_GetOrSpawn_UnknownName_ReturnsError(t *testing.T) {
	pool := NewPool(nil, newTestLogger())
	_, err := pool.GetOrSpawn(context.Background(), "nope")
	if err == nil || !strings.Contains(err.Error(), "unknown subprocess") {
		t.Errorf("want unknown-subprocess error, got %v", err)
	}
}

func TestPool_GetOrSpawn_ContextCancelledWhileStarting(t *testing.T) {
	cfg := SubprocessConfig{Name: "slow", Port: 9500, Command: []string{"sleep", "30"}}
	pool := NewPool([]SubprocessConfig{cfg}, newTestLogger())

	release := make(chan struct{})
	pool.factory = func(cfg SubprocessConfig, logger *slog.Logger) *Subprocess {
		sp := NewSubprocess(cfg, logger)
		sp.probe = func(ctx context.Context, port int) error {
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		sp.startTimeout = 5 * time.Second
		return sp
	}
	defer func() {
		close(release)
		pool.Shutdown(context.Background())
	}()

	// First call will start spawning and block in the probe.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var firstErr error
	go func() {
		_, firstErr = pool.GetOrSpawn(ctx, "slow")
		close(done)
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	<-done

	if firstErr == nil {
		t.Errorf("want ctx cancel error, got nil")
	}
}

func TestPool_RunSweeper_StopsOnContextDone(t *testing.T) {
	pool := NewPool(nil, newTestLogger())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		pool.RunSweeper(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunSweeper did not stop after ctx cancel")
	}
}
