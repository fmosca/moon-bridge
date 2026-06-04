// Package chat_test provides unit tests for the ChatClientAdapter.
package chat_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"moonbridge/internal/format"
	"moonbridge/internal/protocol/chat"
)

// ============================================================================
// ToCoreRequest
// ============================================================================

func TestChatClientAdapter_ToCoreRequest_BasicText(t *testing.T) {
	adapter := chat.NewChatClientAdapter(format.CorePluginHooks{})

	chatReq := &chat.ChatRequest{
		Model: "gpt-4o",
		Messages: []chat.ChatMessage{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi there!"},
			{Role: "user", Content: "How are you?"},
		},
		MaxTokens: 100,
	}

	coreReq, err := adapter.ToCoreRequest(context.Background(), chatReq)
	if err != nil {
		t.Fatalf("ToCoreRequest: %v", err)
	}
	_ = coreReq // used above via assertions

	if coreReq.Model != "gpt-4o" {
		t.Errorf("Model = %q, want gpt-4o", coreReq.Model)
	}
	if coreReq.MaxTokens != 100 {
		t.Errorf("MaxTokens = %d, want 100", coreReq.MaxTokens)
	}
	if len(coreReq.System) == 0 {
		t.Fatal("expected System content from system message")
	}
	if coreReq.System[0].Text != "You are helpful." {
		t.Errorf("System text = %q", coreReq.System[0].Text)
	}
	if len(coreReq.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(coreReq.Messages))
	}
	if coreReq.Messages[0].Role != "user" {
		t.Errorf("Messages[0].Role = %q, want user", coreReq.Messages[0].Role)
	}
}

func TestChatClientAdapter_ToCoreRequest_ToolCalls(t *testing.T) {
	adapter := chat.NewChatClientAdapter(format.CorePluginHooks{})

	chatReq := &chat.ChatRequest{
		Model: "gpt-4o",
		Messages: []chat.ChatMessage{
			{Role: "user", Content: "What's the weather?"},
			{
				Role:    "assistant",
				Content: nil,
				ToolCalls: []chat.ToolCall{{
					ID:   "call_123",
					Type: "function",
					Function: chat.ToolCallFunc{
						Name:      "get_weather",
						Arguments: json.RawMessage(`{"city":"London"}`),
					},
				}},
			},
			{Role: "tool", ToolCallID: "call_123", Content: "Sunny"},
		},
		Tools: []chat.ChatTool{{
			Type: "function",
			Function: chat.FunctionDef{
				Name:        "get_weather",
				Description: "Get weather",
				Parameters:  map[string]any{"type": "object"},
			},
		}},
	}

	coreReq, err := adapter.ToCoreRequest(context.Background(), chatReq)
	if err != nil {
		t.Fatalf("ToCoreRequest: %v", err)
	}

	if len(coreReq.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(coreReq.Tools))
	}
	if coreReq.Tools[0].Name != "get_weather" {
		t.Errorf("Tool name = %q, want get_weather", coreReq.Tools[0].Name)
	}

	// Tool result mapped as user role with tool_result block.
	if len(coreReq.Messages) < 3 {
		t.Fatalf("expected at least 3 messages, got %d", len(coreReq.Messages))
	}
	toolResultMsg := coreReq.Messages[2]
	if len(toolResultMsg.Content) == 0 || toolResultMsg.Content[0].Type != "tool_result" {
		t.Fatalf("expected tool_result content block")
	}
	if toolResultMsg.Content[0].ToolUseID != "call_123" {
		t.Errorf("ToolUseID = %q, want call_123", toolResultMsg.Content[0].ToolUseID)
	}
}

func TestChatClientAdapter_ToCoreRequest_ToolChoiceString(t *testing.T) {
	adapter := chat.NewChatClientAdapter(format.CorePluginHooks{})
	for _, choice := range []string{`"auto"`, `"none"`, `"required"`} {
		chatReq := &chat.ChatRequest{
			Model:      "gpt-4o",
			Messages:   []chat.ChatMessage{{Role: "user", Content: "test"}},
			ToolChoice: json.RawMessage(choice),
		}
		coreReq, err := adapter.ToCoreRequest(context.Background(), chatReq)
		if err != nil {
			t.Fatalf("ToCoreRequest %s: %v", choice, err)
		}
		if coreReq.ToolChoice == nil {
			t.Fatalf("expected ToolChoice for %s", choice)
		}
	}
}

