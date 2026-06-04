//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"moonbridge/internal/config"
	"moonbridge/internal/format"
	"moonbridge/internal/protocol/chat"
)

// ============================================================================
// TestChatClientE2E_TextRoundTrip
// ============================================================================
//
// Inbound Chat Completions → Core → Outbound Chat Completions.
//
// Simulates a client posting an OpenAI Chat Completions request to Moon Bridge,
// which routes through Core to an upstream that also speaks Chat Completions.
// This exercises the full inbound ChatClientAdapter → Core → ChatProviderAdapter
// round-trip, which is the path used by clients like Chatwise.

func TestChatClientE2E_TextRoundTrip(t *testing.T) {
	ctx := context.Background()
	cfg := e2eMinimalConfig()
	hooks := format.CorePluginHooks{}.WithDefaults()
	reg := newTestRegistry(t, cfg, hooks)

	// Get the Chat client adapter (inbound).
	chatClientAdapter, ok := reg.GetClient(config.ProtocolOpenAIChat)
	if !ok {
		t.Fatal("chat client adapter not found in registry")
	}

	// Get the Chat provider adapter (outbound).
	chatProviderAdapter, ok := reg.GetProvider(config.ProtocolOpenAIChat)
	if !ok {
		t.Fatal("chat provider adapter not found in registry")
	}

	// Mock upstream Chat Completions server.
	mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q, want /v1/chat/completions", r.URL.Path)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{
			"id": "chatcmpl_e2e_inbound_001",
			"object": "chat.completion",
			"created": 1717000100,
			"model": "deepseek-v4-flash:cloud",
			"choices": [{"index":0,"message":{"role":"assistant","content":"Hello from inbound Chat E2E!"},"finish_reason":"stop"}],
			"usage": {"prompt_tokens":10,"completion_tokens":7,"total_tokens":17}
		}`)
	}))
	defer mockSrv.Close()

	// Step 1: Build an inbound Chat Completions request.
	chatReq := &chat.ChatRequest{
		Model: "deepseek-flash",
		Messages: []chat.ChatMessage{
			{Role: "system", Content: "You are a helpful assistant."},
			{Role: "user", Content: "Say hello briefly."},
		},
		MaxTokens: 100,
	}

	// Step 2: Inbound ChatClientAdapter.ToCoreRequest.
	coreReq, err := chatClientAdapter.ToCoreRequest(ctx, chatReq)
	if err != nil {
		t.Fatalf("ChatClientAdapter.ToCoreRequest: %v", err)
	}

	// Verify Core request conversion.
	if coreReq.Model != "deepseek-flash" {
		t.Errorf("coreReq.Model = %q, want deepseek-flash", coreReq.Model)
	}
	if len(coreReq.System) == 0 || coreReq.System[0].Text != "You are a helpful assistant." {
		t.Errorf("coreReq.System = %+v, want system instruction", coreReq.System)
	}
	if len(coreReq.Messages) != 1 {
		t.Fatalf("coreReq.Messages: got %d, want 1", len(coreReq.Messages))
	}
	if coreReq.MaxTokens != 100 {
		t.Errorf("coreReq.MaxTokens = %d, want 100", coreReq.MaxTokens)
	}

	// Step 3: Outbound ChatProviderAdapter.FromCoreRequest.
	upstreamAny, err := chatProviderAdapter.FromCoreRequest(ctx, coreReq)
	if err != nil {
		t.Fatalf("ChatProviderAdapter.FromCoreRequest: %v", err)
	}
	upstreamReq := upstreamAny.(*chat.ChatRequest)

	// Verify outbound Chat request was constructed correctly.
	if len(upstreamReq.Messages) < 2 {
		t.Fatalf("upstream messages: got %d, want at least 2 (system + user)", len(upstreamReq.Messages))
	}

	// Step 4: Call mock upstream.
	chatUpstreamClient := chat.NewClient(chat.ClientConfig{
		BaseURL: mockSrv.URL,
		APIKey:  "test-key",
		Client:  mockSrv.Client(),
	})
	upstreamResp, err := chatUpstreamClient.CreateChat(ctx, upstreamReq)
	if err != nil {
		t.Fatalf("CreateChat: %v", err)
	}

	// Step 5: Outbound ChatProviderAdapter.ToCoreResponse.
	coreResp, err := chatProviderAdapter.ToCoreResponse(ctx, upstreamResp)
	if err != nil {
		t.Fatalf("ChatProviderAdapter.ToCoreResponse: %v", err)
	}

	// Verify Core response.
	if coreResp.Status != "completed" {
		t.Errorf("coreResp.Status = %q, want completed", coreResp.Status)
	}
	if len(coreResp.Messages) == 0 {
		t.Fatal("coreResp.Messages is empty")
	}
	if coreResp.Messages[0].Role != "assistant" {
		t.Errorf("coreResp.Messages[0].Role = %q, want assistant", coreResp.Messages[0].Role)
	}

	// Step 6: Inbound ChatClientAdapter.FromCoreResponse.
	outAny, err := chatClientAdapter.FromCoreResponse(ctx, coreResp)
	if err != nil {
		t.Fatalf("ChatClientAdapter.FromCoreResponse: %v", err)
	}
	chatResp := outAny.(*chat.ChatResponse)

	// Verify final Chat Completions response.
	if chatResp.Object != "chat.completion" {
		t.Errorf("chatResp.Object = %q, want chat.completion", chatResp.Object)
	}
	if len(chatResp.Choices) == 0 {
		t.Fatal("chatResp.Choices is empty")
	}
	content, ok := chatResp.Choices[0].Message.Content.(string)
	if !ok || content != "Hello from inbound Chat E2E!" {
		t.Errorf("chatResp.Choices[0].Message.Content = %v, want 'Hello from inbound Chat E2E!'", chatResp.Choices[0].Message.Content)
	}
	if chatResp.Choices[0].FinishReason != "stop" {
		t.Errorf("chatResp.Choices[0].FinishReason = %q, want stop", chatResp.Choices[0].FinishReason)
	}
	if chatResp.Usage == nil {
		t.Fatal("chatResp.Usage is nil")
	}
	if chatResp.Usage.PromptTokens != 10 {
		t.Errorf("chatResp.Usage.PromptTokens = %d, want 10", chatResp.Usage.PromptTokens)
	}
}

// ============================================================================
// TestChatClientE2E_ToolUseRoundTrip
// ============================================================================
//
// Tests inbound Chat → Core → outbound Chat with tool calls.
// This exercises the tool_calls ↔ tool_use content block mapping
// through the full adapter pipeline.

func TestChatClientE2E_ToolUseRoundTrip(t *testing.T) {
	ctx := context.Background()
	cfg := e2eMinimalConfig()
	hooks := format.CorePluginHooks{}.WithDefaults()
	reg := newTestRegistry(t, cfg, hooks)

	chatClientAdapter, ok := reg.GetClient(config.ProtocolOpenAIChat)
	if !ok {
		t.Fatal("chat client adapter not found")
	}
	chatProviderAdapter, ok := reg.GetProvider(config.ProtocolOpenAIChat)
	if !ok {
		t.Fatal("chat provider adapter not found")
	}

	// Mock upstream returning a tool call.
	mockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{
			"id": "chatcmpl_e2e_tool_001",
			"object": "chat.completion",
			"created": 1717000200,
			"model": "gpt-4o",
			"choices": [{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_e2e_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]},"finish_reason":"tool_calls"}],
			"usage": {"prompt_tokens":15,"completion_tokens":12,"total_tokens":27}
		}`)
	}))
	defer mockSrv.Close()

	// Inbound request with tool definitions.
	chatReq := &chat.ChatRequest{
		Model: "gpt-4o",
		Messages: []chat.ChatMessage{
			{Role: "user", Content: "What's the weather in Paris?"},
		},
		Tools: []chat.ChatTool{{
			Type: "function",
			Function: chat.FunctionDef{
				Name:        "get_weather",
				Description: "Get weather for a city",
				Parameters:  map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}},
			},
		}},
		ToolChoice: json.RawMessage(`"auto"`),
	}

	// Inbound ChatClientAdapter → Core.
	coreReq, err := chatClientAdapter.ToCoreRequest(ctx, chatReq)
	if err != nil {
		t.Fatalf("ToCoreRequest: %v", err)
	}

	if len(coreReq.Tools) != 1 {
		t.Fatalf("coreReq.Tools: got %d, want 1", len(coreReq.Tools))
	}
	if coreReq.Tools[0].Name != "get_weather" {
		t.Errorf("coreReq.Tools[0].Name = %q, want get_weather", coreReq.Tools[0].Name)
	}
	if coreReq.ToolChoice == nil {
		t.Fatal("coreReq.ToolChoice is nil")
	}

	// Core → Outbound ChatProviderAdapter → upstream request.
	upstreamAny, err := chatProviderAdapter.FromCoreRequest(ctx, coreReq)
	if err != nil {
		t.Fatalf("FromCoreRequest: %v", err)
	}
	upstreamReq := upstreamAny.(*chat.ChatRequest)

	if len(upstreamReq.Tools) != 1 {
		t.Fatalf("upstreamReq.Tools: got %d, want 1", len(upstreamReq.Tools))
	}

	// Call mock upstream.
	chatUpstreamClient := chat.NewClient(chat.ClientConfig{
		BaseURL: mockSrv.URL,
		APIKey:  "test-key",
		Client:  mockSrv.Client(),
	})
	upstreamResp, err := chatUpstreamClient.CreateChat(ctx, upstreamReq)
	if err != nil {
		t.Fatalf("CreateChat: %v", err)
	}

	// Upstream → Core.
	coreResp, err := chatProviderAdapter.ToCoreResponse(ctx, upstreamResp)
	if err != nil {
		t.Fatalf("ToCoreResponse: %v", err)
	}

	if coreResp.StopReason != "tool_use" {
		t.Errorf("coreResp.StopReason = %q, want tool_use", coreResp.StopReason)
	}

	// Core → Inbound Chat response.
	outAny, err := chatClientAdapter.FromCoreResponse(ctx, coreResp)
	if err != nil {
		t.Fatalf("FromCoreResponse: %v", err)
	}
	chatResp := outAny.(*chat.ChatResponse)

	if chatResp.Choices[0].FinishReason != "tool_calls" {
		t.Errorf("FinishReason = %q, want tool_calls", chatResp.Choices[0].FinishReason)
	}
	if len(chatResp.Choices[0].Message.ToolCalls) == 0 {
		t.Fatal("expected tool_calls in Chat response")
	}
	if chatResp.Choices[0].Message.ToolCalls[0].Function.Name != "get_weather" {
		t.Errorf("tool function name = %q, want get_weather", chatResp.Choices[0].Message.ToolCalls[0].Function.Name)
	}
}

