package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// InfoResponse is the body returned by GET|POST /mcp/{handle}/info.
// It is the self-describing discovery contract consumers use to probe
// a handle without speaking the underlying MCP protocol themselves.
type InfoResponse struct {
	Handle string            `json:"handle"`
	Kind   string            `json:"kind"` // "subprocess" | "remote"
	Tools  []json.RawMessage `json:"tools"`
}

// HandleInfo synthesizes a JSON-RPC tools/list request against the
// handle's backend, applies the handle's allow-list, and wraps the
// filtered tools in an InfoResponse envelope.
//
// The caller's headers (Authorization, X-*) are forwarded verbatim to
// the upstream backend so credential handling stays with the consumer.
// Upstream 4xx responses (e.g. 401 for invalid Bearer) are propagated
// as-is so the consumer can surface credential errors accurately.
func (d *Dispatcher) HandleInfo(w http.ResponseWriter, r *http.Request) {
	handle := r.PathValue("handle")
	if handle == "" {
		http.NotFound(w, r)
		return
	}
	hcfg, ok := d.cfg.Handles[handle]
	if !ok {
		http.NotFound(w, r)
		return
	}

	target, err := d.resolveTarget(r.Context(), &hcfg)
	if err != nil {
		d.logger.Warn("info resolve target", "handle", handle, "err", err)
		status := http.StatusBadGateway
		if errors.Is(err, errConfigLookup) {
			status = http.StatusInternalServerError
		}
		http.Error(w, http.StatusText(status), status)
		return
	}

	// Synthetic JSON-RPC tools/list request. id="info" so downstream
	// logs can distinguish discovery probes from real calls.
	reqBody := []byte(`{"jsonrpc":"2.0","id":"info","method":"tools/list"}`)

	outReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, bytes.NewReader(reqBody))
	if err != nil {
		d.logger.Warn("info build request", "handle", handle, "err", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	copyRequestHeaders(outReq.Header, r.Header)
	outReq.Header.Set("Content-Type", "application/json")
	outReq.Header.Set("Accept", "application/json")

	resp, err := d.client.Do(outReq) // #nosec G107,G704 — target URL is resolved from static config (remote by name or 127.0.0.1:<subprocess-port>); consumer input never influences it
	if err != nil {
		d.logger.Warn("info upstream error", "handle", handle, "err", err)
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	// Propagate upstream 4xx verbatim (most importantly 401, so the
	// consumer can distinguish "credentials invalid" from 502).
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		copyHeaders(w.Header(), resp.Header, "Content-Length")
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
		return
	}
	if resp.StatusCode >= 500 {
		d.logger.Warn("info upstream 5xx", "handle", handle, "status", resp.StatusCode)
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}

	// Decode the JSON-RPC envelope.
	var upstream struct {
		Result struct {
			Tools []json.RawMessage `json:"tools"`
		} `json:"result"`
		Error json.RawMessage `json:"error,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&upstream); err != nil {
		d.logger.Warn("info decode upstream", "handle", handle, "err", err)
		http.Error(w, "bad upstream response", http.StatusBadGateway)
		return
	}
	if len(upstream.Error) > 0 {
		d.logger.Warn("info upstream JSON-RPC error", "handle", handle, "err", string(upstream.Error))
		http.Error(w, "upstream JSON-RPC error", http.StatusBadGateway)
		return
	}

	tools := upstream.Result.Tools
	if len(hcfg.ToolSet) > 0 {
		tools = filterToolsByAllowList(tools, hcfg.ToolSet)
	}

	kind := "subprocess"
	if hcfg.Remote != "" {
		kind = "remote"
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(InfoResponse{
		Handle: handle,
		Kind:   kind,
		Tools:  tools,
	})
}

// filterToolsByAllowList keeps only the tools whose name is in allowed.
// Malformed tool entries are dropped silently — never leaked.
func filterToolsByAllowList(tools []json.RawMessage, allowed map[string]bool) []json.RawMessage {
	kept := make([]json.RawMessage, 0, len(tools))
	for _, raw := range tools {
		var meta struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &meta); err != nil {
			continue
		}
		if allowed[meta.Name] {
			kept = append(kept, raw)
		}
	}
	return kept
}
