package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
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

// clientFor returns a client honoring the handle's resolved request
// timeout. The base client is a shared template; it is shallow-copied so
// each handle's timeout applies while reusing the same Transport (and its
// connection pool).
func (d *Dispatcher) clientFor(h *HandleConfig) *http.Client {
	if h.ResolvedTimeout <= 0 || h.ResolvedTimeout == d.client.Timeout {
		return d.client
	}
	c := &http.Client{}
	*c = *d.client
	c.Timeout = h.ResolvedTimeout
	return c
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
		d.logger.Warn("read body", "handle", handle, "err", err)
		http.Error(w, "bad request body", http.StatusBadRequest)
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
		status := http.StatusBadGateway
		if errors.Is(err, errConfigLookup) {
			// Handle references an entry that doesn't exist in the
			// config — operator error, not upstream failure.
			status = http.StatusInternalServerError
		}
		http.Error(w, http.StatusText(status), status)
		return
	}

	outReq, err := http.NewRequestWithContext(r.Context(), r.Method, target, bytes.NewReader(body))
	if err != nil {
		d.logger.Warn("build upstream request", "handle", handle, "err", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	copyRequestHeaders(outReq.Header, r.Header)

	resp, err := d.clientFor(&hcfg).Do(outReq) // #nosec G107,G704 — outbound URL is resolved from static config (named remote or 127.0.0.1:<subprocess-port>); consumer input never influences it
	if err != nil {
		d.logger.Warn("upstream error", "handle", handle, "err", err)
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	maxBytes := hcfg.ResolvedMaxBytes
	if maxBytes <= 0 {
		maxBytes = MaxResponseBodyBytes
	}

	// If this is a tools/list response AND the handle has an allow-list AND
	// the upstream answers JSON or SSE, buffer and filter. Fail-closed on
	// any filtering error so disallowed tools never leak to the consumer.
	if d.shouldFilterResponse(body, &hcfg, resp) {
		respBody, rerr := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)+1))
		if rerr != nil {
			d.logger.Warn("upstream read", "handle", handle, "err", rerr)
			http.Error(w, "upstream error", http.StatusBadGateway)
			return
		}
		if len(respBody) > maxBytes {
			d.logger.Warn("upstream response too large", "handle", handle, "bytes", len(respBody))
			http.Error(w, "upstream response too large", http.StatusBadGateway)
			return
		}
		out, filtered, ferr := d.filterToolsListBody(respBody, resp, &hcfg)
		if ferr != nil {
			d.logger.Warn("tools/list filter failed", "handle", handle, "err", ferr)
			http.Error(w, "upstream error", http.StatusBadGateway)
			return
		}
		if !filtered {
			out = respBody
		}
		copyHeaders(w.Header(), resp.Header, "Content-Length")
		w.Header().Set("Content-Length", strconv.Itoa(len(out)))
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(out)
		return
	}

	// Default pass-through: buffer within the cap so an oversized upstream
	// response fails closed with 502 before any bytes are forwarded.
	respBody, rerr := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)+1))
	if rerr != nil {
		d.logger.Warn("upstream read", "handle", handle, "err", rerr)
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	if len(respBody) > maxBytes {
		d.logger.Warn("upstream response too large", "handle", handle, "bytes", len(respBody))
		http.Error(w, "upstream response too large", http.StatusBadGateway)
		return
	}
	copyHeaders(w.Header(), resp.Header, "Content-Length")
	w.Header().Set("Content-Length", strconv.Itoa(len(respBody)))
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(respBody)
}

// filterToolsListBody applies the handle's allow-list to a buffered
// tools/list response, handling both application/json and
// text/event-stream upstreams. It returns the body to send and whether
// filtering actually happened; when the upstream answered SSE, the
// filtered result is re-emitted as a single `data:` event. A JSON-RPC
// error envelope passes through verbatim (filtered=false).
func (d *Dispatcher) filterToolsListBody(respBody []byte, resp *http.Response, h *HandleConfig) ([]byte, bool, error) {
	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mt != "text/event-stream" {
		return FilterToolsListResponse(respBody, h.ToolSet)
	}
	jsonBody, derr := DecodeSSEPayload(bytes.NewReader(respBody))
	if derr != nil {
		return nil, false, fmt.Errorf("decode SSE payload: %w", derr)
	}
	newBody, filtered, ferr := FilterToolsListResponse(jsonBody, h.ToolSet)
	if ferr != nil {
		return nil, false, ferr
	}
	if !filtered {
		return respBody, false, nil
	}
	out, eerr := EncodeSSEPayload(newBody)
	if eerr != nil {
		return nil, false, eerr
	}
	return out, true, nil
}

// shouldFilterResponse returns true when the request was tools/list on
// an allow-listed handle AND the upstream response is JSON or SSE.
func (d *Dispatcher) shouldFilterResponse(reqBody []byte, h *HandleConfig, resp *http.Response) bool {
	if len(h.ToolSet) == 0 {
		return false
	}
	method, _ := ExtractMethod(reqBody)
	if method != "tools/list" {
		return false
	}
	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	return mt == "application/json" || mt == "text/event-stream"
}

// errConfigLookup wraps errors that indicate the handle references a
// backend entry that is not present in the loaded config. Translates
// to HTTP 500 (operator error) rather than 502 (upstream failure).
var errConfigLookup = errors.New("config lookup failure")

// resolveTarget returns the absolute URL to forward the request to.
// For subprocess handles it lazily spawns the backend via the pool and
// honors the optional cfg.Path override; defaults to /mcp.
func (d *Dispatcher) resolveTarget(ctx context.Context, h *HandleConfig) (string, error) {
	if h.Subprocess != "" {
		sp, err := d.pool.GetOrSpawn(ctx, h.Subprocess)
		if err != nil {
			if errors.Is(err, ErrUnknownSubprocess) {
				return "", fmt.Errorf("%w: subprocess %q", errConfigLookup, h.Subprocess)
			}
			return "", err
		}
		return fmt.Sprintf("http://127.0.0.1:%d%s", sp.Port(), sp.Path()), nil
	}
	for _, r := range d.cfg.Remotes {
		if r.Name == h.Remote {
			return r.URL, nil
		}
	}
	return "", fmt.Errorf("%w: remote %q", errConfigLookup, h.Remote)
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
