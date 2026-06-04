// Package chat implements the OpenAI Chat Completions adapters for MoonBridge.
//
// ChatClientAdapter converts between the OpenAI Chat Completions API DTOs
// and the Core intermediate format. It implements the inbound (client) side:
//   - format.ClientAdapter:  ToCoreRequest / FromCoreResponse
//   - format.ClientStreamAdapter: FromCoreStream
//
// This enables clients that speak the Chat Completions protocol (e.g. Chatwise,
// Continue, LangChain) to use Moon Bridge as their OpenAI-compatible backend,
// with full cross-protocol routing to any upstream provider.
package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"moonbridge/internal/format"
)

// ============================================================================
// ChatClientAdapter
// ============================================================================

// ChatClientAdapter converts between OpenAI Chat Completions DTOs and
// the Core intermediate format. It serves as the inbound (client) adapter
// for clients that speak the Chat Completions protocol.
//
// It implements format.ClientAdapter and format.ClientStreamAdapter.
type ChatClientAdapter struct {
	hooks format.CorePluginHooks
}

// NewChatClientAdapter creates a new ChatClientAdapter.
func NewChatClientAdapter(hooks format.CorePluginHooks) *ChatClientAdapter {
	return &ChatClientAdapter{
		hooks: hooks.WithDefaults(),
	}
}

// ClientProtocol returns the inbound protocol identifier.
func (a *ChatClientAdapter) ClientProtocol() string {
	return "openai-chat"
}

// ============================================================================
// ToCoreRequest — ChatRequest → CoreRequest
// ============================================================================

// ToCoreRequest converts an inbound Chat Completions request into a CoreRequest.
//
// Supported mappings:
//   - Model, Temperature, TopP, MaxTokens, Stream, Stop → direct copy
//   - Messages (system/user/assistant/tool) → CoreMessages + System
//   - Tools → CoreTool (function → name/desc/schema)
//   - ToolChoice → CoreToolChoice (with raw JSON preserved)
func (a *ChatClientAdapter) ToCoreRequest(ctx context.Context, req any) (*format.CoreRequest, error) {
	chatReq, ok := req.(*ChatRequest)
	if !ok {
		// Accept non-pointer value as well.
		direct, ok2 := req.(ChatRequest)
		if !ok2 {
			return nil, fmt.Errorf("unexpected request type %T; expected *ChatRequest", req)
		}
		chatReq = &direct
	}

	// Allow plugins to mutate the request before conversion.
	a.hooks.MutateCoreRequest(ctx, nil) // no-op if hooks not set

	// Build CoreRequest.
	coreReq := &format.CoreRequest{
		Model:       chatReq.Model,
		Stream:      chatReq.Stream,
		Temperature: chatReq.Temperature,
		TopP:        chatReq.TopP,
	}

	// MaxTokens.
	if chatReq.MaxTokens > 0 {
		coreReq.MaxTokens = chatReq.MaxTokens
	}

	// Stop sequences.
	if len(chatReq.Stop) > 0 {
		coreReq.StopSequences = chatReq.Stop
	}

	// Parse messages: separate system messages from conversation messages.
	var systemBlocks []format.CoreContentBlock
	var messages []format.CoreMessage

	for _, msg := range chatReq.Messages {
		switch msg.Role {
		case "system":
			// System messages go to the system instruction, not the conversation.
			if text, ok := msg.Content.(string); ok && text != "" {
				systemBlocks = append(systemBlocks, format.CoreContentBlock{
					Type: "text",
					Text: text,
				})
			} else if parts := contentPartsFromAny(msg.Content); len(parts) > 0 {
				systemBlocks = append(systemBlocks, parts...)
			}
		case "tool":
			// Tool result message.
			toolResultBlocks := contentBlocksFromAny(msg.Content)
			messages = append(messages, format.CoreMessage{
				Role: "user", // Tool results are mapped to user role in Core format.
				Content: []format.CoreContentBlock{{
					Type:              "tool_result",
					ToolUseID:          msg.ToolCallID,
					ToolResultContent:  toolResultBlocks,
				}},
			})
		default:
			// user or assistant.
			coreMsg := chatMessageToCoreMessage(msg)
			messages = append(messages, coreMsg)
		}
	}

	coreReq.System = systemBlocks
	coreReq.Messages = messages

	// Tools.
	if len(chatReq.Tools) > 0 {
		coreReq.Tools = make([]format.CoreTool, 0, len(chatReq.Tools))
		for _, t := range chatReq.Tools {
			if t.Type != "function" && t.Type != "" {
				continue // skip non-function tools for now
			}
			coreReq.Tools = append(coreReq.Tools, format.CoreTool{
				Name:        t.Function.Name,
				Description: t.Function.Description,
				InputSchema: t.Function.Parameters,
			})
		}
	}

	// ToolChoice.
	if len(chatReq.ToolChoice) > 0 && string(chatReq.ToolChoice) != "null" {
		tc, err := convertChatToolChoice(chatReq.ToolChoice)
		if err != nil {
			return nil, fmt.Errorf("invalid tool_choice: %w", err)
		}
		coreReq.ToolChoice = tc
	}

	return coreReq, nil
}

