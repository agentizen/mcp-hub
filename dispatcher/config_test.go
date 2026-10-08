package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return path
}

func TestLoadConfig_HappyPath(t *testing.T) {
	path := writeTempConfig(t, `
subprocesses:
  - name: m365
    type: node
    port: 9000
    cwd: /opt/mcp-hub/node
    command: ["node", "server.js"]
  - name: gws
    type: python
    port: 9001
    cwd: /opt/mcp-hub/python
    command: ["uv", "run", "main.py"]

remotes:
  - name: clickup
    url: https://mcp.clickup.com/mcp

handles:
  outlook:
    subprocess: m365
    tools: ["outlook_list_messages", "outlook_send"]
  gmail:
    subprocess: gws
  clickup:
    remote: clickup
`)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.Subprocesses) != 2 {
		t.Errorf("want 2 subprocesses, got %d", len(cfg.Subprocesses))
	}
	if len(cfg.Remotes) != 1 {
		t.Errorf("want 1 remote, got %d", len(cfg.Remotes))
	}
	if len(cfg.Handles) != 3 {
		t.Errorf("want 3 handles, got %d", len(cfg.Handles))
	}
	outlook := cfg.Handles["outlook"]
	if outlook.Subprocess != "m365" {
		t.Errorf("outlook.Subprocess = %q, want m365", outlook.Subprocess)
	}
	if !outlook.ToolSet["outlook_list_messages"] {
		t.Errorf("outlook ToolSet missing outlook_list_messages")
	}
	if outlook.ToolSet["nonexistent"] {
		t.Errorf("outlook ToolSet should not contain nonexistent")
	}
	gmail := cfg.Handles["gmail"]
	if gmail.ToolSet != nil {
		t.Errorf("gmail ToolSet should be nil (pass-through), got %v", gmail.ToolSet)
	}
	names := cfg.HandleNames()
	if len(names) != 3 || names[0] != "clickup" {
		t.Errorf("HandleNames not sorted correctly: %v", names)
	}
}

func TestLoadConfig_HandleWithoutBackend(t *testing.T) {
	path := writeTempConfig(t, `
handles:
  orphan: {}
`)
	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "exactly one of subprocess/remote") {
		t.Errorf("want exactly-one error, got %v", err)
	}
}

func TestLoadConfig_HandleWithBothSubprocessAndRemote(t *testing.T) {
	path := writeTempConfig(t, `
subprocesses:
  - name: s1
    port: 9000
    command: [sleep, "30"]
remotes:
  - name: r1
    url: https://example.test/mcp
handles:
  both:
    subprocess: s1
    remote: r1
`)
	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "exactly one of subprocess/remote") {
		t.Errorf("want exactly-one error, got %v", err)
	}
}

func TestLoadConfig_UnknownSubprocessReference(t *testing.T) {
	path := writeTempConfig(t, `
handles:
  x:
    subprocess: ghost
`)
	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "unknown subprocess") {
		t.Errorf("want unknown subprocess error, got %v", err)
	}
}

func TestLoadConfig_UnknownRemoteReference(t *testing.T) {
	path := writeTempConfig(t, `
handles:
  x:
    remote: ghost
`)
	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "unknown remote") {
		t.Errorf("want unknown remote error, got %v", err)
	}
}

func TestLoadConfig_DuplicateSubprocessPort(t *testing.T) {
	path := writeTempConfig(t, `
subprocesses:
  - name: a
    port: 9000
    command: [sleep, "30"]
  - name: b
    port: 9000
    command: [sleep, "30"]
`)
	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "already used") {
		t.Errorf("want duplicate port error, got %v", err)
	}
}

func TestLoadConfig_PortCollidesWithDispatcher(t *testing.T) {
	path := writeTempConfig(t, `
subprocesses:
  - name: a
    port: 8090
    command: [sleep, "30"]
`)
	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "collides with dispatcher") {
		t.Errorf("want dispatcher-port collision error, got %v", err)
	}
}

func TestLoadConfig_EmptyCommand(t *testing.T) {
	path := writeTempConfig(t, `
subprocesses:
  - name: a
    port: 9000
    command: []
`)
	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "command must be non-empty") {
		t.Errorf("want empty-command error, got %v", err)
	}
}

func TestLoadConfig_MissingFile(t *testing.T) {
	_, err := LoadConfig("/nonexistent/config.yaml")
	if err == nil {
		t.Fatalf("want error for missing file, got nil")
	}
}

func TestLoadConfig_UnknownField(t *testing.T) {
	path := writeTempConfig(t, `
mystery: true
`)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatalf("want strict-decoding error for unknown field, got nil")
	}
}

