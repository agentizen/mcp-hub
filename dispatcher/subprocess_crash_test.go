package main

import (
	"context"
	"syscall"
	"testing"
	"time"
)

// TestSubprocess_CrashDetection_TransitionsToStopped asserts the reaper
// notices an unexpected process exit and flips the state machine back
// to Stopped so a future caller can respawn.
func TestSubprocess_CrashDetection_TransitionsToStopped(t *testing.T) {
	cfg := SubprocessConfig{
		Name:    "crash",
		Port:    9700,
		Command: []string{"sleep", "30"},
	}
	sp := NewSubprocess(cfg, newTestLogger())
	sp.probe = func(ctx context.Context, port int) error { return nil }
	sp.startTimeout = 2 * time.Second

	if err := sp.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Snapshot the cmd before killing it.
	sp.mu.Lock()
	cmd := sp.cmd
	sp.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		t.Fatal("no cmd.Process to signal")
	}

	// Send SIGKILL out of band — simulates a crash.
	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("signal: %v", err)
	}

	// Reaper should observe the exit and transition state.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state, _, _ := sp.snapshot()
		if state == StateStopped {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	state, _, _ := sp.snapshot()
	t.Fatalf("state = %s after crash, want Stopped", state)
}

// TestSubprocess_CrashDetection_AllowsRespawn proves that after an
// unexpected exit the subprocess struct can be Start()-ed again.
func TestSubprocess_CrashDetection_AllowsRespawn(t *testing.T) {
	cfg := SubprocessConfig{Name: "respawn", Port: 9701, Command: []string{"sleep", "30"}}
	sp := NewSubprocess(cfg, newTestLogger())
	sp.probe = func(ctx context.Context, port int) error { return nil }
	sp.startTimeout = 2 * time.Second

	if err := sp.Start(context.Background()); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	sp.mu.Lock()
	cmd := sp.cmd
	sp.mu.Unlock()
	_ = cmd.Process.Signal(syscall.SIGKILL)

	// Wait for crash reaper to flip state.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state, _, _ := sp.snapshot()
		if state == StateStopped {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Respawn
	if err := sp.Start(context.Background()); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	defer func() { _ = sp.Stop(context.Background()) }()

	state, _, _ := sp.snapshot()
	if state != StateRunning {
		t.Errorf("state after respawn = %s, want Running", state)
	}
}

// TestSubprocess_Path_Default returns /mcp when cfg.Path is empty and
// the configured value otherwise.
func TestSubprocess_Path_Default(t *testing.T) {
	sp := NewSubprocess(SubprocessConfig{Name: "a", Port: 9710}, newTestLogger())
	if got := sp.Path(); got != "/mcp" {
		t.Errorf("Path() = %q, want /mcp", got)
	}
	sp2 := NewSubprocess(SubprocessConfig{Name: "b", Port: 9711, Path: "/"}, newTestLogger())
	if got := sp2.Path(); got != "/" {
		t.Errorf("Path() = %q, want /", got)
	}
}