// ============================================================================
// FromCoreResponse — CoreResponse → ChatResponse
// ============================================================================

// FromCoreResponse converts a CoreResponse into a Chat Completions response.
//
// Only the first CoreMessage is mapped to a Chat Choice (Chat Completions
// typically returns a single choice). Tool use blocks become tool_calls.
func (a *ChatClientAdapter) FromCoreResponse(ctx context.Context, resp *format.CoreResponse) (any, error) {
	if resp == nil {
		return nil, fmt.Errorf("chat client adapter: core response is nil")
	}

	// Handle error responses.
	if resp.Error != nil {
		return &ChatResponse{
			ID:     resp.ID,
			Object: "chat.completion",
			Model:  resp.Model,
			Choices: []Choice{{
				Index:        0,
				Message:      ChatMessage{Role: "assistant", Content: resp.Error.Message},
				FinishReason: "stop",
			}},
		}, nil
	}

	chatResp := &ChatResponse{
		ID:      resp.ID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   resp.Model,
	}

	// Map Core messages to Chat choices.
	if len(resp.Messages) > 0 {
		// Map the first message as the primary choice.
		msg := resp.Messages[0]
		chatMsg, finishReason := coreMessageToChatMessage(msg, resp.StopReason)
		chatResp.Choices = append(chatResp.Choices, Choice{
			Index:        0,
			Message:      chatMsg,
			FinishReason: finishReason,
		})
	} else {
		// No messages — return empty choice.
		chatResp.Choices = append(chatResp.Choices, Choice{
			Index:        0,
			Message:      ChatMessage{Role: "assistant", Content: ""},
			FinishReason: "stop",
		})
	}

	// Usage.
	chatResp.Usage = coreUsageToChatUsage(resp.Usage)

	return chatResp, nil
}

// ============================================================================
// FromCoreStream — CoreStreamEvent → Chat SSE
// ============================================================================

// ChatStreamResult wraps the data needed for streaming Chat Completions
// responses. It implements the io.Writer interface for writing SSE data to
// an http.ResponseWriter.
type ChatStreamResult struct {
	writer  http.ResponseWriter
	flusher http.Flusher
	id      string
	model   string
	created int64
	sentRole bool
}

