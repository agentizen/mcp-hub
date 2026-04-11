package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestRun_HealthEndpoint_EmptyConfig(t *testing.T) {
	cfg := &Config{Handles: map[string]HandleConfig{}}
	port := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- run(ctx, cfg, port, newTestLogger()) }()

	// Poll until /health answers.
	var body []byte
	url := fmt.Sprintf("http://127.0.0.1:%d/health", port)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url) // #nosec G107 — loopback
		if err == nil {
			body, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(body) == 0 {
		cancel()
		<-done
		t.Fatal("health endpoint never answered")
	}

	var hr HealthResponse
	if err := json.Unmarshal(body, &hr); err != nil {
		cancel()
		<-done
		t.Fatalf("unmarshal: %v — body=%s", err, body)
	}
	if hr.Status != "ok" {
		t.Errorf("status = %q, want ok", hr.Status)
	}
	if hr.Handles == nil || len(hr.Handles) != 0 {
		t.Errorf("handles = %v, want empty slice", hr.Handles)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("run returned err: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not shutdown after ctx cancel")
	}
}

func TestRun_FailsWhenPortBusy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	busy := ln.Addr().(*net.TCPAddr).Port

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = run(ctx, &Config{}, busy, newTestLogger())
	if err == nil {
		t.Error("want run error when port is busy, got nil")
	}
}
