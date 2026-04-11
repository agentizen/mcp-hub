package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
