package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
)

// Dispatcher implements the public HTTP surface: POST /mcp/<handle>.
// It delegates subprocess lifecycle to Pool and forwards remote requests
// via http.DefaultTransport (which honors HTTP_PROXY for smokescreen).
type Dispatcher struct {
	cfg    *Config
	pool   *Pool
	client *http.Client
	logger *slog.Logger
}

// NewDispatcher wires a Dispatcher for the given config and pool.
func NewDispatcher(cfg *Config, pool *Pool, logger *slog.Logger) *Dispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	return &Dispatcher{
		cfg:  cfg,
		pool: pool,
		client: &http.Client{
			Timeout:   RequestForwardTimeout,
			Transport: http.DefaultTransport,
		},
		logger: logger,
	}
}

// ServeHTTP routes POST /mcp/<handle> → upstream backend.
func (d *Dispatcher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	handle, ok := extractHandle(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	hcfg, known := d.cfg.Handles[handle]
	if !known {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Read and size-cap the body.
	body, err := io.ReadAll(io.LimitReader(r.Body, int64(MaxRequestBodyBytes)+1))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(body) > MaxRequestBodyBytes {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}

	// Enforce tools/call allow-list (ignore parse errors — pass through).
	if len(hcfg.ToolSet) > 0 {
		if name, isCall, _ := ExtractToolCallName(body); isCall && !CheckToolCallAllowed(name, hcfg.ToolSet) {
			http.Error(w, "tool not allowed", http.StatusForbidden)
			return
		}
	}

	target, err := d.resolveTarget(r.Context(), &hcfg)
	if err != nil {
		d.logger.Warn("resolve target", "handle", handle, "err", err)
		http.Error(w, "resolve target: "+err.Error(), http.StatusBadGateway)
		return
	}

	outReq, err := http.NewRequestWithContext(r.Context(), r.Method, target, bytes.NewReader(body))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	copyRequestHeaders(outReq.Header, r.Header)

	resp, err := d.client.Do(outReq) // #nosec G107,G704 — outbound URL is resolved from static config (named remote or 127.0.0.1:<subprocess-port>); consumer input never influences it
	if err != nil {
		d.logger.Warn("upstream error", "handle", handle, "err", err)
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	// If this is a tools/list response AND the handle has an allow-list AND
	// the upstream says it's JSON, buffer and filter.
	if d.shouldFilterResponse(body, &hcfg, resp) {
		respBody, rerr := io.ReadAll(resp.Body)
		if rerr != nil {
			http.Error(w, "upstream read: "+rerr.Error(), http.StatusBadGateway)
			return
		}
		out := respBody
		if newBody, filtered, ferr := FilterToolsListResponse(respBody, hcfg.ToolSet); ferr == nil && filtered {
			out = newBody
		}
		copyHeaders(w.Header(), resp.Header, "Content-Length")
		w.Header().Set("Content-Length", strconv.Itoa(len(out)))
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(out)
		return
	}

	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// shouldFilterResponse returns true when the request was tools/list on
// an allow-listed handle AND the upstream response is JSON.
func (d *Dispatcher) shouldFilterResponse(reqBody []byte, h *HandleConfig, resp *http.Response) bool {
	if len(h.ToolSet) == 0 {
		return false
	}
	method, _ := ExtractMethod(reqBody)
	if method != "tools/list" {
		return false
	}
	return strings.Contains(resp.Header.Get("Content-Type"), "application/json")
}

// resolveTarget returns the absolute URL to forward the request to.
func (d *Dispatcher) resolveTarget(ctx context.Context, h *HandleConfig) (string, error) {
	if h.Subprocess != "" {
		sp, err := d.pool.GetOrSpawn(ctx, h.Subprocess)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("http://127.0.0.1:%d/mcp", sp.Port()), nil
	}
	for _, r := range d.cfg.Remotes {
		if r.Name == h.Remote {
			return r.URL, nil
		}
	}
	return "", fmt.Errorf("remote %q not found", h.Remote)
}

// extractHandle parses a /mcp/<handle> path. Returns "", false when the
// prefix is wrong OR when extra segments are present; paths like
// /mcp/<handle>/info are routed by the mux to HandleInfo directly.
func extractHandle(path string) (string, bool) {
	const prefix = "/mcp/"
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	rest := strings.TrimPrefix(path, prefix)
	if rest == "" {
		return "", false
	}
	if strings.ContainsRune(rest, '/') {
		return "", false
	}
	return rest, true
}

// copyRequestHeaders copies request headers from src to dst, dropping
// hop-by-hop and Host headers.
func copyRequestHeaders(dst, src http.Header) {
	for k, vs := range src {
		if isHopByHop(k) || strings.EqualFold(k, "Host") {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

// copyHeaders copies response headers, excluding the listed names.
func copyHeaders(dst, src http.Header, exclude ...string) {
	excl := make(map[string]bool, len(exclude))
	for _, e := range exclude {
		excl[strings.ToLower(e)] = true
	}
	for k, vs := range src {
		if excl[strings.ToLower(k)] || isHopByHop(k) {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

// isHopByHop returns true for connection-management headers per RFC 7230.
func isHopByHop(h string) bool {
	switch strings.ToLower(h) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
		"te", "trailers", "transfer-encoding", "upgrade":
		return true
	}
	return false
}
