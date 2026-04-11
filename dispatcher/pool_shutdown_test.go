package main

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

// TestPool_Shutdown_KillsStartingSubprocess asserts that a subprocess
// stuck in the probe phase is force-killed by drainAndStop so the OS
// process never outlives the dispatcher.
func TestPool_Shutdown_KillsStartingSubprocess(t *testing.T) {
	cfg := SubprocessConfig{Name: "slow", Port: 9800, Command: []string{"sleep", "30"}}
	pool := NewPool([]SubprocessConfig{cfg}, newTestLogger())

	// Probe blocks until ctx is canceled — simulates a subprocess that
	// never finishes listening on its port.
	pool.factory = func(cfg SubprocessConfig, logger *slog.Logger) *Subprocess {
		sp := NewSubprocess(cfg, logger)
		sp.probe = func(ctx context.Context, port int) error {
			<-ctx.Done()
			return ctx.Err()
		}
		sp.startTimeout = 30 * time.Second // would normally block for 30s
		sp.stopTimeout = 2 * time.Second
		return sp
	}

	// Kick off GetOrSpawn in the background. It will block in Start's
	// probe until drainAndStop aborts it.
	errCh := make(chan error, 1)
	go func() {
		_, err := pool.GetOrSpawn(context.Background(), "slow")
		errCh <- err
	}()

	// Wait until the subprocess is in Starting state.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		pool.mu.Lock()
		sp := pool.subprocesses["slow"]
		pool.mu.Unlock()
		if sp != nil {
			state, _, _ := sp.snapshot()
			if state == StateStarting {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Shutdown should abort the probe and kill the process quickly.
	start := time.Now()
	pool.Shutdown(context.Background())
	elapsed := time.Since(start)
	if elapsed > 5*time.Second {
		t.Errorf("Shutdown took %s, want < 5s (probe was 30s)", elapsed)
	}

	// GetOrSpawn goroutine should have returned with an error.
	select {
	case err := <-errCh:
		if err == nil {
			t.Error("GetOrSpawn returned nil; want error after shutdown")
		}
	case <-time.After(5 * time.Second):
		t.Error("GetOrSpawn goroutine never returned")
	}

	// Final state should be Stopped.
	pool.mu.Lock()
	sp := pool.subprocesses["slow"]
	pool.mu.Unlock()
	if sp != nil {
		state, _, _ := sp.snapshot()
		if state != StateStopped {
			t.Errorf("final state = %s, want Stopped", state)
		}
	}
}
