package main

import (
	"encoding/json"
	"net/http"
)

// version is overridden at build time via
//
//	go build -ldflags "-X main.version=<tag>"
//
// Defaults to "dev" in untagged builds.
var version = "dev"

// HealthResponse is the body of GET /health.
type HealthResponse struct {
	Status  string   `json:"status"`
	Version string   `json:"version"`
	Handles []string `json:"handles"`
}

// NewHealthHandler returns a handler that reports the configured handles
// and the build version.
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
		resp := HealthResponse{Status: "ok", Version: version, Handles: handles}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}
