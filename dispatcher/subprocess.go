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
//   - readyCh/stoppedCh/exitCh are freshly allocated on each state
//     transition and closed once the transition completes (success or
//     failure). readyCh is closed by Start, stoppedCh by Stop, exitCh
//     by the reaper goroutine that owns the single cmd.Process.Wait call.
type Subprocess struct {
	cfg    SubprocessConfig
	logger *slog.Logger

	mu        sync.Mutex
	state     SubprocessState
	cmd       *exec.Cmd
	lastUsed  time.Time
	readyCh   chan struct{}
	stoppedCh chan struct{}
	exitCh    chan struct{}

	// probeCancel cancels the in-flight probe context so drainAndStop
	// can unblock a Start stuck in readiness polling. nil when no probe
	// is running.
	probeCancel context.CancelFunc

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

// Path returns the configured URL path for upstream forwarding. Defaults
// to "/mcp" when the operator omitted it in the YAML.
func (s *Subprocess) Path() string {
	if s.cfg.Path == "" {
		return "/mcp"
	}
	return s.cfg.Path
}

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

	cmd := exec.Command(s.cfg.Command[0], s.cfg.Command[1:]...) // #nosec G204 — command comes from operator config
	cmd.Dir = s.cfg.Cwd
	if len(s.cfg.Env) > 0 {
		env := os.Environ()
		for k, v := range s.cfg.Env {
			env = append(env, fmt.Sprintf("%s=%s", k, v))
		}
		cmd.Env = env
	}
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("subprocess %q: exec start: %w", s.cfg.Name, err)
	}

	readyCh := make(chan struct{})
	exitCh := make(chan struct{})
	s.cmd = cmd
	s.readyCh = readyCh
	s.exitCh = exitCh
	s.state = StateStarting
	s.logger.Info("subprocess starting", "name", s.cfg.Name, "port", s.cfg.Port, "pid", cmd.Process.Pid)
	s.mu.Unlock()

	// Single owner of cmd.Process.Wait: the reaper. Runs for the whole
	// lifetime of this cmd instance and closes exitCh when the process
	// has been reaped (graceful Stop, crash, or Start rollback).
	go s.reap(cmd, exitCh)

	probeCtx, cancel := context.WithTimeout(ctx, s.startTimeout)
	s.mu.Lock()
	s.probeCancel = cancel
	s.mu.Unlock()
	probeErr := s.probe(probeCtx, s.cfg.Port)

	s.mu.Lock()
	s.probeCancel = nil
	cancel()
	if probeErr != nil {
		_ = cmd.Process.Kill()
		s.state = StateStopped
		s.cmd = nil
		s.exitCh = nil
		s.readyCh = nil
		close(readyCh)
		s.mu.Unlock()
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

// reap is the single goroutine that waits on cmd.Process.Wait. It is
// launched by Start and runs until the process has exited. On unexpected
// exits (state still Running when Wait returns) it transitions the
// state machine to Stopped so future requests respawn cleanly.
func (s *Subprocess) reap(cmd *exec.Cmd, exitCh chan struct{}) {
	_, waitErr := cmd.Process.Wait()

	s.mu.Lock()
	// Only handle the unexpected-exit case. Normal Stop and Start
	// rollback paths own their own state transitions.
	if s.cmd == cmd && s.state == StateRunning {
		s.state = StateStopped
		s.cmd = nil
		s.exitCh = nil
		s.lastUsed = time.Time{}
		s.logger.Warn("subprocess exited unexpectedly", "name", s.cfg.Name, "err", waitErr)
	}
	s.mu.Unlock()
	close(exitCh)
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
	exitCh := s.exitCh
	stoppedCh := make(chan struct{})
	s.stoppedCh = stoppedCh
	s.state = StateStopping
	s.logger.Info("subprocess stopping", "name", s.cfg.Name, "pid", cmd.Process.Pid)
	s.mu.Unlock()

	_ = cmd.Process.Signal(syscall.SIGTERM)

	stopCtx, cancel := context.WithTimeout(ctx, s.stopTimeout)
	defer cancel()

	select {
	case <-exitCh:
	case <-stopCtx.Done():
		_ = cmd.Process.Kill()
		<-exitCh
	}

	s.mu.Lock()
	s.state = StateStopped
	s.cmd = nil
	s.exitCh = nil
	s.stoppedCh = nil
	s.lastUsed = time.Time{}
	close(stoppedCh)
	s.mu.Unlock()
	s.logger.Info("subprocess stopped", "name", s.cfg.Name)
	return nil
}

// drainAndStop is called from Pool.Shutdown. It ensures the underlying
// process is dead regardless of the current state: it aborts in-flight
// probes, force-kills Starting subprocesses, and Stop()s Running ones.
// Safe to call concurrently with Start.
func (s *Subprocess) drainAndStop(ctx context.Context) {
	for {
		state, readyCh, stoppedCh := s.snapshot()
		switch state {
		case StateStopped:
			return

		case StateStopping:
			select {
			case <-stoppedCh:
				return
			case <-ctx.Done():
				return
			}

		case StateStarting:
			// Force the probe to abort quickly and kill the process so
			// the Start rollback unblocks.
			s.mu.Lock()
			cancel := s.probeCancel
			cmd := s.cmd
			s.mu.Unlock()
			if cancel != nil {
				cancel()
			}
			if cmd != nil {
				_ = cmd.Process.Kill()
			}
			select {
			case <-readyCh:
				// Re-snapshot after rollback completes.
				continue
			case <-ctx.Done():
				return
			}

		case StateRunning:
			if err := s.Stop(ctx); err != nil && !errors.Is(err, ErrSubprocessBusy) {
				s.logger.Warn("drain stop failed", "name", s.cfg.Name, "err", err)
			}
			return
		}
	}
}

// defaultReadyProbe attempts a TCP connect to 127.0.0.1:<port> every
// readyPollInterval until ctx deadline or a connection succeeds.
func defaultReadyProbe(ctx context.Context, port int) error {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	dialer := net.Dialer{Timeout: readyDialTimeout}
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
