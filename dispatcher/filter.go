package main

import (
	"encoding/json"
	"errors"
	"fmt"
)

// toolCallInspection is the minimal subset of a JSON-RPC request we need
// to read. We ONLY inspect method + params.name — everything else is
// opaque and passed through untouched.
type toolCallInspection struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type toolCallParams struct {
	Name string `json:"name"`
}

// ExtractMethod parses the JSON-RPC request body and returns the method
// field. Returns method == "" if the body is not a JSON object.
func ExtractMethod(body []byte) (string, error) {
	var msg toolCallInspection
	if err := json.Unmarshal(body, &msg); err != nil {
		return "", err
	}
	return msg.Method, nil
}

// ExtractToolCallName parses a JSON-RPC request body and returns the
// tool name if the request is a "tools/call". isCall reports whether the
// body is a tools/call request at all.
func ExtractToolCallName(body []byte) (name string, isCall bool, err error) {
	var msg toolCallInspection
	if err := json.Unmarshal(body, &msg); err != nil {
		return "", false, err
	}
	if msg.Method != "tools/call" {
		return "", false, nil
	}
	var p toolCallParams
	if len(msg.Params) > 0 {
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return "", true, fmt.Errorf("tools/call params: %w", err)
		}
	}
	return p.Name, true, nil
}

// CheckToolCallAllowed returns true when the allow-set is empty (pass
// through) OR when name is explicitly listed in the allow-set.
func CheckToolCallAllowed(name string, allowed map[string]bool) bool {
	if len(allowed) == 0 {
		return true
	}
	return allowed[name]
}

// FilterToolsListResponse filters a `tools/list` JSON-RPC response body,
// removing any tool whose name is not in the allow-set. Semantics:
//
//   - Empty allow-set  → (nil, false, nil) : caller passes the original
//     body through unchanged.
//   - JSON-RPC error envelope (no "result" key) → (nil, false, nil) :
//     caller passes it through. Errors don't leak disallowed tools.
//   - Valid tools list → (newBody, true, nil) : caller writes newBody.
//   - Any other malformed / unexpected shape → (nil, false, error) :
//     caller MUST fail-closed (502) to prevent allow-list bypass.
func FilterToolsListResponse(body []byte, allowed map[string]bool) ([]byte, bool, error) {
	if len(allowed) == 0 {
		return nil, false, nil
	}
	// Flexible envelope so we preserve unknown JSON-RPC fields.
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, false, fmt.Errorf("tools/list envelope: %w", err)
	}
	resultRaw, ok := envelope["result"]
	if !ok {
		// Error envelope (or ping/notification response) — pass through.
		return nil, false, nil
	}
	var resultObj map[string]json.RawMessage
	if err := json.Unmarshal(resultRaw, &resultObj); err != nil {
		return nil, false, fmt.Errorf("tools/list result shape: %w", err)
	}
	toolsRaw, hasTools := resultObj["tools"]
	if !hasTools {
		// result without tools key — unexpected; fail-closed.
		return nil, false, errors.New("tools/list result missing tools field")
	}
	var toolsArr []json.RawMessage
	if err := json.Unmarshal(toolsRaw, &toolsArr); err != nil {
		return nil, false, fmt.Errorf("tools/list tools array: %w", err)
	}

	kept := make([]json.RawMessage, 0, len(toolsArr))
	for _, raw := range toolsArr {
		var meta struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &meta); err != nil {
			// Drop malformed tool entries rather than leaking them.
			continue
		}
		if allowed[meta.Name] {
			kept = append(kept, raw)
		}
	}

	keptBytes, err := json.Marshal(kept)
	if err != nil {
		return nil, false, err
	}
	resultObj["tools"] = keptBytes
	newResult, err := json.Marshal(resultObj)
	if err != nil {
		return nil, false, err
	}
	envelope["result"] = newResult
	out, err := json.Marshal(envelope)
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}
