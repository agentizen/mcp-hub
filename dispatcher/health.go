package main

import (
	"encoding/json"
	"net/http"
)

// HealthResponse is the body of GET /health.
type HealthResponse struct {
	Status  string   `json:"status"`
	Handles []string `json:"handles"`
}

// NewHealthHandler returns a handler that reports the configured handles.
func NewHealthHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handles := cfg.HandleNames()
		if handles == nil {
			handles = []string{}
		}
		resp := HealthResponse{Status: "ok", Handles: handles}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}