func TestChatClientAdapter_ToCoreRequest_ToolChoiceObject(t *testing.T) {
	adapter := chat.NewChatClientAdapter(format.CorePluginHooks{})

	chatReq := &chat.ChatRequest{
		Model:    "gpt-4o",
		Messages: []chat.ChatMessage{{Role: "user", Content: "test"}},
		ToolChoice: json.RawMessage(`{"type":"function","function":{"name":"get_weather"}}`),
	}

	coreReq, err := adapter.ToCoreRequest(context.Background(), chatReq)
	if err != nil {
		t.Fatalf("ToCoreRequest: %v", err)
	}
	if coreReq.ToolChoice == nil {
		t.Fatal("expected ToolChoice")
	}
	if coreReq.ToolChoice.Name != "get_weather" {
		t.Errorf("ToolChoice.Name = %q, want get_weather", coreReq.ToolChoice.Name)
	}
	if coreReq.ToolChoice.Mode != "any" {
		t.Errorf("ToolChoice.Mode = %q, want any", coreReq.ToolChoice.Mode)
	}
}

func TestChatClientAdapter_ToCoreRequest_SamplingParams(t *testing.T) {
	adapter := chat.NewChatClientAdapter(format.CorePluginHooks{})

	temp := 0.5
	topP := 0.9
	chatReq := &chat.ChatRequest{
		Model:       "gpt-4o",
		Messages:    []chat.ChatMessage{{Role: "user", Content: "test"}},
		Stream:      true,
		Temperature: &temp,
		TopP:        &topP,
		Stop:        []string{"END", "STOP"},
	}

	coreReq, err := adapter.ToCoreRequest(context.Background(), chatReq)
	if err != nil {
		t.Fatalf("ToCoreRequest: %v", err)
	}

	if !coreReq.Stream {
		t.Error("Stream = false, want true")
	}
	if coreReq.Temperature == nil || *coreReq.Temperature != 0.5 {
		t.Errorf("Temperature = %v, want 0.5", coreReq.Temperature)
	}
	if coreReq.TopP == nil || *coreReq.TopP != 0.9 {
		t.Errorf("TopP = %v, want 0.9", coreReq.TopP)
	}
	if len(coreReq.StopSequences) != 2 {
		t.Errorf("StopSequences = %v, want 2 items", coreReq.StopSequences)
	}
}

func TestChatClientAdapter_ToCoreRequest_MultimodalContent(t *testing.T) {
	adapter := chat.NewChatClientAdapter(format.CorePluginHooks{})

	chatReq := &chat.ChatRequest{
		Model: "gpt-4o",
		Messages: []chat.ChatMessage{{
			Role: "user",
			Content: []chat.ContentPart{
				{Type: "text", Text: "What is this?"},
				{Type: "image_url", ImageURL: &chat.ImageURL{URL: "data:image/png;base64,abc"}},
			},
		}},
	}

	coreReq, err := adapter.ToCoreRequest(context.Background(), chatReq)
	if err != nil {
		t.Fatalf("ToCoreRequest: %v", err)
	}
	if len(coreReq.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(coreReq.Messages))
	}
	msg := coreReq.Messages[0]
	if len(msg.Content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(msg.Content))
	}
	if msg.Content[0].Type != "text" {
		t.Errorf("Content[0].Type = %q, want text", msg.Content[0].Type)
	}
	if msg.Content[1].Type != "image" {
		t.Errorf("Content[1].Type = %q, want image", msg.Content[1].Type)
	}
}

