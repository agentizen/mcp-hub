package main

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestDefaultReadyProbe_ListeningSocket(t *testing.T) {
	// Bind a real listener on an ephemeral port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	port := ln.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := defaultReadyProbe(ctx, port); err != nil {
		t.Errorf("defaultReadyProbe = %v, want nil (socket is listening)", err)
	}
}

func TestDefaultReadyProbe_TimesOut(t *testing.T) {
	// Pick a port nobody is listening on. Port 1 is privileged and safe.
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	err := defaultReadyProbe(ctx, 1)
	if err == nil {
		t.Error("want timeout error, got nil")
	}
}

func TestNewSubprocess_NilLogger(t *testing.T) {
	sp := NewSubprocess(SubprocessConfig{Name: "n", Port: 9999}, nil)
	if sp == nil || sp.logger == nil {
		t.Error("NewSubprocess(nil logger) should default to slog.Default")
	}
}

func TestNewDispatcher_NilLogger(t *testing.T) {
	d := NewDispatcher(&Config{}, NewPool(nil, nil), nil)
	if d == nil || d.logger == nil {
		t.Error("NewDispatcher(nil logger) should default to slog.Default")
	}
}

func TestIsHopByHop(t *testing.T) {
	for _, h := range []string{"Connection", "TE", "Trailers", "Upgrade", "Keep-Alive"} {
		if !isHopByHop(h) {
			t.Errorf("isHopByHop(%q) = false, want true", h)
		}
	}
	for _, h := range []string{"Authorization", "Content-Type", "X-Custom"} {
		if isHopByHop(h) {
			t.Errorf("isHopByHop(%q) = true, want false", h)
		}
	}
}
