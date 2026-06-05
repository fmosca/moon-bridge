package chat

import (
	"encoding/json"
	"testing"
)

func TestIsEmptyArgsEncodingVariants(t *testing.T) {
	cases := []struct {
		raw      string
		expected bool
	}{
		// Direct forms
		{``, true},
		{`""`, true},
		{`{}`, true},
		{`"{}"`, true},
		// Double-encoded: json.Marshal("") = "\"\""
		{`"\"\""`, true},
		// Triple-encoded: json.Marshal("\"\"") = "\"\\\"\\\"\""
		{`"\"\\\"\\\"\""`, true},
		// Valid args — should NOT be empty
		{`{"url":"https://example.com"}`, false},
		{`{"query":"test"}`, false},
	}

	for _, tc := range cases {
		// We can't call isEmptyArgs directly (it's a closure), so we
		// test stripEmptyArgToolCalls with a synthetic message pair.
		assistant := ChatMessage{
			Role: "assistant",
			ToolCalls: []ToolCall{{
				Function: ToolCallFunc{
					Name:      "TestTool",
					Arguments: json.RawMessage(tc.raw),
				},
			}},
		}
		toolErr := ChatMessage{
			Role:    "tool",
			Content: "Invalid input for tool TestTool: Type validation failed",
		}
		result := stripEmptyArgToolCalls([]ChatMessage{assistant, toolErr})

		stripped := len(result) == 0
		if stripped != tc.expected {
			t.Errorf("raw=%q  expected_stripped=%v  got_stripped=%v", tc.raw, tc.expected, stripped)
		}
	}
}