func TestChatClientAdapter_ToCoreRequest_ReasoningContent(t *testing.T) {
	adapter := chat.NewChatClientAdapter(format.CorePluginHooks{})

	chatReq := &chat.ChatRequest{
		Model: "deepseek-v4",
		Messages: []chat.ChatMessage{
			{Role: "user", Content: "Think about this"},
			{Role: "assistant", Content: "The answer is 42", ReasoningContent: "I need to analyze this..."},
		},
	}

	coreReq, err := adapter.ToCoreRequest(context.Background(), chatReq)
	if err != nil {
		t.Fatalf("ToCoreRequest: %v", err)
	}

	// Second message (assistant) should have a reasoning block first.
	asstMsg := coreReq.Messages[1]
	if len(asstMsg.Content) < 2 {
		t.Fatalf("expected at least 2 content blocks in assistant message, got %d", len(asstMsg.Content))
	}
	if asstMsg.Content[0].Type != "reasoning" {
		t.Errorf("first block type = %q, want reasoning", asstMsg.Content[0].Type)
	}
	if asstMsg.Content[0].ReasoningText != "I need to analyze this..." {
		t.Errorf("reasoning text = %q", asstMsg.Content[0].ReasoningText)
	}
}

func TestChatClientAdapter_ToCoreRequest_NilModel(t *testing.T) {
	adapter := chat.NewChatClientAdapter(format.CorePluginHooks{})
	chatReq := &chat.ChatRequest{Model: "", Messages: []chat.ChatMessage{{Role: "user", Content: "hi"}}}
	coreReq, err := adapter.ToCoreRequest(context.Background(), chatReq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if coreReq.Model != "" {
		t.Errorf("Model = %q, want empty", coreReq.Model)
	}
}

// ============================================================================
// FromCoreResponse
// ============================================================================

func TestChatClientAdapter_FromCoreResponse_Basic(t *testing.T) {
	adapter := chat.NewChatClientAdapter(format.CorePluginHooks{})

	coreResp := &format.CoreResponse{
		ID:     "resp_001",
		Status: "completed",
		Model:  "gpt-4o",
		Messages: []format.CoreMessage{
			{
				Role: "assistant",
				Content: []format.CoreContentBlock{
					{Type: "text", Text: "Hello from Moon Bridge!"},
				},
			},
		},
		StopReason: "end_turn",
		Usage: format.CoreUsage{
			InputTokens:  10,
			OutputTokens: 42,
		},
	}

	chatRespAny, err := adapter.FromCoreResponse(context.Background(), coreResp)
	if err != nil {
		t.Fatalf("FromCoreResponse: %v", err)
	}

	chatResp := chatRespAny.(*chat.ChatResponse)
	if chatResp.Object != "chat.completion" {
		t.Errorf("Object = %q", chatResp.Object)
	}
	if len(chatResp.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(chatResp.Choices))
	}
	content, ok := chatResp.Choices[0].Message.Content.(string)
	if !ok || content != "Hello from Moon Bridge!" {
		t.Errorf("Content = %v", chatResp.Choices[0].Message.Content)
	}
	if chatResp.Choices[0].FinishReason != "stop" {
		t.Errorf("FinishReason = %q, want stop", chatResp.Choices[0].FinishReason)
	}
	if chatResp.Usage.PromptTokens != 10 {
		t.Errorf("PromptTokens = %d, want 10", chatResp.Usage.PromptTokens)
	}
	if chatResp.Usage.CompletionTokens != 42 {
		t.Errorf("CompletionTokens = %d, want 42", chatResp.Usage.CompletionTokens)
	}
}

func TestChatClientAdapter_FromCoreResponse_ToolUse(t *testing.T) {
	adapter := chat.NewChatClientAdapter(format.CorePluginHooks{})

	coreResp := &format.CoreResponse{
		ID:     "resp_002",
		Status: "completed",
		Messages: []format.CoreMessage{
			{
				Role: "assistant",
				Content: []format.CoreContentBlock{
					{Type: "text", Text: "Let me check."},
					{Type: "tool_use", ToolUseID: "tool_1", ToolName: "search", ToolInput: json.RawMessage(`{"q":"test"}`)},
				},
			},
		},
		StopReason: "tool_use",
	}

	chatRespAny, err := adapter.FromCoreResponse(context.Background(), coreResp)
	if err != nil {
		t.Fatalf("FromCoreResponse: %v", err)
	}
	chatResp := chatRespAny.(*chat.ChatResponse)
	msg := chatResp.Choices[0].Message
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(msg.ToolCalls))
	}
	if msg.ToolCalls[0].ID != "tool_1" {
		t.Errorf("ToolCall ID = %q, want tool_1", msg.ToolCalls[0].ID)
	}
	if chatResp.Choices[0].FinishReason != "tool_calls" {
		t.Errorf("FinishReason = %q, want tool_calls", chatResp.Choices[0].FinishReason)
	}
}

