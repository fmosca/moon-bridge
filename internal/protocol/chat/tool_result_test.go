package chat

import (
	"encoding/json"
	"fmt"
	"testing"

	"moonbridge/internal/format"
)

func TestToolResultMessageSerialization(t *testing.T) {
	// Tool result messages must have content as a string, not a map or array.
	// DeepSeek/Ollama return 400: "invalid type: map, expected a string"
	// if content is not a plain string.

	adapter := NewChatProviderAdapter(4096, nil, format.CorePluginHooks{})

	tests := []struct {
		name    string
		msg     format.CoreMessage
		wantStr bool // content should marshal as a JSON string
	}{
		{
			name: "simple tool result with text",
			msg: format.CoreMessage{
				Role: "tool",
				Content: []format.CoreContentBlock{{
					Type:              "tool_result",
					ToolUseID:         "call_123",
					ToolResultContent: []format.CoreContentBlock{{Type: "text", Text: "Result: 42"}},
				}},
			},
			wantStr: true,
		},
		{
			name: "tool result with error text",
			msg: format.CoreMessage{
				Role: "tool",
				Content: []format.CoreContentBlock{{
					Type:              "tool_result",
					ToolUseID:         "call_456",
					ToolResultContent: []format.CoreContentBlock{{Type: "text", Text: "Error: Invalid input for tool WebFetch"}},
				}},
			},
			wantStr: true,
		},
		{
			name: "tool result with empty content",
			msg: format.CoreMessage{
				Role: "tool",
				Content: []format.CoreContentBlock{{
					Type:              "tool_result",
					ToolUseID:         "call_789",
					ToolResultContent: nil,
				}},
			},
			wantStr: true,
		},
		{
			name: "tool result with multiple text blocks",
			msg: format.CoreMessage{
				Role: "tool",
				Content: []format.CoreContentBlock{{
					Type:      "tool_result",
					ToolUseID: "call_multi",
					ToolResultContent: []format.CoreContentBlock{
						{Type: "text", Text: "Part 1. "},
						{Type: "text", Text: "Part 2."},
					},
				}},
			},
			wantStr: true,
		},
		{
			name: "tool result with user role (round-trip from Chat Completions)",
			msg: format.CoreMessage{
				Role: "user", // Core format uses 'user' role for tool results
				Content: []format.CoreContentBlock{{
					Type:              "tool_result",
					ToolUseID:         "call_roundtrip",
					ToolResultContent: []format.CoreContentBlock{{Type: "text", Text: "Round-trip result"}},
				}},
			},
			wantStr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chatMsg := adapter.toChatMessage(tt.msg)

			// Serialize the message
			b, err := json.Marshal(chatMsg)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}

			// Parse back to check content type
			var parsed struct {
				Role       string `json:"role"`
				Content    any    `json:"content"`
				ToolCallID string `json:"tool_call_id,omitempty"`
			}
			if err := json.Unmarshal(b, &parsed); err != nil {
				t.Fatalf("unmarshal: %v (raw: %s)", err, string(b))
			}

			fmt.Printf("  %s: role=%s content_type=%T content=%v tool_call_id=%s\n",
				tt.name, parsed.Role, parsed.Content, parsed.Content, parsed.ToolCallID)

			// All tool result messages must have role "tool" in the Chat Completions format.
			if parsed.Role != "tool" {
				t.Errorf("role = %q, want tool", parsed.Role)
			}

			if tt.wantStr {
				_, ok := parsed.Content.(string)
				if !ok {
					t.Errorf("content is %T, want string: %v", parsed.Content, parsed.Content)
				}
			}
		})
	}
}