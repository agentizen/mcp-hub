package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeRunningSubprocess constructs a Subprocess that is already in the
// Running state without spawning any real process. Used to wire a Pool
// to an httptest.Server port for proxy tests.
func fakeRunningSubprocess(cfg SubprocessConfig) *Subprocess {
	sp := NewSubprocess(cfg, newTestLogger())
	sp.state = StateRunning
	sp.lastUsed = time.Now()
	return sp
}

func httptestPort(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse httptest URL: %v", err)
	}
	p, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("atoi port: %v", err)
	}
	return p
}

func TestProxy_UnknownHandle_Returns404(t *testing.T) {
	cfg := &Config{Handles: map[string]HandleConfig{}}
	d := NewDispatcher(cfg, NewPool(nil, newTestLogger()), newTestLogger())

	req := httptest.NewRequest(http.MethodPost, "/mcp/nope", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()
	d.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404", rr.Code)
	}
}

func TestProxy_SubprocessHandle_ForwardsToInternalPort(t *testing.T) {
	var received struct {
		method string
		body   []byte
		auth   string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.method = r.Method
		received.auth = r.Header.Get("Authorization")
		received.body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"ok"}`))
	}))
	defer srv.Close()

	port := httptestPort(t, srv)
	subCfg := SubprocessConfig{Name: "sub", Port: port, Command: []string{"sleep", "30"}}
	cfg := &Config{
		Subprocesses: []SubprocessConfig{subCfg},
		Handles: map[string]HandleConfig{
			"h": {Subprocess: "sub"},
		},
	}
	pool := NewPool(cfg.Subprocesses, newTestLogger())
	pool.mu.Lock()
	pool.subprocesses["sub"] = fakeRunningSubprocess(subCfg)
	pool.mu.Unlock()

	d := NewDispatcher(cfg, pool, newTestLogger())

	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"t"}}`)
	req := httptest.NewRequest(http.MethodPost, "/mcp/h", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer token-42")
	rr := httptest.NewRecorder()
	d.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("code = %d, want 200 — body=%s", rr.Code, rr.Body.String())
	}
	if received.method != http.MethodPost {
		t.Errorf("upstream method = %q, want POST", received.method)
	}
	if received.auth != "Bearer token-42" {
		t.Errorf("Authorization = %q, want passthrough", received.auth)
	}
	if !bytes.Equal(received.body, body) {
		t.Errorf("upstream body mismatch: got %s, want %s", received.body, body)
	}
	if !strings.Contains(rr.Body.String(), `"result":"ok"`) {
		t.Errorf("response body = %s, want upstream pass-through", rr.Body.String())
	}
}