// FromCoreStream consumes a channel of CoreStreamEvent and produces
// Chat Completions SSE stream output. The returned value is a
// *ChatStreamResult that writes SSE data as it receives core events.
//
// The caller is responsible for calling the result's Write method or
// iterating the core events channel directly. However, the simpler
// integration path is for the adapter_dispatch layer to call
// writeChatStreamSSE which handles the SSE framing.
func (a *ChatClientAdapter) FromCoreStream(ctx context.Context, req *format.CoreRequest, events <-chan format.CoreStreamEvent) (any, error) {
	// Return the events channel directly — the dispatch layer will
	// iterate it and write Chat-format SSE events.
	// We wrap it in a struct that carries enough context to produce
	// properly formatted Chat stream chunks.
	result := &ChatStreamContext{
		events:  events,
		id:      generateChatID(),
		model:   "",
		created: time.Now().Unix(),
	}
	if req != nil {
		result.model = req.Model
	}
	return result, nil
}

// ChatStreamContext carries context for converting Core stream events
// to Chat Completions SSE format.
type ChatStreamContext struct {
	events  <-chan format.CoreStreamEvent
	id      string
	model   string
	created int64
}

// NewChatStreamContext creates a ChatStreamContext for streaming Chat completions.
func NewChatStreamContext(id, model string, created int64, events <-chan format.CoreStreamEvent) *ChatStreamContext {
	return &ChatStreamContext{
		events:  events,
		id:      id,
		model:   model,
		created: created,
	}
}

// Events returns the underlying core stream events channel.
func (c *ChatStreamContext) Events() <-chan format.CoreStreamEvent {
	return c.events
}

// ID returns the stream response ID.
func (c *ChatStreamContext) ID() string {
	return c.id
}

// Model returns the model name for the stream.
func (c *ChatStreamContext) Model() string {
	return c.model
}

// Created returns the Unix timestamp for the stream.
func (c *ChatStreamContext) Created() int64 {
	return c.created
}

// ============================================================================
// Conversion helpers: Chat → Core
// ============================================================================

// chatMessageToCoreMessage converts a ChatMessage to a CoreMessage.
func chatMessageToCoreMessage(msg ChatMessage) format.CoreMessage {
	role := msg.Role
	if role == "" {
		role = "user"
	}

	var content []format.CoreContentBlock

	// Handle tool calls on assistant messages.
	if len(msg.ToolCalls) > 0 {
		// Add any text content first.
		if textBlocks := contentBlocksFromAny(msg.Content); len(textBlocks) > 0 {
			content = append(content, textBlocks...)
		}
		// Add tool call blocks.
		for _, tc := range msg.ToolCalls {
			content = append(content, format.CoreContentBlock{
				Type:      "tool_use",
				ToolUseID: tc.ID,
				ToolName:  tc.Function.Name,
				ToolInput: unquoteArguments(tc.Function.Arguments),
			})
		}
	} else {
		content = contentBlocksFromAny(msg.Content)
	}

	// Handle reasoning_content (DeepSeek etc.).
	if msg.ReasoningContent != "" {
		reasoning := format.CoreContentBlock{
			Type:          "reasoning",
			ReasoningText: msg.ReasoningContent,
		}
		content = append([]format.CoreContentBlock{reasoning}, content...)
	}

	return format.CoreMessage{
		Role:    role,
		Content: content,
	}
}

// contentBlocksFromAny converts a Chat content field (string, []ContentPart,
// or []any from JSON) to CoreContentBlocks.
func contentBlocksFromAny(content any) []format.CoreContentBlock {
	if content == nil {
		return nil
	}
	switch v := content.(type) {
	case string:
		if v == "" {
			return nil
		}
		return []format.CoreContentBlock{{Type: "text", Text: v}}
	case []any:
		var blocks []format.CoreContentBlock
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				blocks = append(blocks, contentPartMapToCoreBlock(m))
			}
		}
		return blocks
	case []ContentPart:
		var blocks []format.CoreContentBlock
		for _, p := range v {
			switch p.Type {
			case "text":
				blocks = append(blocks, format.CoreContentBlock{Type: "text", Text: p.Text})
			case "image_url":
				if p.ImageURL != nil {
					blocks = append(blocks, format.CoreContentBlock{Type: "image", ImageData: p.ImageURL.URL, MediaType: imageMediaType(p.ImageURL.URL)})
			}
			}
		}
		return blocks
	default:
		return nil
	}
}