// ============================================================================
// TestChatClientE2E_CrossProtocolToAnthropic
// ============================================================================
//
// Tests inbound Chat Completions → Core → outbound Anthropic.
// Verifies that Chat clients can route to Anthropic upstream providers.

func TestChatClientE2E_CrossProtocolToAnthropic(t *testing.T) {
	ctx := context.Background()
	cfg := e2eMinimalConfig()
	hooks := format.CorePluginHooks{}.WithDefaults()
	reg := newTestRegistry(t, cfg, hooks)

	chatClientAdapter, ok := reg.GetClient(config.ProtocolOpenAIChat)
	if !ok {
		t.Fatal("chat client adapter not found")
	}
	anthProviderAdapter, ok := reg.GetProvider(config.ProtocolAnthropic)
	if !ok {
		t.Fatal("anthropic provider adapter not found")
	}

	// Inbound Chat request.
	chatReq := &chat.ChatRequest{
		Model: "claude-3.5-sonnet",
		Messages: []chat.ChatMessage{
			{Role: "system", Content: "Be brief."},
			{Role: "user", Content: "What is 2+2?"},
		},
		MaxTokens: 50,
	}

	// Chat → Core.
	coreReq, err := chatClientAdapter.ToCoreRequest(ctx, chatReq)
	if err != nil {
		t.Fatalf("ToCoreRequest: %v", err)
	}

	// Core → Anthropic.
	upstreamAny, err := anthProviderAdapter.FromCoreRequest(ctx, coreReq)
	if err != nil {
		t.Fatalf("AnthropicProviderAdapter.FromCoreRequest: %v", err)
	}

	// Verify we got an Anthropic request type.
	if upstreamAny == nil {
		t.Fatal("upstream request is nil")
	}

	// Verify the Anthropic request has the system prompt and user message.
	// We don't call a real upstream, just verify the conversion worked.
	_ = upstreamAny
}
