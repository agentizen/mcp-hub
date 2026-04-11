package main

import (
	"encoding/json"
	"testing"
)

func TestExtractToolCallName_ValidRequest(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"gmail_list","arguments":{"q":"is:unread"}}}`)
	name, isCall, err := ExtractToolCallName(body)
	if err != nil {
		t.Fatalf("ExtractToolCallName: %v", err)
	}
	if !isCall {
		t.Error("want isCall=true")
	}
	if name != "gmail_list" {
		t.Errorf("name = %q, want gmail_list", name)
	}
}

func TestExtractToolCallName_NotAToolCall(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	name, isCall, err := ExtractToolCallName(body)
	if err != nil {
		t.Fatalf("ExtractToolCallName: %v", err)
	}
	if isCall {
		t.Error("want isCall=false for tools/list")
	}
	if name != "" {
		t.Errorf("name = %q, want empty", name)
	}
}

func TestExtractToolCallName_InvalidJSON(t *testing.T) {
	_, _, err := ExtractToolCallName([]byte(`not json`))
	if err == nil {
		t.Error("want parse error")
	}
}

func TestExtractMethod(t *testing.T) {
	m, err := ExtractMethod([]byte(`{"method":"tools/list"}`))
	if err != nil || m != "tools/list" {
		t.Errorf("method=%q err=%v", m, err)
	}
}

func TestCheckToolCallAllowed_EmptySetPassesAll(t *testing.T) {
	if !CheckToolCallAllowed("anything", nil) {
		t.Error("empty allow-set should pass all")
	}
	if !CheckToolCallAllowed("anything", map[string]bool{}) {
		t.Error("empty allow-set map should pass all")
	}
}

func TestCheckToolCallAllowed_WithSet(t *testing.T) {
	allow := map[string]bool{"a": true, "b": true}
	if !CheckToolCallAllowed("a", allow) {
		t.Error("'a' should be allowed")
	}
	if CheckToolCallAllowed("c", allow) {
		t.Error("'c' should NOT be allowed")
	}
}

func TestFilterToolsListResponse_FiltersOutDisallowed(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"a","description":"A"},{"name":"b","description":"B"},{"name":"c","description":"C"}]}}`)
	allow := map[string]bool{"a": true, "c": true}
	out, filtered, err := FilterToolsListResponse(body, allow)
	if err != nil {
		t.Fatalf("FilterToolsListResponse: %v", err)
	}
	if !filtered {
		t.Fatal("expected filtered=true")
	}

	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	result := got["result"].(map[string]any)
	tools := result["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("len tools = %d, want 2", len(tools))
	}
	names := map[string]bool{}
	for _, tt := range tools {
		names[tt.(map[string]any)["name"].(string)] = true
	}
	if !names["a"] || !names["c"] || names["b"] {
		t.Errorf("filtered tools = %v, want {a,c}", names)
	}
	if got["id"] == nil {
		t.Error("id field should be preserved")
	}
}

func TestFilterToolsListResponse_EmptyAllowSet_PassesThrough(t *testing.T) {
	body := []byte(`{"result":{"tools":[{"name":"a"}]}}`)
	_, filtered, err := FilterToolsListResponse(body, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if filtered {
		t.Error("empty allow-set should NOT filter")
	}
}

func TestFilterToolsListResponse_NonJSON(t *testing.T) {
	_, filtered, err := FilterToolsListResponse([]byte(`not json`), map[string]bool{"a": true})
	if err == nil {
		t.Error("want error for non-JSON body")
	}
	if filtered {
		t.Error("filtered should be false on error")
	}
}

// TestFilterToolsListResponse_ResultNotObject_FailsClosed asserts the
// caller receives an error when the upstream returns a result that
// isn't a JSON object. Previously the filter would silently pass
// through, which could bypass the allow-list.
func TestFilterToolsListResponse_ResultNotObject_FailsClosed(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","result":"unexpected-string"}`)
	_, filtered, err := FilterToolsListResponse(body, map[string]bool{"a": true})
	if err == nil {
		t.Error("want error when result is not an object")
	}
	if filtered {
		t.Error("filtered should be false when the filter errors")
	}
}

func TestFilterToolsListResponse_ResultMissingToolsKey_FailsClosed(t *testing.T) {
	body := []byte(`{"result":{"unexpected":42}}`)
	_, _, err := FilterToolsListResponse(body, map[string]bool{"a": true})
	if err == nil {
		t.Error("want error when result has no tools key")
	}
}

func TestFilterToolsListResponse_ToolsNotArray_FailsClosed(t *testing.T) {
	body := []byte(`{"result":{"tools":"not-an-array"}}`)
	_, _, err := FilterToolsListResponse(body, map[string]bool{"a": true})
	if err == nil {
		t.Error("want error when tools is not an array")
	}
}

func TestFilterToolsListResponse_ErrorEnvelope_PassesThrough(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-1,"message":"boom"}}`)
	_, filtered, err := FilterToolsListResponse(body, map[string]bool{"a": true})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if filtered {
		t.Error("error envelope should NOT be filtered")
	}
}
