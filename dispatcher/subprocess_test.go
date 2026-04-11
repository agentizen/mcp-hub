package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// sleepConfig returns a config that spawns `sleep 30` — a harmless real
// subprocess we can kill. The probe is injected separately to avoid any
// actual port dependency.
func sleepConfig(name string, port int) SubprocessConfig {
	return SubprocessConfig{
		Name:    name,
		Type:    "test",
		Port:    port,
		Command: []string{"sleep", "30"},
	}
}

func TestSubprocess_StartStop_HappyPath(t *testing.T) {
	sp := NewSubprocess(sleepConfig("happy", 9000), newTestLogger())
	sp.probe = func(ctx context.Context, port int) error { return nil }
	sp.startTimeout = 2 * time.Second

	if err := sp.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	state, _, _ := sp.snapshot()
	if state != StateRunning {
		t.Errorf("state after Start = %s, want Running", state)
	}

	if err := sp.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	state, _, _ = sp.snapshot()
	if state != StateStopped {
		t.Errorf("state after Stop = %s, want Stopped", state)
	}
}

func TestSubprocess_StartTimeout(t *testing.T) {
	sp := NewSubprocess(sleepConfig("timeout", 9001), newTestLogger())
	sp.probe = func(ctx context.Context, port int) error {
		<-ctx.Done()
		return ctx.Err()
	}
	sp.startTimeout = 150 * time.Millisecond

	err := sp.Start(context.Background())
	if err == nil {
		t.Fatal("want start error, got nil")
	}
	state, _, _ := sp.snapshot()
	if state != StateStopped {
		t.Errorf("state after failed Start = %s, want Stopped", state)
	}
	// Subsequent Start must succeed fresh.
	sp.probe = func(ctx context.Context, port int) error { return nil }
	sp.startTimeout = 2 * time.Second
	if err := sp.Start(context.Background()); err != nil {
		t.Fatalf("recovery Start: %v", err)
	}
	_ = sp.Stop(context.Background())
}

func TestSubprocess_DoubleStart_ReturnsBusy(t *testing.T) {
	sp := NewSubprocess(sleepConfig("double", 9002), newTestLogger())
	sp.probe = func(ctx context.Context, port int) error { return nil }
	sp.startTimeout = 2 * time.Second

	if err := sp.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = sp.Stop(context.Background()) }()

	err := sp.Start(context.Background())
	if !errors.Is(err, ErrSubprocessBusy) {
		t.Errorf("second Start = %v, want ErrSubprocessBusy", err)
	}
}

func TestSubprocess_StopFromStopped_ReturnsBusy(t *testing.T) {
	sp := NewSubprocess(sleepConfig("idle", 9003), newTestLogger())
	err := sp.Stop(context.Background())
	if !errors.Is(err, ErrSubprocessBusy) {
		t.Errorf("Stop on Stopped = %v, want ErrSubprocessBusy", err)
	}
}

func TestSubprocess_Snapshot_ReturnsNilChannelsWhenStable(t *testing.T) {
	sp := NewSubprocess(sleepConfig("snap", 9004), newTestLogger())
	state, readyCh, stoppedCh := sp.snapshot()
	if state != StateStopped {
		t.Errorf("initial state = %s, want Stopped", state)
	}
	if readyCh != nil || stoppedCh != nil {
		t.Errorf("channels should be nil in Stopped, got ready=%v stopped=%v", readyCh, stoppedCh)
	}
}

func TestSubprocess_IsIdle(t *testing.T) {
	sp := NewSubprocess(sleepConfig("idle-check", 9005), newTestLogger())
	// Stopped → never idle
	if sp.isIdle(time.Now()) {
		t.Error("Stopped subprocess should not be idle")
	}

	sp.probe = func(ctx context.Context, port int) error { return nil }
	sp.startTimeout = 2 * time.Second
	if err := sp.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = sp.Stop(context.Background()) }()

	// Just-started → not idle
	if sp.isIdle(time.Now()) {
		t.Error("freshly started subprocess should not be idle")
	}
	// Simulate old lastUsed.
	sp.mu.Lock()
	sp.lastUsed = time.Now().Add(-2 * SubprocessIdleTTL)
	sp.mu.Unlock()
	if !sp.isIdle(time.Now()) {
		t.Error("stale subprocess should be idle")
	}
}

func TestSubprocess_Start_InvalidBinary(t *testing.T) {
	cfg := SubprocessConfig{
		Name:    "bad",
		Port:    9006,
		Command: []string{"/nonexistent/binary/please"},
	}
	sp := NewSubprocess(cfg, newTestLogger())
	sp.probe = func(ctx context.Context, port int) error { return nil }

	err := sp.Start(context.Background())
	if err == nil {
		t.Fatal("want exec error for missing binary, got nil")
	}
	state, _, _ := sp.snapshot()
	if state != StateStopped {
		t.Errorf("state after exec failure = %s, want Stopped", state)
	}
}

func TestSubprocess_Port(t *testing.T) {
	sp := NewSubprocess(sleepConfig("port", 9123), newTestLogger())
	if sp.Port() != 9123 {
		t.Errorf("Port() = %d, want 9123", sp.Port())
	}
	if sp.Name() != "port" {
		t.Errorf("Name() = %q, want %q", sp.Name(), "port")
	}
}

func TestSubprocessState_String(t *testing.T) {
	cases := map[SubprocessState]string{
		StateStopped:  "Stopped",
		StateStarting: "Starting",
		StateRunning:  "Running",
		StateStopping: "Stopping",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("State %d = %q, want %q", s, got, want)
		}
	}
}
