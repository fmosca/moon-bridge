package openai_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"moonbridge/internal/format"
	"moonbridge/internal/protocol/openai"
)

func TestToCoreRequest_BasicText(t *testing.T) {
	adapter := openai.NewOpenAIAdapter(format.CorePluginHooks{})

	req := &openai.ResponsesRequest{
		Model: "gpt-4o",
		Input: json.RawMessage(`"hello"`),
	}

	result, err := adapter.ToCoreRequest(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	if result.Model != "gpt-4o" {
		t.Errorf("Model = %q", result.Model)
	}
	if len(result.Messages) != 1 {
		t.Fatalf("got %d messages", len(result.Messages))
	}
	if result.Messages[0].Role != "user" {
		t.Errorf("Role = %q", result.Messages[0].Role)
	}
	if len(result.Messages[0].Content) != 1 {
		t.Fatalf("got %d content blocks", len(result.Messages[0].Content))
	}
	if result.Messages[0].Content[0].Text != "hello" {
		t.Errorf("Text = %q", result.Messages[0].Content[0].Text)
	}
}

func TestToCoreRequest_WithInstructions(t *testing.T) {
	adapter := openai.NewOpenAIAdapter(format.CorePluginHooks{})

	req := &openai.ResponsesRequest{
		Model:        "gpt-4o",
		Input:        json.RawMessage(`"hello"`),
		Instructions: "Be concise.",
	}

	result, err := adapter.ToCoreRequest(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.System) == 0 {
		t.Fatal("expected system blocks")
	}
	if result.System[0].Text != "Be concise." {
		t.Errorf("System text = %q", result.System[0].Text)
	}
}

func TestToCoreRequest_AppendsInjectedTools(t *testing.T) {
	adapter := openai.NewOpenAIAdapter(format.CorePluginHooks{
		InjectTools: func(context.Context) []format.CoreTool {
			return []format.CoreTool{{
				Name:        "visual_brief",
				Description: "inspect attached image",
				InputSchema: map[string]any{"type": "object"},
			}}
		},
	})

	req := &openai.ResponsesRequest{
		Model: "gpt-4o",
		Input: json.RawMessage(`"describe the attached image"`),
	}

	result, err := adapter.ToCoreRequest(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Tools) != 1 {
		t.Fatalf("got %d tools, want 1: %+v", len(result.Tools), result.Tools)
	}
	if result.Tools[0].Name != "visual_brief" {
		t.Fatalf("tool name = %q, want visual_brief", result.Tools[0].Name)
	}
}

func TestToCoreRequest_FunctionCallOutputImage(t *testing.T) {
	adapter := openai.NewOpenAIAdapter(format.CorePluginHooks{})

	req := &openai.ResponsesRequest{
		Model: "gpt-4o",
		Input: json.RawMessage(`[
			{"type":"function_call","call_id":"call_view","name":"view_image","arguments":"{\"path\":\"dog.jpg\"}"},
			{"type":"function_call_output","call_id":"call_view","output":[
				{"type":"input_image","image_url":"data:image/jpeg;base64,abc123","detail":"original"}
			]}
		]`),
	}

	result, err := adapter.ToCoreRequest(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Messages) != 2 {
		t.Fatalf("messages = %d, want 2: %+v", len(result.Messages), result.Messages)
	}
	toolResult := result.Messages[1].Content[0]
	if toolResult.Type != "tool_result" || toolResult.ToolUseID != "call_view" {
		t.Fatalf("tool result = %+v", toolResult)
	}
	if len(toolResult.ToolResultContent) != 1 {
		t.Fatalf("tool result content = %+v", toolResult.ToolResultContent)
	}
	image := toolResult.ToolResultContent[0]
	if image.Type != "image" || image.ImageData != "data:image/jpeg;base64,abc123" || image.MediaType != "image/jpeg" {
		t.Fatalf("image block = %+v", image)
	}
}

func TestFromCoreResponse_Basic(t *testing.T) {
	adapter := openai.NewOpenAIAdapter(format.CorePluginHooks{})

	coreResp := &format.CoreResponse{
		ID:     "resp_123",
		Status: "completed",
		Model:  "gpt-4o",
		Messages: []format.CoreMessage{
			{Role: "assistant", Content: []format.CoreContentBlock{{Type: "text", Text: "Hello!"}}},
		},
		Usage: format.CoreUsage{InputTokens: 10, OutputTokens: 20, TotalTokens: 30},
	}

	result, err := adapter.FromCoreResponse(context.Background(), coreResp)
	if err != nil {
		t.Fatal(err)
	}

	resp, ok := result.(*openai.Response)
	if !ok {
		t.Fatal("expected *openai.Response")
	}

	if resp.ID != "resp_123" {
		t.Errorf("ID = %q", resp.ID)
	}
	if resp.Status != "completed" {
		t.Errorf("Status = %q", resp.Status)
	}
}

func TestFromCoreResponse_Error(t *testing.T) {
	adapter := openai.NewOpenAIAdapter(format.CorePluginHooks{})

	coreResp := &format.CoreResponse{
		Status: "failed",
		Error:  &format.CoreError{Message: "upstream error", Code: "api_error"},
	}

	result, err := adapter.FromCoreResponse(context.Background(), coreResp)
	if err != nil {
		t.Fatal(err)
	}
	resp := result.(*openai.Response)

	if resp.Status != "failed" {
		t.Errorf("Status = %q", resp.Status)
	}
	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Message != "upstream error" {
		t.Errorf("Error.Message = %q", resp.Error.Message)
	}
}

func TestToCoreRequest_NilInput(t *testing.T) {
	adapter := openai.NewOpenAIAdapter(format.CorePluginHooks{})

	req := &openai.ResponsesRequest{
		Model: "gpt-4o",
		Input: nil,
	}

	_, err := adapter.ToCoreRequest(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
}

func TestToCoreRequest_ReasoningModelInjectsEmptyReasoningBeforeFunctionCall(t *testing.T) {
	adapter := openai.NewOpenAIAdapter(format.CorePluginHooks{})
	req := &openai.ResponsesRequest{
		Model: "o3-mini",
		Input: json.RawMessage(`[
			{"type":"function_call","id":"fc_1","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"Paris\"}"}
		]`),
	}
	result, err := adapter.ToCoreRequest(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Messages) != 1 {
		t.Fatalf("messages len=%d, want 1", len(result.Messages))
	}
	if len(result.Messages[0].Content) < 2 {
		t.Fatalf("assistant content len=%d, want >=2", len(result.Messages[0].Content))
	}
	if result.Messages[0].Content[0].Type != "reasoning" {
		t.Fatalf("first content type=%q, want reasoning", result.Messages[0].Content[0].Type)
	}
	if result.Messages[0].Content[1].Type != "tool_use" {
		t.Fatalf("second content type=%q, want tool_use", result.Messages[0].Content[1].Type)
	}
}

func TestToCoreRequest_KeepsToolUseAdjacentToToolResultWhenReasoningPrecedesOutput(t *testing.T) {
	adapter := openai.NewOpenAIAdapter(format.CorePluginHooks{})
	req := &openai.ResponsesRequest{
		Model: "gpt-5.4",
		Input: json.RawMessage(`[
			{"type":"function_call","id":"fc_1","call_id":"call_1","name":"tool_a","arguments":"{\"a\":1}"},
			{"type":"reasoning","summary":[{"type":"text","text":"thinking after tool call"}]},
			{"type":"function_call_output","call_id":"call_1","output":"ok"}
		]`),
	}

	result, err := adapter.ToCoreRequest(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Messages) != 2 {
		t.Fatalf("messages len=%d, want 2; got %+v", len(result.Messages), result.Messages)
	}

	assistant := result.Messages[0]
	if assistant.Role != "assistant" {
		t.Fatalf("messages[0].Role=%q, want assistant", assistant.Role)
	}
	if len(assistant.Content) != 2 {
		t.Fatalf("assistant content len=%d, want 2; got %+v", len(assistant.Content), assistant.Content)
	}
	if assistant.Content[0].Type != "reasoning" || assistant.Content[0].ReasoningText != "thinking after tool call" {
		t.Fatalf("assistant.Content[0]=%+v, want merged reasoning", assistant.Content[0])
	}
	if assistant.Content[1].Type != "tool_use" || assistant.Content[1].ToolUseID != "call_1" {
		t.Fatalf("assistant.Content[1]=%+v, want tool_use call_1", assistant.Content[1])
	}

	toolResult := result.Messages[1]
	if toolResult.Role != "tool" {
		t.Fatalf("messages[1].Role=%q, want tool", toolResult.Role)
	}
	if len(toolResult.Content) != 1 || toolResult.Content[0].Type != "tool_result" || toolResult.Content[0].ToolUseID != "call_1" {
		t.Fatalf("tool result message=%+v", toolResult)
	}
}

func TestToCoreRequest_BatchesCustomToolCallsAndOutputsIntoSingleRound(t *testing.T) {
	adapter := openai.NewOpenAIAdapter(format.CorePluginHooks{})
	req := &openai.ResponsesRequest{
		Model: "gpt-5.4",
		Input: json.RawMessage(`[
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"before tools"}]},
			{"type":"custom_tool_call","call_id":"call_a","name":"apply_patch","input":"patch a","arguments":"{\"input\":\"patch a\"}"},
			{"type":"custom_tool_call_output","call_id":"call_a","output":"ok a"},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"between tools"}]},
			{"type":"custom_tool_call","call_id":"call_b","name":"apply_patch","input":"patch b","arguments":"{\"input\":\"patch b\"}"},
			{"type":"custom_tool_call_output","call_id":"call_b","output":"ok b"},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"between tools 2"}]},
			{"type":"custom_tool_call","call_id":"call_c","name":"apply_patch","input":"patch c","arguments":"{\"input\":\"patch c\"}"},
			{"type":"custom_tool_call_output","call_id":"call_c","output":"ok c"},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"after tools"}]}
		]`),
	}

	result, err := adapter.ToCoreRequest(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	if len(result.Messages) != 10 {
		t.Fatalf("messages len=%d, want 10; got %+v", len(result.Messages), result.Messages)
	}

	if result.Messages[0].Role != "assistant" || len(result.Messages[0].Content) != 1 || result.Messages[0].Content[0].Text != "before tools" {
		t.Fatalf("messages[0]=%+v, want pre-tool assistant text", result.Messages[0])
	}

	for i, want := range []struct {
		assistantTextIdx int
		msgIdx           int
		callID  string
		outcome string
	}{
		{0, 1, "call_a", "ok a"},
		{3, 4, "call_b", "ok b"},
		{6, 7, "call_c", "ok c"},
	} {
		if result.Messages[want.assistantTextIdx].Role != "assistant" {
			t.Fatalf("assistant commentary turn %d = %+v", i, result.Messages[want.assistantTextIdx])
		}
		assistant := result.Messages[want.msgIdx]
		if assistant.Role != "assistant" || len(assistant.Content) != 1 || assistant.Content[0].Type != "tool_use" || assistant.Content[0].ToolUseID != want.callID {
			t.Fatalf("assistant tool turn %d = %+v", i, assistant)
		}
		toolResult := result.Messages[want.msgIdx+1]
		if toolResult.Role != "tool" || len(toolResult.Content) != 1 || toolResult.Content[0].Type != "tool_result" || toolResult.Content[0].ToolUseID != want.callID {
			t.Fatalf("tool result turn %d = %+v", i, toolResult)
		}
		if got := toolResult.Content[0].ToolResultContent[0].Text; got != want.outcome {
			t.Fatalf("tool result text turn %d = %q, want %q", i, got, want.outcome)
		}
	}

	if result.Messages[9].Role != "assistant" || len(result.Messages[9].Content) != 1 || result.Messages[9].Content[0].Text != "after tools" {
		t.Fatalf("messages[9]=%+v, want trailing assistant text", result.Messages[9])
	}
}

func TestFromCoreStream_NoDuplicateDoneForToolUse(t *testing.T) {
	adapter := openai.NewOpenAIAdapter(format.CorePluginHooks{})
	coreReq := &format.CoreRequest{Model: "gpt-4o"}
	evCh := make(chan format.CoreStreamEvent, 8)
	evCh <- format.CoreStreamEvent{
		Type:  format.CoreContentBlockStarted,
		Index: 5,
		ContentBlock: &format.CoreContentBlock{
			Type:      "tool_use",
			ToolUseID: "call_1",
			ToolName:  "exec_command",
		},
	}
	evCh <- format.CoreStreamEvent{Type: format.CoreToolCallArgsDelta, Index: 5, Delta: `{"cmd":"ls"}`}
	evCh <- format.CoreStreamEvent{Type: format.CoreToolCallArgsDone, Index: 5, Delta: `{"cmd":"ls"}`}
	evCh <- format.CoreStreamEvent{Type: format.CoreContentBlockDone, Index: 5}
	evCh <- format.CoreStreamEvent{Type: format.CoreEventCompleted, Status: "completed"}
	close(evCh)

	streamAny, err := adapter.FromCoreStream(context.Background(), coreReq, evCh)
	if err != nil {
		t.Fatal(err)
	}
	stream := streamAny.(<-chan openai.StreamEvent)
	var argsDone int
	var itemDone int
	for ev := range stream {
		if ev.Event == "response.function_call_arguments.done" {
			argsDone++
		}
		if ev.Event == "response.output_item.done" {
			if data, ok := ev.Data.(openai.OutputItemEvent); ok && strings.HasPrefix(data.Item.CallID, "call_") {
				itemDone++
			}
		}
	}
	if argsDone != 1 {
		t.Fatalf("function_call_arguments.done count=%d, want 1", argsDone)
	}
	if itemDone != 1 {
		t.Fatalf("output_item.done (tool) count=%d, want 1", itemDone)
	}
}

// ---------------------------------------------------------------------------
// Regression: malformed tool call arguments
// ---------------------------------------------------------------------------

// TestToCoreRequest_RepairedFunctionCallArguments verifies that when Codex
// sends a function_call input item with a common trailing-bracket malformation,
// the adapter successfully repairs the arguments and does NOT silently discard
// them or generate an error.
func TestToCoreRequest_RepairedFunctionCallArguments(t *testing.T) {
	adapter := openai.NewOpenAIAdapter(format.CorePluginHooks{})

	// This reproduces the exact scenario from the bug report:
	// The model generated arguments with a trailing "]" making it invalid JSON:
	//   {"edits":[...],"path":"/Users/.../Caddyfile.internal"}]
	malformedArgs := `{"edits": [{"newText": "# --- LLM Proxy ---\\n\\nbridge.lan.fmosca.dev {\\n    import internal_tls\\n    import authelia\\n    reverse_proxy moonbridge:38440\\n}\\n\\n# --- Music ---", "oldText": "# --- Music ---"}], "path": "/Users/francesco.mosca/Work/ansible-homeserver/files/opt/fmosca.dev/caddy/Caddyfile.internal"}]`

	req := &openai.ResponsesRequest{
		Model: "gpt-4o",
		Input: json.RawMessage(`[
			{"type":"function_call","call_id":"call_malformed","name":"filesystem_edit_file","arguments":"` + strings.ReplaceAll(malformedArgs, "\"", "\\\"") + `"},
			{"type":"function_call_output","call_id":"call_malformed","output":"Error: MCP tool arguments must be a JSON object"}
		]`),
	}

	result, err := adapter.ToCoreRequest(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	// Find the tool_use block
	var toolUse *format.CoreContentBlock
	for i := range result.Messages {
		for j := range result.Messages[i].Content {
			b := &result.Messages[i].Content[j]
			if b.Type == "tool_use" && b.ToolUseID == "call_malformed" {
				toolUse = b
			}
		}
	}
	if toolUse == nil {
		t.Fatal("expected tool_use block with call_id=call_malformed")
	}

	parsed := map[string]any{}
	if err := json.Unmarshal(toolUse.ToolInput, &parsed); err != nil {
		t.Fatalf("tool_use ToolInput must be valid JSON, got: %s", string(toolUse.ToolInput))
	}

	// Since it is repaired, it should NOT contain a diagnostic, but have normal fields.
	if _, hasError := parsed["_malformed_arguments_error"]; hasError {
		t.Errorf("expected repaired arguments, but got malformed error key: %v", parsed)
	}
	if path, ok := parsed["path"].(string); !ok || path == "" {
		t.Errorf("expected path to be populated, got: %v", parsed)
	}
}

// TestToCoreRequest_MalformedFunctionCallArguments verifies that when Codex
// sends a function_call input item with completely unrepairable JSON in arguments,
// the adapter does NOT silently discard the arguments to `{}`. Instead it should
// convert the failed tool call and its output into a plain text turn to avoid 400 validation crashes
// on the upstream LLM API while preserving the full debugging context.
func TestToCoreRequest_MalformedFunctionCallArguments(t *testing.T) {
	adapter := openai.NewOpenAIAdapter(format.CorePluginHooks{})

	unrepairableArgs := `{"edits":`

	req := &openai.ResponsesRequest{
		Model: "gpt-4o",
		Input: json.RawMessage(`[
			{"type":"function_call","call_id":"call_malformed","name":"filesystem_edit_file","arguments":"` + strings.ReplaceAll(unrepairableArgs, "\"", "\\\"") + `"},
			{"type":"function_call_output","call_id":"call_malformed","output":"Error: MCP tool arguments must be a JSON object"}
		]`),
	}

	result, err := adapter.ToCoreRequest(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	// Verify that we converted to plain text assistant/user messages
	if len(result.Messages) < 2 {
		t.Fatalf("expected at least 2 converted messages, got: %d", len(result.Messages))
	}

	msgAssistant := result.Messages[0]
	if msgAssistant.Role != "assistant" {
		t.Errorf("expected role assistant for first message, got: %s", msgAssistant.Role)
	}
	if len(msgAssistant.Content) != 1 || msgAssistant.Content[0].Type != "text" {
		t.Fatalf("expected a single text block, got: %v", msgAssistant.Content)
	}
	if !strings.Contains(msgAssistant.Content[0].Text, "filesystem_edit_file") {
		t.Errorf("expected text to mention tool name, got: %s", msgAssistant.Content[0].Text)
	}
	if !strings.Contains(msgAssistant.Content[0].Text, unrepairableArgs) {
		t.Errorf("expected text to contain raw arguments, got: %s", msgAssistant.Content[0].Text)
	}

	msgUser := result.Messages[1]
	if msgUser.Role != "user" {
		t.Errorf("expected role user for second message, got: %s", msgUser.Role)
	}
	if len(msgUser.Content) != 1 || msgUser.Content[0].Type != "text" {
		t.Fatalf("expected a single text block, got: %v", msgUser.Content)
	}
	expectedOutput := `Error: "Error: MCP tool arguments must be a JSON object"`
	if msgUser.Content[0].Text != expectedOutput {
		t.Errorf("expected text '%s', got: %s", expectedOutput, msgUser.Content[0].Text)
	}
}

// TestFromCoreStream_RepairedNamespaceToolArgs verifies that when the
// upstream model generates a tool_use block for a nested namespace tool with
// a common trailing-bracket malformation, the streaming adapter successfully
// repairs it and returns clean, valid arguments so the tool call can succeed.
func TestFromCoreStream_RepairedNamespaceToolArgs(t *testing.T) {
	adapter := openai.NewOpenAIAdapter(format.CorePluginHooks{})

	// Build a CoreRequest with a tool map containing a namespace tool.
	coreReq := &format.CoreRequest{
		Model: "gpt-4o",
		Extensions: map[string]any{
			"codex_tool_map": map[string]any{
				"mcp__filesystem": map[string]any{
					"kind":        "nested_namespace",
					"openai_name": "mcp__filesystem",
					"namespace":  "",
				},
			},
		},
	}

	// Simulate: model calls the namespace tool with malformed JSON params
	evCh := make(chan format.CoreStreamEvent, 8)
	evCh <- format.CoreStreamEvent{
		Type:  format.CoreContentBlockStarted,
		Index: 0,
		ContentBlock: &format.CoreContentBlock{
			Type:      "tool_use",
			ToolUseID: "call_repaired",
			ToolName:  "mcp__filesystem",
		},
	}
	// Malformed arguments: trailing ']' at the end of params
	malformedParams := `{"action":"edit_file","params":{"edits":[{"newText":"x"}],"path":"/foo.txt"}]}`
	evCh <- format.CoreStreamEvent{Type: format.CoreToolCallArgsDelta, Index: 0, Delta: malformedParams}
	evCh <- format.CoreStreamEvent{Type: format.CoreToolCallArgsDone, Index: 0, Delta: malformedParams}
	evCh <- format.CoreStreamEvent{Type: format.CoreContentBlockDone, Index: 0}
	evCh <- format.CoreStreamEvent{Type: format.CoreEventCompleted, Status: "completed"}
	close(evCh)

	streamAny, err := adapter.FromCoreStream(context.Background(), coreReq, evCh)
	if err != nil {
		t.Fatal(err)
	}
	stream := streamAny.(<-chan openai.StreamEvent)

	var argsDone openai.FunctionCallArgumentsDoneEvent
	for ev := range stream {
		if ev.Event == "response.function_call_arguments.done" {
			argsDone = ev.Data.(openai.FunctionCallArgumentsDoneEvent)
		}
	}

	// The arguments must be valid JSON (so Codex can parse them for the MCP call).
	var parsed map[string]any
	if err := json.Unmarshal([]byte(argsDone.Arguments), &parsed); err != nil {
		t.Fatalf("function_call arguments must be valid JSON, got: %s\nunmarshal error: %v", argsDone.Arguments, err)
	}

	// Since it is repaired, it should NOT contain a diagnostic, but have normal fields.
	if _, hasErr := parsed["_malformed_arguments_error"]; hasErr {
		t.Errorf("expected repaired arguments, but got malformed error key: %v", parsed)
	}
	if path, ok := parsed["path"].(string); !ok || path != "/foo.txt" {
		t.Errorf("expected path to be /foo.txt, got: %v", parsed)
	}
}

// TestFromCoreStream_MalformedNamespaceToolArgs verifies that when the
// upstream model generates a tool_use block for a nested namespace tool with
// completely unrepairable arguments, the streaming adapter produces a function_call that
// has valid Arguments (not garbage), and includes a diagnostic message.
func TestFromCoreStream_MalformedNamespaceToolArgs(t *testing.T) {
	adapter := openai.NewOpenAIAdapter(format.CorePluginHooks{})

	// Build a CoreRequest with a tool map containing a namespace tool.
	coreReq := &format.CoreRequest{
		Model: "gpt-4o",
		Extensions: map[string]any{
			"codex_tool_map": map[string]any{
				"mcp__filesystem": map[string]any{
					"kind":        "nested_namespace",
					"openai_name": "mcp__filesystem",
					"namespace":  "",
				},
			},
		},
	}

	// Simulate: model calls the namespace tool with unrepairable JSON params
	evCh := make(chan format.CoreStreamEvent, 8)
	evCh <- format.CoreStreamEvent{
		Type:  format.CoreContentBlockStarted,
		Index: 0,
		ContentBlock: &format.CoreContentBlock{
			Type:      "tool_use",
			ToolUseID: "call_bad",
			ToolName:  "mcp__filesystem",
		},
	}
	unrepairableParams := `{"action":"edit_file","params":{invalid garbage`
	evCh <- format.CoreStreamEvent{Type: format.CoreToolCallArgsDelta, Index: 0, Delta: unrepairableParams}
	evCh <- format.CoreStreamEvent{Type: format.CoreToolCallArgsDone, Index: 0, Delta: unrepairableParams}
	evCh <- format.CoreStreamEvent{Type: format.CoreContentBlockDone, Index: 0}
	evCh <- format.CoreStreamEvent{Type: format.CoreEventCompleted, Status: "completed"}
	close(evCh)

	streamAny, err := adapter.FromCoreStream(context.Background(), coreReq, evCh)
	if err != nil {
		t.Fatal(err)
	}
	stream := streamAny.(<-chan openai.StreamEvent)

	var argsDone openai.FunctionCallArgumentsDoneEvent
	for ev := range stream {
		if ev.Event == "response.function_call_arguments.done" {
			argsDone = ev.Data.(openai.FunctionCallArgumentsDoneEvent)
		}
	}

	// The arguments must be valid JSON (so Codex can parse them for the MCP call).
	var parsed map[string]any
	if err := json.Unmarshal([]byte(argsDone.Arguments), &parsed); err != nil {
		t.Fatalf("function_call arguments must be valid JSON, got: %s\nunmarshal error: %v", argsDone.Arguments, err)
	}

	// Should contain a diagnostic about malformed arguments.
	if _, hasErr := parsed["_malformed_arguments_error"]; !hasErr {
		t.Errorf("expected _malformed_arguments_error key in arguments, got: %v", parsed)
	}
}

// TestToCoreRequest_NamespacedToolCallReconstruction verifies that when Codex
// sends a history item with a namespace (e.g. name="edit_file", namespace="mcp__filesystem"),
// it is reconstructed as a nested call to the namespace tool mcp__filesystem with nested action and params.
func TestToCoreRequest_NamespacedToolCallReconstruction(t *testing.T) {
	adapter := openai.NewOpenAIAdapter(format.CorePluginHooks{})

	arguments := `{"edits": [{"newText": "foo", "oldText": "bar"}], "path": "/Users/test/file"}`

	req := &openai.ResponsesRequest{
		Model: "gpt-4o",
		Input: json.RawMessage(`[
			{"type":"function_call","call_id":"call_namespaced","name":"edit_file","namespace":"mcp__filesystem","arguments":"` + strings.ReplaceAll(arguments, "\"", "\\\"") + `"}
		]`),
	}

	result, err := adapter.ToCoreRequest(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	var toolUse *format.CoreContentBlock
	for i := range result.Messages {
		for j := range result.Messages[i].Content {
			b := &result.Messages[i].Content[j]
			if b.Type == "tool_use" && b.ToolUseID == "call_namespaced" {
				toolUse = b
			}
		}
	}
	if toolUse == nil {
		t.Fatal("expected tool_use block with call_id=call_namespaced")
	}

	// ToolName must be the namespace
	if toolUse.ToolName != "mcp__filesystem" {
		t.Errorf("expected ToolName to be 'mcp__filesystem', got: %s", toolUse.ToolName)
	}

	// ToolInput must contain action and params
	parsed := map[string]any{}
	if err := json.Unmarshal(toolUse.ToolInput, &parsed); err != nil {
		t.Fatalf("tool_use ToolInput must be valid JSON, got: %s", string(toolUse.ToolInput))
	}

	if parsed["action"] != "edit_file" {
		t.Errorf("expected action to be 'edit_file', got: %v", parsed["action"])
	}

	params, ok := parsed["params"].(map[string]any)
	if !ok {
		t.Fatalf("expected params to be an object, got: %v", parsed["params"])
	}

	if params["path"] != "/Users/test/file" {
		t.Errorf("expected path to be '/Users/test/file', got: %v", params["path"])
	}
}

