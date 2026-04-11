package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthHandler_ReturnsStatusAndHandles(t *testing.T) {
	cfg := &Config{
		Handles: map[string]HandleConfig{
			"gmail":   {Remote: "r"},
			"outlook": {Subprocess: "s"},
		},
	}
	h := NewHealthHandler(cfg)
	rr := httptest.NewRecorder()
	h(rr, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rr.Code)
	}
	var resp HealthResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("status = %q, want ok", resp.Status)
	}
	if len(resp.Handles) != 2 {
		t.Errorf("handles = %v, want 2 entries", resp.Handles)
	}
	// Must be sorted — see HandleNames.
	if resp.Handles[0] != "gmail" || resp.Handles[1] != "outlook" {
		t.Errorf("handles not sorted: %v", resp.Handles)
	}
}

func TestHealthHandler_EmptyHandles(t *testing.T) {
	h := NewHealthHandler(&Config{})
	rr := httptest.NewRecorder()
	h(rr, httptest.NewRequest(http.MethodGet, "/health", nil))

	var resp HealthResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Handles == nil || len(resp.Handles) != 0 {
		t.Errorf("handles = %v, want empty slice", resp.Handles)
	}
}

func TestHealthHandler_MethodNotAllowed(t *testing.T) {
	h := NewHealthHandler(&Config{})
	rr := httptest.NewRecorder()
	h(rr, httptest.NewRequest(http.MethodPost, "/health", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("code = %d, want 405", rr.Code)
	}
}