// contentPartsFromAny converts content to ContentPart slice for system messages.
func contentPartsFromAny(content any) []format.CoreContentBlock {
	return contentBlocksFromAny(content)
}

// contentPartMapToCoreBlock converts a raw map to a CoreContentBlock.
func contentPartMapToCoreBlock(m map[string]any) format.CoreContentBlock {
	typ, _ := m["type"].(string)
	switch typ {
	case "text":
		text, _ := m["text"].(string)
		return format.CoreContentBlock{Type: "text", Text: text}
	case "image_url":
		imageURL, ok := m["image_url"].(map[string]any)
		if !ok {
			return format.CoreContentBlock{Type: "text"}
		}
		url, _ := imageURL["url"].(string)
		return format.CoreContentBlock{Type: "image", ImageData: url, MediaType: imageMediaType(url)}
	default:
		text, _ := m["text"].(string)
		if text != "" {
			return format.CoreContentBlock{Type: "text", Text: text}
		}
		return format.CoreContentBlock{Type: "text"}
	}
}

// imageMediaType infers the MIME type from an image URL/data URI.
func imageMediaType(url string) string {
	if strings.HasPrefix(url, "data:image/") {
		end := strings.Index(url, ";")
		if end > len("data:image/") {
			return "image/" + url[len("data:image/"):end]
		}
	}
	// Default for URL-based images.
	return "image/png"
}

// convertChatToolChoice converts a Chat API tool_choice JSON value to
// CoreToolChoice.
func convertChatToolChoice(raw json.RawMessage) (*format.CoreToolChoice, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}

	// Try string value first ("auto", "none", "required").
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return &format.CoreToolChoice{Mode: s, Raw: raw}, nil
	}

	// Try object value ({"type":"function","function":{"name":"..."}}).
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err == nil {
		mode := "auto"
		name := ""
		if t, ok := obj["type"].(string); ok && t == "function" {
			if fn, ok := obj["function"].(map[string]any); ok {
				if n, ok := fn["name"].(string); ok {
					name = n
					mode = "any"
				}
			}
		}
		return &format.CoreToolChoice{Mode: mode, Name: name, Raw: raw}, nil
	}

	return &format.CoreToolChoice{Mode: "auto", Raw: raw}, nil
}

// ============================================================================
// Conversion helpers: Core → Chat
// ============================================================================

// coreMessageToChatMessage converts a CoreMessage to a ChatMessage and
// determines the finish_reason.
func coreMessageToChatMessage(msg format.CoreMessage, stopReason string) (ChatMessage, string) {
	var textParts []any
	var toolCalls []ToolCall

	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			if block.Text != "" {
				textParts = append(textParts, block.Text)
			}
		case "reasoning":
			// Reasoning blocks are not exposed in Chat Completions by default.
			// They could be mapped to reasoning_content if needed.
			// For now, skip them in the text output.
		case "tool_use":
			toolCalls = append(toolCalls, ToolCall{
				ID:   block.ToolUseID,
				Type: "function",
				Function: ToolCallFunc{
					Name:      block.ToolName,
					Arguments: block.ToolInput,
			},
			})
		case "tool_result":
			// Tool results should not appear in assistant messages.
		default:
			if block.Text != "" {
				textParts = append(textParts, block.Text)
			}
		}
	}

	// Build content: concatenate text parts as a single string.
	var content any
	if len(textParts) == 1 {
		if s, ok := textParts[0].(string); ok {
			content = s
		}
	} else if len(textParts) > 1 {
		var sb strings.Builder
		for _, p := range textParts {
			if s, ok := p.(string); ok {
				sb.WriteString(s)
			}
		}
		content = sb.String()
	}

	chatMsg := ChatMessage{
		Role:      "assistant",
		Content:   content,
		ToolCalls: toolCalls,
	}

	// Determine finish_reason.
	finishReason := mapStopReasonToFinish(stopReason)
	if len(toolCalls) > 0 && finishReason == "stop" {
		finishReason = "tool_calls"
	}

	return chatMsg, finishReason
}

