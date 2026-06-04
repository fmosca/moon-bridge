//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
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
// Exercises the full inbound ChatClientAdapter → Core → ChatProviderAdapter
// round-trip. When TEST_OPENAI_API_KEY is set, runs against a real upstream.

func TestChatClientE2E_TextRoundTrip(t *testing.T) {
	if apiKey := os.Getenv("TEST_OPENAI_API_KEY"); apiKey != "" {
		t.Run("real", func(t *testing.T) { testChatClientRealRoundTrip(t, apiKey) })
		return
	}

	ctx := context.Background()
	cfg := e2eMinimalConfig()
	hooks := format.CorePluginHooks{}.WithDefaults()
	reg := newTestRegistry(t, cfg, hooks)

	chatClientAdapter, ok := reg.GetClient(config.ProtocolOpenAIChat)
	if !ok {
		t.Fatal("chat client adapter not found in registry")
	}
	chatProviderAdapter, ok := reg.GetProvider(config.ProtocolOpenAIChat)
	if !ok {
		t.Fatal("chat provider adapter not found in registry")
	}

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

	chatReq := &chat.ChatRequest{
		Model: "deepseek-flash",
		Messages: []chat.ChatMessage{
			{Role: "system", Content: "You are a helpful assistant."},
			{Role: "user", Content: "Say hello briefly."},
		},
		MaxTokens: 100,
	}

	coreReq, err := chatClientAdapter.ToCoreRequest(ctx, chatReq)
	if err != nil {
		t.Fatalf("ChatClientAdapter.ToCoreRequest: %v", err)
	}

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

	upstreamAny, err := chatProviderAdapter.FromCoreRequest(ctx, coreReq)
	if err != nil {
		t.Fatalf("ChatProviderAdapter.FromCoreRequest: %v", err)
	}
	upstreamReq := upstreamAny.(*chat.ChatRequest)

	if len(upstreamReq.Messages) < 2 {
		t.Fatalf("upstream messages: got %d, want at least 2 (system + user)", len(upstreamReq.Messages))
	}

	chatUpstreamClient := chat.NewClient(chat.ClientConfig{
		BaseURL: mockSrv.URL,
		APIKey:  "test-key",
		Client:  mockSrv.Client(),
	})
	upstreamResp, err := chatUpstreamClient.CreateChat(ctx, upstreamReq)
	if err != nil {
		t.Fatalf("CreateChat: %v", err)
	}

	coreResp, err := chatProviderAdapter.ToCoreResponse(ctx, upstreamResp)
	if err != nil {
		t.Fatalf("ChatProviderAdapter.ToCoreResponse: %v", err)
	}

	if coreResp.Status != "completed" {
		t.Errorf("coreResp.Status = %q, want completed", coreResp.Status)
	}
	if len(coreResp.Messages) == 0 {
		t.Fatal("coreResp.Messages is empty")
	}
	if coreResp.Messages[0].Role != "assistant" {
		t.Errorf("coreResp.Messages[0].Role = %q, want assistant", coreResp.Messages[0].Role)
	}

	outAny, err := chatClientAdapter.FromCoreResponse(ctx, coreResp)
	if err != nil {
		t.Fatalf("ChatClientAdapter.FromCoreResponse: %v", err)
	}
	chatResp := outAny.(*chat.ChatResponse)

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
// Exercises the tool_calls ↔ tool_use content block mapping
// through the full adapter pipeline.

func TestChatClientE2E_ToolUseRoundTrip(t *testing.T) {
	if apiKey := os.Getenv("TEST_OPENAI_API_KEY"); apiKey != "" {
		t.Run("real", func(t *testing.T) { testChatClientRealToolRoundTrip(t, apiKey) })
		return
	}

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

	upstreamAny, err := chatProviderAdapter.FromCoreRequest(ctx, coreReq)
	if err != nil {
		t.Fatalf("FromCoreRequest: %v", err)
	}
	upstreamReq := upstreamAny.(*chat.ChatRequest)

	if len(upstreamReq.Tools) != 1 {
		t.Fatalf("upstreamReq.Tools: got %d, want 1", len(upstreamReq.Tools))
	}

	chatUpstreamClient := chat.NewClient(chat.ClientConfig{
		BaseURL: mockSrv.URL,
		APIKey:  "test-key",
		Client:  mockSrv.Client(),
	})
	upstreamResp, err := chatUpstreamClient.CreateChat(ctx, upstreamReq)
	if err != nil {
		t.Fatalf("CreateChat: %v", err)
	}

	coreResp, err := chatProviderAdapter.ToCoreResponse(ctx, upstreamResp)
	if err != nil {
		t.Fatalf("ToCoreResponse: %v", err)
	}

	if coreResp.StopReason != "tool_use" {
		t.Errorf("coreResp.StopReason = %q, want tool_use", coreResp.StopReason)
	}

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
	if apiKey := os.Getenv("TEST_ANTHROPIC_API_KEY"); apiKey != "" {
		t.Skip("Real mode: testAnthropicRealClient covers the Anthropic path; skip mock test")
	}

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

	chatReq := &chat.ChatRequest{
		Model: "claude-3.5-sonnet",
		Messages: []chat.ChatMessage{
			{Role: "system", Content: "Be brief."},
			{Role: "user", Content: "What is 2+2?"},
		},
		MaxTokens: 50,
	}

	coreReq, err := chatClientAdapter.ToCoreRequest(ctx, chatReq)
	if err != nil {
		t.Fatalf("ToCoreRequest: %v", err)
	}

	upstreamAny, err := anthProviderAdapter.FromCoreRequest(ctx, coreReq)
	if err != nil {
		t.Fatalf("AnthropicProviderAdapter.FromCoreRequest: %v", err)
	}

	if upstreamAny == nil {
		t.Fatal("upstream request is nil")
	}
	_ = upstreamAny
}

// ============================================================================
// Real upstream helpers (opt-in via TEST_OPENAI_API_KEY)
// ============================================================================

// testChatClientRealRoundTrip exercises the full adapter chain
// (ChatClientAdapter → ChatProviderAdapter) against a real upstream.
func testChatClientRealRoundTrip(t *testing.T, apiKey string) {
	t.Helper()

	ctx := context.Background()
	cfg := e2eMinimalConfig()
	hooks := format.CorePluginHooks{}.WithDefaults()
	reg := newTestRegistry(t, cfg, hooks)

	chatClientAdapter, _ := reg.GetClient(config.ProtocolOpenAIChat)
	chatProviderAdapter, _ := reg.GetProvider(config.ProtocolOpenAIChat)

	model := realChatModel()
	baseURL := os.Getenv("TEST_OPENAI_BASE_URL")

	chatReq := &chat.ChatRequest{
		Model: model,
		Messages: []chat.ChatMessage{
			{Role: "user", Content: "say hello in exactly one word"},
		},
		MaxTokens: 50,
	}

	coreReq, err := chatClientAdapter.ToCoreRequest(ctx, chatReq)
	if err != nil {
		t.Fatalf("ToCoreRequest: %v", err)
	}

	coreReq.Model = model

	upstreamAny, err := chatProviderAdapter.FromCoreRequest(ctx, coreReq)
	if err != nil {
		t.Fatalf("FromCoreRequest: %v", err)
	}
	upstreamReq := upstreamAny.(*chat.ChatRequest)

	chatClient := chat.NewClient(chat.ClientConfig{
		BaseURL: baseURL,
		APIKey:  apiKey,
	})
	upstreamResp, err := chatClient.CreateChat(ctx, upstreamReq)
	if err != nil {
		t.Fatalf("CreateChat (real): %v", err)
	}

	coreResp, err := chatProviderAdapter.ToCoreResponse(ctx, upstreamResp)
	if err != nil {
		t.Fatalf("ToCoreResponse: %v", err)
	}

	outAny, err := chatClientAdapter.FromCoreResponse(ctx, coreResp)
	if err != nil {
		t.Fatalf("FromCoreResponse: %v", err)
	}
	chatResp := outAny.(*chat.ChatResponse)

	if chatResp.Object != "chat.completion" {
		t.Errorf("Object = %q, want chat.completion", chatResp.Object)
	}
	if len(chatResp.Choices) == 0 {
		t.Fatal("response has no choices")
	}
	content, ok := chatResp.Choices[0].Message.Content.(string)
	if !ok || content == "" {
		t.Error("response content is empty or not a string")
	}
	t.Logf("Real inbound round-trip: model=%s id=%s text=%q", chatResp.Model, chatResp.ID, content)
}

// testChatClientRealToolRoundTrip exercises the tool-call path
// through the full adapter chain against a real upstream.
func testChatClientRealToolRoundTrip(t *testing.T, apiKey string) {
	t.Helper()

	ctx := context.Background()
	cfg := e2eMinimalConfig()
	hooks := format.CorePluginHooks{}.WithDefaults()
	reg := newTestRegistry(t, cfg, hooks)

	chatClientAdapter, _ := reg.GetClient(config.ProtocolOpenAIChat)
	chatProviderAdapter, _ := reg.GetProvider(config.ProtocolOpenAIChat)

	model := realChatModel()
	baseURL := os.Getenv("TEST_OPENAI_BASE_URL")

	chatReq := &chat.ChatRequest{
		Model: model,
		Messages: []chat.ChatMessage{
			{Role: "user", Content: "What is the weather in Paris?"},
		},
		Tools: []chat.ChatTool{{
			Type: "function",
			Function: chat.FunctionDef{
				Name:        "get_weather",
				Description: "Get the current weather for a city",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"city": map[string]any{"type": "string"},
					},
					"required": []string{"city"},
				},
			},
		}},
		ToolChoice: json.RawMessage(`"auto"`),
		MaxTokens:  100,
	}

	coreReq, err := chatClientAdapter.ToCoreRequest(ctx, chatReq)
	if err != nil {
		t.Fatalf("ToCoreRequest: %v", err)
	}

	coreReq.Model = model

	upstreamAny, err := chatProviderAdapter.FromCoreRequest(ctx, coreReq)
	if err != nil {
		t.Fatalf("FromCoreRequest: %v", err)
	}
	upstreamReq := upstreamAny.(*chat.ChatRequest)

	chatClient := chat.NewClient(chat.ClientConfig{
		BaseURL: baseURL,
		APIKey:  apiKey,
	})
	upstreamResp, err := chatClient.CreateChat(ctx, upstreamReq)
	if err != nil {
		t.Fatalf("CreateChat (real): %v", err)
	}

	coreResp, err := chatProviderAdapter.ToCoreResponse(ctx, upstreamResp)
	if err != nil {
		t.Fatalf("ToCoreResponse: %v", err)
	}

	outAny, err := chatClientAdapter.FromCoreResponse(ctx, coreResp)
	if err != nil {
		t.Fatalf("FromCoreResponse: %v", err)
	}
	chatResp := outAny.(*chat.ChatResponse)

	if len(chatResp.Choices) == 0 {
		t.Fatal("response has no choices")
	}
	t.Logf("Real tool round-trip: model=%s finish=%q tools=%d",
		chatResp.Model, chatResp.Choices[0].FinishReason, len(chatResp.Choices[0].Message.ToolCalls))
}
