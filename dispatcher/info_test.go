package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newInfoRequest builds a request that Go 1.22 mux.HandleFunc would
// have filled with the PathValue("handle"). We bypass the mux by
// calling SetPathValue directly.
func newInfoRequest(method, handle, body string, headers map[string]string) *http.Request {
	req := httptest.NewRequest(method, "/mcp/"+handle+"/info", strings.NewReader(body))
	req.SetPathValue("handle", handle)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req
}

func TestHandleInfo_UnknownHandle_Returns404(t *testing.T) {
	d := NewDispatcher(&Config{Handles: map[string]HandleConfig{}}, NewPool(nil, newTestLogger()), newTestLogger())
	req := newInfoRequest(http.MethodGet, "nope", "", nil)
	rr := httptest.NewRecorder()
	d.HandleInfo(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404", rr.Code)
	}
}

func TestHandleInfo_UnknownSubprocess_Returns500(t *testing.T) {
	cfg := &Config{
		Handles: map[string]HandleConfig{"h": {Subprocess: "ghost"}},
	}
	d := NewDispatcher(cfg, NewPool(nil, newTestLogger()), newTestLogger())
	req := newInfoRequest(http.MethodGet, "h", "", nil)
	rr := httptest.NewRecorder()
	d.HandleInfo(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("code = %d, want 500 for config-lookup failure", rr.Code)
	}
}

func TestHandleInfo_RemoteHandle_ReturnsToolsAsKindRemote(t *testing.T) {
	var receivedAuth, receivedBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		receivedBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"info","result":{"tools":[{"name":"a","description":"A"},{"name":"b","description":"B"}]}}`))
	}))
	defer srv.Close()

	cfg := &Config{
		Remotes: []RemoteConfig{{Name: "r", URL: srv.URL}},
		Handles: map[string]HandleConfig{"h": {Remote: "r"}},
	}
	d := NewDispatcher(cfg, NewPool(nil, newTestLogger()), newTestLogger())

	req := newInfoRequest(http.MethodGet, "h", "", map[string]string{"Authorization": "Bearer xxx"})
	rr := httptest.NewRecorder()
	d.HandleInfo(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 — body=%s", rr.Code, rr.Body.String())
	}
	if receivedAuth != "Bearer xxx" {
		t.Errorf("upstream Authorization = %q, want Bearer xxx", receivedAuth)
	}
	if !strings.Contains(receivedBody, `"method":"tools/list"`) {
		t.Errorf("upstream body missing tools/list: %s", receivedBody)
	}

	var resp InfoResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Handle != "h" {
		t.Errorf("handle = %q, want h", resp.Handle)
	}
	if resp.Kind != "remote" {
		t.Errorf("kind = %q, want remote", resp.Kind)
	}
	if len(resp.Tools) != 2 {
		t.Errorf("tools = %d, want 2", len(resp.Tools))
	}
}

func TestHandleInfo_SubprocessHandle_FiltersTools(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"info","result":{"tools":[{"name":"drive_list"},{"name":"drive_upload"},{"name":"gmail_send"}]}}`))
	}))
	defer srv.Close()

	port := httptestPort(t, srv)
	subCfg := SubprocessConfig{Name: "sub", Port: port, Command: []string{"sleep", "30"}}
	cfg := &Config{
		Subprocesses: []SubprocessConfig{subCfg},
		Handles: map[string]HandleConfig{
			"drive": {Subprocess: "sub", ToolSet: map[string]bool{"drive_list": true, "drive_upload": true}},
		},
	}
	pool := NewPool(cfg.Subprocesses, newTestLogger())
	pool.mu.Lock()
	pool.subprocesses["sub"] = fakeRunningSubprocess(subCfg)
	pool.mu.Unlock()

	d := NewDispatcher(cfg, pool, newTestLogger())
	req := newInfoRequest(http.MethodGet, "drive", "", map[string]string{"Authorization": "Bearer ya29"})
	rr := httptest.NewRecorder()
	d.HandleInfo(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp InfoResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Kind != "subprocess" {
		t.Errorf("kind = %q, want subprocess", resp.Kind)
	}
	if len(resp.Tools) != 2 {
		t.Fatalf("tools filtered = %d, want 2 — got %s", len(resp.Tools), rr.Body.String())
	}
	names := map[string]bool{}
	for _, raw := range resp.Tools {
		var m struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(raw, &m)
		names[m.Name] = true
	}
	if !names["drive_list"] || !names["drive_upload"] || names["gmail_send"] {
		t.Errorf("filtered names = %v, want {drive_list, drive_upload}", names)
	}
}

func TestHandleInfo_Upstream401_Propagated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid token"}`))
	}))
	defer srv.Close()

	cfg := &Config{
		Remotes: []RemoteConfig{{Name: "r", URL: srv.URL}},
		Handles: map[string]HandleConfig{"h": {Remote: "r"}},
	}
	d := NewDispatcher(cfg, NewPool(nil, newTestLogger()), newTestLogger())

	req := newInfoRequest(http.MethodGet, "h", "", nil)
	rr := httptest.NewRecorder()
	d.HandleInfo(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("code = %d, want 401", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "invalid token") {
		t.Errorf("body missing upstream error: %s", rr.Body.String())
	}
}