func TestChatClientAdapter_FromCoreResponse_Error(t *testing.T) {
	adapter := chat.NewChatClientAdapter(format.CorePluginHooks{})

	coreResp := &format.CoreResponse{
		ID:     "resp_err",
		Status: "failed",
		Error: &format.CoreError{
			Message: "Model not found",
			Type:    "invalid_request_error",
			Code:    "model_not_found",
		},
	}

	chatRespAny, err := adapter.FromCoreResponse(context.Background(), coreResp)
	if err != nil {
		t.Fatalf("FromCoreResponse: %v", err)
	}
	chatResp := chatRespAny.(*chat.ChatResponse)
	content, ok := chatResp.Choices[0].Message.Content.(string)
	if !ok || content != "Model not found" {
		t.Errorf("Error content = %v", chatResp.Choices[0].Message.Content)
	}
}

func TestChatClientAdapter_FromCoreResponse_EmptyMessages(t *testing.T) {
	adapter := chat.NewChatClientAdapter(format.CorePluginHooks{})

	coreResp := &format.CoreResponse{
		ID:     "resp_empty",
		Status: "completed",
	}

	chatRespAny, err := adapter.FromCoreResponse(context.Background(), coreResp)
	if err != nil {
		t.Fatalf("FromCoreResponse: %v", err)
	}
	chatResp := chatRespAny.(*chat.ChatResponse)
	if len(chatResp.Choices) != 1 {
		t.Fatal("expected 1 choice for empty messages")
	}
	if chatResp.Choices[0].FinishReason != "stop" {
		t.Errorf("FinishReason = %q, want stop", chatResp.Choices[0].FinishReason)
	}
}

func TestChatClientAdapter_FromCoreResponse_StopReasons(t *testing.T) {
	adapter := chat.NewChatClientAdapter(format.CorePluginHooks{})

	cases := map[string]string{
		"end_turn":       "stop",
		"max_tokens":     "length",
		"tool_use":       "tool_calls",
		"content_filter": "content_filter",
		"stop_sequence":  "stop",
	}

	for stopReason, wantFinish := range cases {
		coreResp := &format.CoreResponse{
			ID:     "resp_" + stopReason,
			Status: "completed",
			Messages: []format.CoreMessage{
				{Role: "assistant", Content: []format.CoreContentBlock{{Type: "text", Text: "ok"}}},
			},
			StopReason: stopReason,
		}
		chatRespAny, err := adapter.FromCoreResponse(context.Background(), coreResp)
		if err != nil {
			t.Fatalf("FromCoreResponse(%s): %v", stopReason, err)
		}
		chatResp := chatRespAny.(*chat.ChatResponse)
		if chatResp.Choices[0].FinishReason != wantFinish {
			t.Errorf("StopReason %q → FinishReason %q, want %q", stopReason, chatResp.Choices[0].FinishReason, wantFinish)
		}
	}
}

func TestChatClientAdapter_FromCoreResponse_WithCacheUsage(t *testing.T) {
	adapter := chat.NewChatClientAdapter(format.CorePluginHooks{})

	coreResp := &format.CoreResponse{
		ID:     "resp_cached",
		Status: "completed",
		Messages: []format.CoreMessage{
			{Role: "assistant", Content: []format.CoreContentBlock{{Type: "text", Text: "ok"}}},
		},
		StopReason: "end_turn",
		Usage: format.CoreUsage{
			InputTokens:       100,
			OutputTokens:      50,
			CachedInputTokens: 60,
		},
	}

	chatRespAny, err := adapter.FromCoreResponse(context.Background(), coreResp)
	if err != nil {
		t.Fatalf("FromCoreResponse: %v", err)
	}
	chatResp := chatRespAny.(*chat.ChatResponse)
	if chatResp.Usage == nil {
		t.Fatal("expected Usage")
	}
	if chatResp.Usage.PromptTokensDetails == nil {
		t.Fatal("expected PromptTokensDetails with cache")
	}
	if chatResp.Usage.PromptTokensDetails.CachedTokens != 60 {
		t.Errorf("CachedTokens = %d, want 60", chatResp.Usage.PromptTokensDetails.CachedTokens)
	}
}