func TestLoadConfig_EmptyPlaceholder(t *testing.T) {
	path := writeTempConfig(t, `
subprocesses: []
remotes: []
handles: {}
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if names := cfg.HandleNames(); len(names) != 0 {
		t.Errorf("want empty handle list, got %v", names)
	}
}

func TestLoadConfig_TimeoutSeconds_Invalid(t *testing.T) {
	cases := []struct {
		label   string
		content string
		needle  string
	}{
		{"negative subprocess", `
subprocesses:
  - name: a
    port: 9000
    command: [sleep, "30"]
    timeout_seconds: -1
`, "timeout_seconds"},
		{"over-capped subprocess", `
subprocesses:
  - name: a
    port: 9000
    command: [sleep, "30"]
    timeout_seconds: 301
`, "exceeds"},
		{"negative remote", `
remotes:
  - name: r
    url: https://example.test/mcp
    timeout_seconds: -5
`, "timeout_seconds"},
		{"over-capped remote", `
remotes:
  - name: r
    url: https://example.test/mcp
    timeout_seconds: 10000
`, "exceeds"},
		{"negative handle", `
remotes:
  - name: r
    url: https://example.test/mcp
handles:
  h:
    remote: r
    timeout_seconds: -2
`, "timeout_seconds"},
		{"over-capped handle", `
remotes:
  - name: r
    url: https://example.test/mcp
handles:
  h:
    remote: r
    timeout_seconds: 999999
`, "exceeds"},
		{"negative max bytes", `
remotes:
  - name: r
    url: https://example.test/mcp
handles:
  h:
    remote: r
    max_response_bytes: -1
`, "max_response_bytes"},
	}
	for _, c := range cases {
		path := writeTempConfig(t, c.content)
		_, err := LoadConfig(path)
		if err == nil {
			t.Errorf("%s: want validation error, got nil", c.label)
			continue
		}
		if !strings.Contains(err.Error(), c.needle) {
			t.Errorf("%s: error message %q missing %q", c.label, err.Error(), c.needle)
		}
	}
}

func TestLoadConfig_TimeoutSeconds_Valid(t *testing.T) {
	path := writeTempConfig(t, `
remotes:
  - name: r
    url: https://example.test/mcp
    timeout_seconds: 30
handles:
  h:
    remote: r
    timeout_seconds: 300
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Handles["h"].ResolvedTimeout != 300*time.Second {
		t.Errorf("ResolvedTimeout = %v, want 300s", cfg.Handles["h"].ResolvedTimeout)
	}
}

func TestLoadConfig_TimeoutResolution_FallsBackToRemote(t *testing.T) {
	path := writeTempConfig(t, `
remotes:
  - name: r
    url: https://example.test/mcp
    timeout_seconds: 45
  - name: r2
    url: https://example.test/mcp
handles:
  inherits:
    remote: r
  defaults:
    remote: r2
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := cfg.Handles["inherits"].ResolvedTimeout; got != 45*time.Second {
		t.Errorf("inherits ResolvedTimeout = %v, want 45s", got)
	}
	if got := cfg.Handles["defaults"].ResolvedTimeout; got != RequestForwardTimeout {
		t.Errorf("defaults ResolvedTimeout = %v, want global %v", got, RequestForwardTimeout)
	}
}

func TestLoadConfig_TimeoutResolution_HandleOverridesBackend(t *testing.T) {
	path := writeTempConfig(t, `
remotes:
  - name: r
    url: https://example.test/mcp
    timeout_seconds: 45
handles:
  h:
    remote: r
    timeout_seconds: 10
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := cfg.Handles["h"].ResolvedTimeout; got != 10*time.Second {
		t.Errorf("ResolvedTimeout = %v, want 10s (handle wins)", got)
	}
}

func TestLoadConfig_TimeoutResolution_SubprocessBackend(t *testing.T) {
	path := writeTempConfig(t, `
subprocesses:
  - name: sp
    port: 9000
    command: [sleep, "30"]
    timeout_seconds: 60
handles:
  h:
    subprocess: sp
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := cfg.Handles["h"].ResolvedTimeout; got != 60*time.Second {
		t.Errorf("ResolvedTimeout = %v, want 60s from subprocess", got)
	}
}

func TestLoadConfig_MaxResponseResolution(t *testing.T) {
	path := writeTempConfig(t, `
remotes:
  - name: r
    url: https://example.test/mcp
    max_response_bytes: 2048
  - name: r2
    url: https://example.test/mcp
handles:
  inherits:
    remote: r
  overrides:
    remote: r
    max_response_bytes: 4096
  defaults:
    remote: r2
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := cfg.Handles["inherits"].ResolvedMaxBytes; got != 2048 {
		t.Errorf("inherits ResolvedMaxBytes = %d, want 2048", got)
	}
	if got := cfg.Handles["overrides"].ResolvedMaxBytes; got != 4096 {
		t.Errorf("overrides ResolvedMaxBytes = %d, want 4096", got)
	}
	if got := cfg.Handles["defaults"].ResolvedMaxBytes; got != MaxResponseBodyBytes {
		t.Errorf("defaults ResolvedMaxBytes = %d, want global %d", got, MaxResponseBodyBytes)
	}
}