func TestHandleInfo_Upstream500_Returns502(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := &Config{
		Remotes: []RemoteConfig{{Name: "r", URL: srv.URL}},
		Handles: map[string]HandleConfig{"h": {Remote: "r"}},
	}
	d := NewDispatcher(cfg, NewPool(nil, newTestLogger()), newTestLogger())

	req := newInfoRequest(http.MethodGet, "h", "", nil)
	rr := httptest.NewRecorder()
	d.HandleInfo(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Errorf("code = %d, want 502", rr.Code)
	}
}

func TestHandleInfo_MalformedJSON_Returns502(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{not-json`))
	}))
	defer srv.Close()

	cfg := &Config{
		Remotes: []RemoteConfig{{Name: "r", URL: srv.URL}},
		Handles: map[string]HandleConfig{"h": {Remote: "r"}},
	}
	d := NewDispatcher(cfg, NewPool(nil, newTestLogger()), newTestLogger())

	req := newInfoRequest(http.MethodGet, "h", "", nil)
	rr := httptest.NewRecorder()
	d.HandleInfo(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Errorf("code = %d, want 502", rr.Code)
	}
}

func TestHandleInfo_JSONRPCError_Returns502(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"info","error":{"code":-32601,"message":"method not found"}}`))
	}))
	defer srv.Close()

	cfg := &Config{
		Remotes: []RemoteConfig{{Name: "r", URL: srv.URL}},
		Handles: map[string]HandleConfig{"h": {Remote: "r"}},
	}
	d := NewDispatcher(cfg, NewPool(nil, newTestLogger()), newTestLogger())

	req := newInfoRequest(http.MethodGet, "h", "", nil)
	rr := httptest.NewRecorder()
	d.HandleInfo(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Errorf("code = %d, want 502 for JSON-RPC error envelope", rr.Code)
	}
}

func TestHandleInfo_EmptyToolsResult_ReturnsEmptyArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"info","result":{"tools":[]}}`))
	}))
	defer srv.Close()

	cfg := &Config{
		Remotes: []RemoteConfig{{Name: "r", URL: srv.URL}},
		Handles: map[string]HandleConfig{"h": {Remote: "r"}},
	}
	d := NewDispatcher(cfg, NewPool(nil, newTestLogger()), newTestLogger())

	req := newInfoRequest(http.MethodGet, "h", "", nil)
	rr := httptest.NewRecorder()
	d.HandleInfo(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rr.Code)
	}
	var resp InfoResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Tools == nil {
		t.Error("tools should be [] not nil")
	}
	if len(resp.Tools) != 0 {
		t.Errorf("tools = %v, want empty", resp.Tools)
	}
}

func TestHandleInfo_POSTAlsoAccepted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"tools":[{"name":"a"}]}}`))
	}))
	defer srv.Close()

	cfg := &Config{
		Remotes: []RemoteConfig{{Name: "r", URL: srv.URL}},
		Handles: map[string]HandleConfig{"h": {Remote: "r"}},
	}
	d := NewDispatcher(cfg, NewPool(nil, newTestLogger()), newTestLogger())

	req := newInfoRequest(http.MethodPost, "h", "", nil)
	rr := httptest.NewRecorder()
	d.HandleInfo(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("code = %d, want 200 for POST", rr.Code)
	}
}

// Integration via the main mux — prove the Go 1.22 pattern matching
// routes /mcp/{handle}/info to HandleInfo and /mcp/{handle} to the
// catch-all dispatcher without ambiguity.
func TestMuxRouting_InfoAndProxyCoexist(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.Header.Get("X-Kind"), "info") {
			_, _ = w.Write([]byte(`{"result":{"tools":[]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"result":"proxy-ok"}`))
	}))
	defer upstream.Close()

	cfg := &Config{
		Remotes: []RemoteConfig{{Name: "r", URL: upstream.URL}},
		Handles: map[string]HandleConfig{"h": {Remote: "r"}},
	}
	d := NewDispatcher(cfg, NewPool(nil, newTestLogger()), newTestLogger())

	mux := http.NewServeMux()
	mux.HandleFunc("/mcp/{handle}/info", d.HandleInfo)
	mux.Handle("/mcp/", d)

	// /info path
	req := httptest.NewRequest(http.MethodGet, "/mcp/h/info", nil)
	req.Header.Set("X-Kind", "info")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("info code = %d, want 200", rr.Code)
	}
	var infoResp InfoResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &infoResp)
	if infoResp.Handle != "h" {
		t.Errorf("info handle = %q, want h", infoResp.Handle)
	}

	// Proxy path
	req2 := httptest.NewRequest(http.MethodPost, "/mcp/h", bytes.NewReader([]byte(`{"method":"ping"}`)))
	rr2 := httptest.NewRecorder()
	mux.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Errorf("proxy code = %d, want 200 — body=%s", rr2.Code, rr2.Body.String())
	}
	if !strings.Contains(rr2.Body.String(), "proxy-ok") {
		t.Errorf("proxy body = %s", rr2.Body.String())
	}
}