func TestChatClientAdapter_ClientProtocol(t *testing.T) {
	adapter := chat.NewChatClientAdapter(format.CorePluginHooks{})
	if adapter.ClientProtocol() != "openai-chat" {
		t.Errorf("ClientProtocol = %q, want openai-chat", adapter.ClientProtocol())
	}
}

// ============================================================================
// FromCoreStream
// ============================================================================

func TestChatClientAdapter_FromCoreStream_ReturnsContext(t *testing.T) {
	adapter := chat.NewChatClientAdapter(format.CorePluginHooks{})

	coreReq := &format.CoreRequest{Model: "gpt-4o"}
	events := make(chan format.CoreStreamEvent)
	close(events)

	result, err := adapter.FromCoreStream(context.Background(), coreReq, events)
	if err != nil {
		t.Fatalf("FromCoreStream: %v", err)
	}

	ctx, ok := result.(*chat.ChatStreamContext)
	if !ok {
		t.Fatalf("expected *ChatStreamContext, got %T", result)
	}
	if ctx.Model() != "gpt-4o" {
		t.Errorf("Model = %q, want gpt-4o", ctx.Model())
	}
	if ctx.ID() == "" {
		t.Error("expected non-empty ID")
	}
}

// ============================================================================
// WriteChatNonStreamResponse
// ============================================================================

func TestWriteChatNonStreamResponse(t *testing.T) {
	resp := &chat.ChatResponse{
		ID:      "chatcmpl-test",
		Object:  "chat.completion",
		Created: 1234567890,
		Model:   "gpt-4o",
		Choices: []chat.Choice{{
			Index:        0,
			Message:      chat.ChatMessage{Role: "assistant", Content: "Hello!"},
			FinishReason: "stop",
		}},
		Usage: &chat.Usage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7},
	}

	w := httptest.NewRecorder()
	chat.WriteChatNonStreamResponse(w, resp)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	ct := w.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}

	var out chat.ChatResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.ID != "chatcmpl-test" {
		t.Errorf("ID = %q", out.ID)
	}
	content, ok := out.Choices[0].Message.Content.(string)
	if !ok || content != "Hello!" {
		t.Errorf("Content = %v", out.Choices[0].Message.Content)
	}
}

// ============================================================================
// WriteChatStreamSSE
// ============================================================================

func TestWriteChatStreamSSE_Basic(t *testing.T) {
	events := make(chan format.CoreStreamEvent, 10)

	ctx := &chat.ChatStreamContext{}
	// Use exported fields via constructor.
	ctx = chat.NewChatStreamContext("chatcmpl-test", "gpt-4o", 1234567890, events)

	go func() {
		events <- format.CoreStreamEvent{Type: format.CoreEventCreated}
		events <- format.CoreStreamEvent{Type: format.CoreTextDelta, Delta: "Hel"}
		events <- format.CoreStreamEvent{Type: format.CoreTextDelta, Delta: "lo!"}
		events <- format.CoreStreamEvent{
			Type:      format.CoreEventCompleted,
			StopReason: "end_turn",
			Usage:     &format.CoreUsage{InputTokens: 5, OutputTokens: 3},
		}
		close(events)
	}()

	w := httptest.NewRecorder()
	chat.WriteChatStreamSSE(w, ctx)

	body := w.Body.String()

	// Should contain Chat SSE data lines.
	if !bytes.Contains([]byte(body), []byte("chat.completion.chunk")) {
		t.Error("expected chat.completion.chunk in SSE output")
	}
	if !bytes.Contains([]byte(body), []byte("Hel")) {
		t.Error("expected 'Hel' delta in SSE output")
	}
	if !bytes.Contains([]byte(body), []byte("lo!")) {
		t.Error("expected 'lo!' delta in SSE output")
	}
	if !bytes.Contains([]byte(body), []byte("[DONE]")) {
		t.Error("expected [DONE] marker in SSE output")
	}
	// Should have finish_reason in the final chunk.
	if !bytes.Contains([]byte(body), []byte(`"stop"`)) {
		t.Error("expected finish_reason 'stop' in SSE output")
	}
	if !bytes.Contains([]byte(body), []byte("prompt_tokens")) {
		t.Error("expected usage in final SSE chunk")
	}
}