func TestProxy_RemoteHandle_ForwardsToRemoteURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Origin", "remote")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":"remote-ok"}`))
	}))
	defer srv.Close()

	cfg := &Config{
		Remotes: []RemoteConfig{{Name: "r", URL: srv.URL}},
		Handles: map[string]HandleConfig{"h": {Remote: "r"}},
	}
	d := NewDispatcher(cfg, NewPool(nil, newTestLogger()), newTestLogger())

	req := httptest.NewRequest(http.MethodPost, "/mcp/h", strings.NewReader(`{"method":"ping"}`))
	rr := httptest.NewRecorder()
	d.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("code = %d, want 200", rr.Code)
	}
	if rr.Header().Get("X-Origin") != "remote" {
		t.Errorf("X-Origin header not propagated")
	}
	if !strings.Contains(rr.Body.String(), "remote-ok") {
		t.Errorf("body = %s", rr.Body.String())
	}
}

func TestProxy_MethodNotAllowed(t *testing.T) {
	cfg := &Config{Handles: map[string]HandleConfig{"h": {Remote: "r"}}}
	d := NewDispatcher(cfg, NewPool(nil, newTestLogger()), newTestLogger())
	req := httptest.NewRequest(http.MethodGet, "/mcp/h", nil)
	rr := httptest.NewRecorder()
	d.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("code = %d, want 405", rr.Code)
	}
}

func TestProxy_BodyTooLarge(t *testing.T) {
	cfg := &Config{
		Remotes: []RemoteConfig{{Name: "r", URL: "http://127.0.0.1:1"}},
		Handles: map[string]HandleConfig{"h": {Remote: "r"}},
	}
	d := NewDispatcher(cfg, NewPool(nil, newTestLogger()), newTestLogger())

	big := bytes.Repeat([]byte("A"), MaxRequestBodyBytes+100)
	req := httptest.NewRequest(http.MethodPost, "/mcp/h", bytes.NewReader(big))
	rr := httptest.NewRecorder()
	d.ServeHTTP(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("code = %d, want 413", rr.Code)
	}
}

func TestProxy_UpstreamError_Returns502(t *testing.T) {
	cfg := &Config{
		Remotes: []RemoteConfig{{Name: "r", URL: "http://127.0.0.1:1"}},
		Handles: map[string]HandleConfig{"h": {Remote: "r"}},
	}
	d := NewDispatcher(cfg, NewPool(nil, newTestLogger()), newTestLogger())

	req := httptest.NewRequest(http.MethodPost, "/mcp/h", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()
	d.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Errorf("code = %d, want 502", rr.Code)
	}
}

func TestProxy_ToolCallAllowList_Rejects(t *testing.T) {
	cfg := &Config{
		Remotes: []RemoteConfig{{Name: "r", URL: "http://unused.test"}},
		Handles: map[string]HandleConfig{
			"h": {Remote: "r", ToolSet: map[string]bool{"allowed": true}},
		},
	}
	d := NewDispatcher(cfg, NewPool(nil, newTestLogger()), newTestLogger())

	body := []byte(`{"method":"tools/call","params":{"name":"forbidden"}}`)
	req := httptest.NewRequest(http.MethodPost, "/mcp/h", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	d.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("code = %d, want 403", rr.Code)
	}
}

func TestProxy_ToolCallAllowList_AllowsListed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":"ok"}`))
	}))
	defer srv.Close()

	cfg := &Config{
		Remotes: []RemoteConfig{{Name: "r", URL: srv.URL}},
		Handles: map[string]HandleConfig{
			"h": {Remote: "r", ToolSet: map[string]bool{"allowed": true}},
		},
	}
	d := NewDispatcher(cfg, NewPool(nil, newTestLogger()), newTestLogger())

	body := []byte(`{"method":"tools/call","params":{"name":"allowed"}}`)
	req := httptest.NewRequest(http.MethodPost, "/mcp/h", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	d.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("code = %d, want 200 — body=%s", rr.Code, rr.Body.String())
	}
}

func TestProxy_ToolsListResponse_IsFiltered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"a"},{"name":"b"},{"name":"c"}]}}`))
	}))
	defer srv.Close()

	cfg := &Config{
		Remotes: []RemoteConfig{{Name: "r", URL: srv.URL}},
		Handles: map[string]HandleConfig{
			"h": {Remote: "r", ToolSet: map[string]bool{"a": true}},
		},
	}
	d := NewDispatcher(cfg, NewPool(nil, newTestLogger()), newTestLogger())

	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	req := httptest.NewRequest(http.MethodPost, "/mcp/h", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	d.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rr.Code)
	}
	rb := rr.Body.String()
	if !strings.Contains(rb, `"a"`) {
		t.Errorf("response missing tool 'a': %s", rb)
	}
	if strings.Contains(rb, `"b"`) || strings.Contains(rb, `"c"`) {
		t.Errorf("response still contains disallowed tools: %s", rb)
	}
}

func TestExtractHandle(t *testing.T) {
	cases := []struct {
		in     string
		handle string
		ok     bool
	}{
		{"/mcp/gmail", "gmail", true},
		{"/mcp/", "", false},
		{"/mcp/gmail/info", "", false},
		{"/health", "", false},
		{"/mcp", "", false},
	}
	for _, c := range cases {
		got, ok := extractHandle(c.in)
		if got != c.handle || ok != c.ok {
			t.Errorf("extractHandle(%q) = (%q,%v), want (%q,%v)", c.in, got, ok, c.handle, c.ok)
		}
	}
}

func TestProxy_HostHeaderNotForwarded(t *testing.T) {
	var receivedHost string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// http.Request.Host is set from Host header OR URL Host.
		receivedHost = r.Host
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	cfg := &Config{
		Remotes: []RemoteConfig{{Name: "r", URL: srv.URL}},
		Handles: map[string]HandleConfig{"h": {Remote: "r"}},
	}
	d := NewDispatcher(cfg, NewPool(nil, newTestLogger()), newTestLogger())

	req := httptest.NewRequest(http.MethodPost, "/mcp/h", strings.NewReader(`{}`))
	req.Header.Set("Host", "spoofed.example.com")
	rr := httptest.NewRecorder()
	d.ServeHTTP(rr, req)

	if strings.Contains(receivedHost, "spoofed") {
		t.Errorf("spoofed Host leaked to upstream: %q", receivedHost)
	}
}