// mapStopReasonToFinish maps Core stop_reason to Chat finish_reason.
func mapStopReasonToFinish(stopReason string) string {
	switch stopReason {
	case "end_turn":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "content_filter":
		return "content_filter"
	case "stop_sequence":
		return "stop"
	default:
		if stopReason != "" {
			return stopReason
		}
		return "stop"
	}
}

// coreUsageToChatUsage maps CoreUsage to Chat Usage.
func coreUsageToChatUsage(u format.CoreUsage) *Usage {
	if u.InputTokens == 0 && u.OutputTokens == 0 {
		return nil
	}
	usage := &Usage{
		PromptTokens:     u.InputTokens,
		CompletionTokens:  u.OutputTokens,
		TotalTokens:      u.InputTokens + u.OutputTokens,
	}
	if u.CachedInputTokens > 0 {
		usage.PromptTokensDetails = &PromptTokensDetails{
			CachedTokens: u.CachedInputTokens,
		}
	}
	return usage
}

// generateChatID creates a unique ID for Chat Completion responses.
func generateChatID() string {
	return fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
}

// WriteChatStreamSSE writes Chat Completions SSE events from a
// chatStreamContext to the given http.ResponseWriter.
//
// This is the integration point called by the adapter_dispatch layer
// to actually stream data to the HTTP client.
func WriteChatStreamSSE(w http.ResponseWriter, ctx *ChatStreamContext) {
	log := slog.Default().With("path", "chat-stream")

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	flusher, ok := w.(http.Flusher)
	if !ok {
		log.Warn("response writer does not support flushing")
	}

	sentRole := false
	var finishReason string

	for ev := range ctx.events {
		switch ev.Type {
		case format.CoreEventCreated, format.CoreEventInProgress:
			// Send initial role chunk if not yet sent.
			if !sentRole {
				sentRole = true
				writeChatChunk(w, ctx.id, ctx.model, ctx.created, 0, Delta{Role: "assistant"}, "", nil, nil)
				if flusher != nil {
					flusher.Flush()
				}
			}

		case format.CoreTextDelta:
			if !sentRole {
				sentRole = true
				writeChatChunk(w, ctx.id, ctx.model, ctx.created, 0, Delta{Role: "assistant"}, "", nil, nil)
				if flusher != nil {
					flusher.Flush()
			}
			}
			writeChatChunk(w, ctx.id, ctx.model, ctx.created, 0, Delta{Content: ev.Delta}, "", nil, nil)
			if flusher != nil {
				flusher.Flush()
			}

		case format.CoreToolCallArgsDelta:
			if !sentRole {
				sentRole = true
				writeChatChunk(w, ctx.id, ctx.model, ctx.created, 0, Delta{Role: "assistant"}, "", nil, nil)
				if flusher != nil {
					flusher.Flush()
			}
			}
			// Build tool call delta.
			var tcIndex *int
			if ev.Index > 0 {
				i := ev.Index
				tcIndex = &i
			}
			tc := ToolCall{
				Index:    tcIndex,
				ID:       "",
				Type:     "function",
				Function: ToolCallFunc{Name: "", Arguments: json.RawMessage(ev.Delta)},
			}
			if ev.ContentBlock != nil {
				tc.ID = ev.ContentBlock.ToolUseID
				tc.Function.Name = ev.ContentBlock.ToolName
			}
			writeChatChunk(w, ctx.id, ctx.model, ctx.created, 0, Delta{ToolCalls: []ToolCall{tc}}, "", nil, nil)
			if flusher != nil {
				flusher.Flush()
			}

		case format.CoreToolCallArgsDone:
			// Tool call args complete — no explicit chunk needed,
			// but some clients expect a finish on the tool call.

		case format.CoreContentBlockStarted:
			if ev.ContentBlock != nil && ev.ContentBlock.Type == "tool_use" {
				if !sentRole {
					sentRole = true
					writeChatChunk(w, ctx.id, ctx.model, ctx.created, 0, Delta{Role: "assistant"}, "", nil, nil)
					if flusher != nil {
						flusher.Flush()
				}
			}
				// Send tool call header with ID and name.
				var tcIndex *int
				if ev.Index > 0 {
					i := ev.Index
					tcIndex = &i
				}
				tc := ToolCall{
					Index:    tcIndex,
					ID:       ev.ContentBlock.ToolUseID,
					Type:     "function",
					Function: ToolCallFunc{Name: ev.ContentBlock.ToolName},
			}
				writeChatChunk(w, ctx.id, ctx.model, ctx.created, 0, Delta{ToolCalls: []ToolCall{tc}}, "", nil, nil)
				if flusher != nil {
					flusher.Flush()
			}
			}

		case format.CoreContentBlockDone:
			if ev.ContentBlock != nil && ev.ContentBlock.Type == "tool_use" {
				finishReason = "tool_calls"
			}

		case format.CoreEventCompleted:
			if ev.StopReason != "" && finishReason == "" {
				finishReason = mapStopReasonToFinish(ev.StopReason)
			}
			if finishReason == "" {
				finishReason = "stop"
			}
			// Send final chunk with finish_reason and usage.
			var usage *Usage
			if ev.Usage != nil {
				usage = coreUsageToChatUsage(*ev.Usage)
			}
			if finishReason == "tool_calls" {
				writeChatChunk(w, ctx.id, ctx.model, ctx.created, 0, Delta{}, finishReason, usage, nil)
			} else {
				writeChatChunk(w, ctx.id, ctx.model, ctx.created, 0, Delta{}, finishReason, usage, nil)
			}
			if flusher != nil {
				flusher.Flush()
			}

		case format.CoreEventIncomplete, format.CoreEventFailed:
			if finishReason == "" {
				if ev.Type == format.CoreEventIncomplete {
					finishReason = "length"
				} else {
					finishReason = "stop"
				}
			}
			writeChatChunk(w, ctx.id, ctx.model, ctx.created, 0, Delta{}, finishReason, nil, nil)
			if flusher != nil {
				flusher.Flush()
			}

		case format.CorePing:
			// Ignore pings in Chat format.

		case format.CoreTextDone:
			// Text done — no explicit chunk needed in Chat format.
			if ev.StopReason != "" && finishReason == "" {
				finishReason = mapStopReasonToFinish(ev.StopReason)
			}

		default:
			// Skip unknown event types.
		}
	}

	// Send [DONE] marker.
	io.WriteString(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

// writeChatChunk writes a single Chat Completions streaming chunk as SSE.
func writeChatChunk(w io.Writer, id, model string, created int64, index int, delta Delta, finishReason string, usage *Usage, streamOptions *StreamOptions) {
	chunk := ChatStreamChunk{
		ID:      id,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   model,
		Choices: []StreamChoice{{
			Index:        index,
			Delta:        delta,
			FinishReason: finishReason,
		}},
	}
	if usage != nil {
		chunk.Usage = usage
	}
	data, err := json.Marshal(chunk)
	if err != nil {
		slog.Warn("chat stream: marshal chunk failed", "error", err)
		return
	}
	w.Write([]byte("data: "))
	w.Write(data)
	w.Write([]byte("\n\n"))
}

// WriteChatNonStreamResponse writes a non-streaming Chat Completions
// response to the http.ResponseWriter.
func WriteChatNonStreamResponse(w http.ResponseWriter, chatResp *ChatResponse) {
	w.Header().Set("Content-Type", "application/json")
	data, err := json.Marshal(chatResp)
	if err != nil {
		slog.Warn("chat response: marshal failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Write(data)
}
