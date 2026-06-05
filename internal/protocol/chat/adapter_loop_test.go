package chat

import (
	"encoding/json"
	"testing"
)

func mkAssistant(toolName, args string) ChatMessage {
	return ChatMessage{
		Role: "assistant",
		ToolCalls: []ToolCall{{
			ID:   "call_test",
			Type: "function",
			Function: ToolCallFunc{
				Name:      toolName,
				Arguments: json.RawMessage(args),
			},
		}},
	}
}

func mkToolError(content string) ChatMessage {
	return ChatMessage{Role: "tool", Content: content}
}

func mkUser(content string) ChatMessage {
	return ChatMessage{Role: "user", Content: content}
}

func TestStripEmptyArgToolCalls(t *testing.T) {
	msgs := []ChatMessage{
		mkAssistant("WebSearch", `{"query":"hello"}`),
		mkToolError("some result"), // normal - should stay
		mkUser("what's next?"),
		mkAssistant("run_python_code", `{}`),  // empty args
		mkToolError("MCP error -32602: Invalid arguments"), // error
		mkAssistant("run_python_code", `{}`),  // empty args again
		mkToolError("MCP error -32602: Invalid arguments"),
		mkAssistant("run_python_code", `{}`),  // third time
		mkToolError("MCP error -32602: Invalid arguments"),
		mkUser("what were you trying to do?"),
	}

	result := stripEmptyArgToolCalls(msgs)

	// Should have: normal pair (2) + user (1) + 3 collapsed pairs removed + user (1) = 4
	// Actually: normal pair stays (2) + user (1) + all 3 empty-arg pairs removed (0) + last user (1) = 4
	if len(result) < 4 || len(result) > 6 {
		t.Errorf("expected ~4-6 messages after strip, got %d", len(result))
	}

	// Verify no empty-arg tool calls remain.
	for i, m := range result {
		for _, tc := range m.ToolCalls {
			args := string(tc.Function.Arguments)
			if args == "{}" || args == `""` || args == "" {
				t.Errorf("message [%d] still has empty-arg tool call: %s", i, args)
			}
		}
	}

	// Verify the first pair (good tool call) is preserved.
	if len(result) < 2 || result[0].Role != "assistant" || len(result[0].ToolCalls) == 0 {
		t.Error("first good assistant message was stripped")
	}
}

func TestStripKeepsEmptyArgWithoutError(t *testing.T) {
	// Empty args but NO error follow-up — should keep it (legitimate call).
	msgs := []ChatMessage{
		mkAssistant("list_things", `{}`),        // empty args, but...
		mkToolError("here are the results: [...]"), // not an error message
	}
	result := stripEmptyArgToolCalls(msgs)
	if len(result) != 2 {
		t.Errorf("expected 2 messages (kept), got %d", len(result))
	}
}

func TestCollapseToolCallLoops(t *testing.T) {
	msgs := []ChatMessage{
		mkUser("do something"),
		mkAssistant("run_python_code", `{"code":"print(1)"}`),
		mkToolError("MCP error -32602"),
		mkAssistant("run_python_code", `{"code":"print(1)"}`),  // same args
		mkToolError("MCP error -32602"),
		mkAssistant("run_python_code", `{"code":"print(1)"}`),  // same args
		mkToolError("MCP error -32602"),
		mkUser("continue please"),
	}

	result := collapseToolCallLoops(msgs)

	// Should collapse 3 identical failing calls to: first attempt + error + summary
	hasSummary := false
	for _, m := range result {
		if m.Role == "assistant" {
			if s, ok := m.Content.(string); ok && len(s) > 0 && len(m.ToolCalls) == 0 {
				hasSummary = true
			}
		}
	}
	if !hasSummary {
		t.Error("expected a summary message after collapse")
	}
	if len(result) >= len(msgs) {
		t.Errorf("expected fewer messages after collapse, got %d (was %d)", len(result), len(msgs))
	}
}
