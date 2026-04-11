package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// ErrSubprocessBusy is returned when Start/Stop is called while the
// subprocess is not in the expected state. The pool uses this to retry.
var ErrSubprocessBusy = errors.New("subprocess not in expected state")

// SubprocessState enumerates the lifecycle states of a single subprocess.
type SubprocessState int

const (
	StateStopped SubprocessState = iota
	StateStarting
	StateRunning
	StateStopping
)

func (s SubprocessState) String() string {
	switch s {
	case StateStopped:
		return "Stopped"
	case StateStarting:
		return "Starting"
	case StateRunning:
		return "Running"
	case StateStopping:
		return "Stopping"
	default:
		return fmt.Sprintf("unknown(%d)", int(s))
	}
}

// readyProbe is a pluggable readiness check. Default: TCP connect to
// 127.0.0.1:<port>. Tests inject their own.
type readyProbe func(ctx context.Context, port int) error

// Subprocess owns a single backend process and its state machine.
//
// Concurrency contract:
//   - All state transitions take s.mu.
//   - Blocking operations (exec.Wait, readiness polls) run WITHOUT holding s.mu.
//   - readyCh/stoppedCh are freshly allocated on each state transition and
//     closed once the transition completes (success or failure).
type Subprocess struct {
	cfg    SubprocessConfig
	logger *slog.Logger

	mu        sync.Mutex
	state     SubprocessState
	cmd       *exec.Cmd
	lastUsed  time.Time
	readyCh   chan struct{}
	stoppedCh chan struct{}

	// Injectable hooks (tests only). Zero values use sensible defaults.
	probe        readyProbe
	startTimeout time.Duration
	stopTimeout  time.Duration
}

// NewSubprocess constructs a fresh Subprocess in StateStopped.
func NewSubprocess(cfg SubprocessConfig, logger *slog.Logger) *Subprocess {
	if logger == nil {
		logger = slog.Default()
	}
	return &Subprocess{
		cfg:          cfg,
		logger:       logger,
		state:        StateStopped,
		probe:        defaultReadyProbe,
		startTimeout: SubprocessStartTimeout,
		stopTimeout:  SubprocessStopTimeout,
	}
}

// Port returns the configured port (thread-safe; cfg is immutable).
func (s *Subprocess) Port() int { return s.cfg.Port }

// Name returns the configured subprocess name.
func (s *Subprocess) Name() string { return s.cfg.Name }

// snapshot returns the current state plus the active transition channels.
// Callers MUST NOT hold s.mu before calling.
func (s *Subprocess) snapshot() (SubprocessState, chan struct{}, chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state, s.readyCh, s.stoppedCh
}

// touch refreshes lastUsed to now. Ignored when not Running.
func (s *Subprocess) touch() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == StateRunning {
		s.lastUsed = time.Now()
	}
}

// isIdle reports whether the subprocess should be swept.
func (s *Subprocess) isIdle(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != StateRunning {
		return false
	}
	return now.Sub(s.lastUsed) >= SubprocessIdleTTL
}

// Start spawns the backend process and blocks until the readiness probe
// succeeds or SubprocessStartTimeout elapses. Returns ErrSubprocessBusy
// when the subprocess is not in StateStopped at entry.
func (s *Subprocess) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.state != StateStopped {
		s.mu.Unlock()
		return ErrSubprocessBusy
	}

	// Build and spawn the command. cmd.Start returns quickly; failures
	// here (binary not found, cwd missing) roll back immediately.
	cmd := exec.Command(s.cfg.Command[0], s.cfg.Command[1:]...) // #nosec G204 — command comes from operator config
	cmd.Dir = s.cfg.Cwd
	if len(s.cfg.Env) > 0 {
		env := os.Environ()
		for k, v := range s.cfg.Env {
			env = append(env, fmt.Sprintf("%s=%s", k, v))
		}
		cmd.Env = env
	}
	// Ensure the child doesn't inherit our stdio — drop output to /dev/null.
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("subprocess %q: exec start: %w", s.cfg.Name, err)
	}

	readyCh := make(chan struct{})
	s.cmd = cmd
	s.readyCh = readyCh
	s.state = StateStarting
	s.logger.Info("subprocess starting", "name", s.cfg.Name, "port", s.cfg.Port, "pid", cmd.Process.Pid)
	s.mu.Unlock()

	// Poll readiness without holding the lock.
	probeCtx, cancel := context.WithTimeout(ctx, s.startTimeout)
	defer cancel()
	probeErr := s.probe(probeCtx, s.cfg.Port)

	s.mu.Lock()
	if probeErr != nil {
		_ = cmd.Process.Kill()
		s.state = StateStopped
		s.cmd = nil
		s.readyCh = nil
		close(readyCh)
		s.mu.Unlock()
		_, _ = cmd.Process.Wait() // reap outside the lock
		s.logger.Warn("subprocess start failed", "name", s.cfg.Name, "err", probeErr)
		return fmt.Errorf("subprocess %q: ready check: %w", s.cfg.Name, probeErr)
	}

	s.state = StateRunning
	s.lastUsed = time.Now()
	s.readyCh = nil
	close(readyCh)
	s.mu.Unlock()
	s.logger.Info("subprocess running", "name", s.cfg.Name, "port", s.cfg.Port)
	return nil
}

// Stop sends SIGTERM, waits up to SubprocessStopTimeout, then falls back
// to SIGKILL. Returns ErrSubprocessBusy when not currently Running.
func (s *Subprocess) Stop(ctx context.Context) error {
	s.mu.Lock()
	if s.state != StateRunning {
		s.mu.Unlock()
		return ErrSubprocessBusy
	}
	cmd := s.cmd
	stoppedCh := make(chan struct{})
	s.stoppedCh = stoppedCh
	s.state = StateStopping
	s.logger.Info("subprocess stopping", "name", s.cfg.Name, "pid", cmd.Process.Pid)
	s.mu.Unlock()

	// Graceful SIGTERM, fall back to SIGKILL on timeout.
	_ = cmd.Process.Signal(syscall.SIGTERM)

	done := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(done)
	}()

	stopCtx, cancel := context.WithTimeout(ctx, s.stopTimeout)
	defer cancel()

	select {
	case <-done:
	case <-stopCtx.Done():
		_ = cmd.Process.Kill()
		<-done
	}

	s.mu.Lock()
	s.state = StateStopped
	s.cmd = nil
	s.stoppedCh = nil
	s.lastUsed = time.Time{}
	close(stoppedCh)
	s.mu.Unlock()
	s.logger.Info("subprocess stopped", "name", s.cfg.Name)
	return nil
}

// defaultReadyProbe attempts a TCP connect to 127.0.0.1:<port> every
// readyPollInterval until ctx deadline or a connection succeeds.
func defaultReadyProbe(ctx context.Context, port int) error {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	dialer := net.Dialer{Timeout: readyPollInterval}
	for {
		conn, err := dialer.DialContext(ctx, "tcp", addr)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(readyPollInterval):
		}
	}
}