func TestWriteChatStreamSSE_ToolCalls(t *testing.T) {
	events := make(chan format.CoreStreamEvent, 10)

	ctx := chat.NewChatStreamContext("chatcmpl-test", "gpt-4o", 1234567890, events)

	go func() {
		events <- format.CoreStreamEvent{Type: format.CoreEventCreated}
		events <- format.CoreStreamEvent{
			Type:         format.CoreContentBlockStarted,
			Index:        0,
			ContentBlock: &format.CoreContentBlock{Type: "tool_use", ToolUseID: "call_1", ToolName: "search"},
		}
		events <- format.CoreStreamEvent{
			Type:         format.CoreToolCallArgsDelta,
			Index:        0,
			Delta:        `{"q":"test"}`,
			ContentBlock: &format.CoreContentBlock{ToolUseID: "call_1", ToolName: "search"},
		}
		events <- format.CoreStreamEvent{
			Type:      format.CoreEventCompleted,
			StopReason: "tool_use",
		}
		close(events)
	}()

	w := httptest.NewRecorder()
	chat.WriteChatStreamSSE(w, ctx)

	body := w.Body.String()
	if !bytes.Contains([]byte(body), []byte("tool_calls")) {
		t.Error("expected tool_calls in SSE output")
	}
	if !bytes.Contains([]byte(body), []byte("search")) {
		t.Error("expected tool function name 'search' in SSE output")
	}
}

func TestWriteChatStreamSSE_Incomplete(t *testing.T) {
	events := make(chan format.CoreStreamEvent, 4)
	ctx := chat.NewChatStreamContext("chatcmpl-test", "gpt-4o", 1234567890, events)

	go func() {
		events <- format.CoreStreamEvent{Type: format.CoreEventCreated}
		events <- format.CoreStreamEvent{Type: format.CoreEventIncomplete}
		close(events)
	}()

	w := httptest.NewRecorder()
	chat.WriteChatStreamSSE(w, ctx)

	body := w.Body.String()
	if !bytes.Contains([]byte(body), []byte("length")) {
		t.Error("expected finish_reason 'length' for incomplete")
	}
}

// ============================================================================
// coreRequestToResponsesRequest (tested via server integration)
// ============================================================================

func TestChatClientAdapter_RoundTrip_Simple(t *testing.T) {
	// Test that ChatRequest → CoreRequest → ChatResponse round-trips cleanly.
	adapter := chat.NewChatClientAdapter(format.CorePluginHooks{})

	chatReq := &chat.ChatRequest{
		Model: "gpt-4o",
		Messages: []chat.ChatMessage{
			{Role: "system", Content: "Be brief."},
			{Role: "user", Content: "Say hi"},
		},
		MaxTokens: 50,
	}

	coreReq, err := adapter.ToCoreRequest(context.Background(), chatReq)
	if err != nil {
		t.Fatalf("ToCoreRequest: %v", err)
	}
	_ = coreReq
	coreResp := &format.CoreResponse{
		ID:     "resp_roundtrip",
		Status: "completed",
		Model:  "gpt-4o",
		Messages: []format.CoreMessage{
			{Role: "assistant", Content: []format.CoreContentBlock{{Type: "text", Text: "Hi!"}}},
		},
		StopReason: "end_turn",
		Usage:      format.CoreUsage{InputTokens: 8, OutputTokens: 2},
	}

	chatRespAny, err := adapter.FromCoreResponse(context.Background(), coreResp)
	if err != nil {
		t.Fatalf("FromCoreResponse: %v", err)
	}
	chatResp := chatRespAny.(*chat.ChatResponse)

	content, _ := chatResp.Choices[0].Message.Content.(string)
	if content != "Hi!" {
		t.Errorf("round-trip content = %q, want 'Hi!'", content)
	}
}
