// Package chat implements the OpenAI Chat Completions ProviderAdapter for MoonBridge.
//
// ChatProviderAdapter converts between Core format and OpenAI Chat Completions
// REST API DTOs. It implements format.ProviderAdapter (non-streaming) and
// format.ProviderStreamAdapter (streaming).
package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"moonbridge/internal/foundation/config"
	"moonbridge/internal/protocol/format"
)

// ============================================================================
// ChatProviderAdapter
// ============================================================================

// ChatProviderAdapter converts Core format requests/responses to/from
// the OpenAI Chat Completions API format.
//
// Clean room: no dependency on protocol-specific packages beyond chat/.
// Only references: config, format, and chat types.
type ChatProviderAdapter struct {
	cfg   config.Config
	client *Client
	hooks format.CorePluginHooks

	streamMu      sync.Mutex
	streamEvents  []ChatStreamChunk
}

// NewChatProviderAdapter creates a new ChatProviderAdapter.
//
// client is the HTTP client for Chat API calls. May be nil if the adapter
// is registered for type conversion only (dispatch layer manages the client).
func NewChatProviderAdapter(cfg config.Config, client *Client, hooks format.CorePluginHooks) *ChatProviderAdapter {
	return &ChatProviderAdapter{
		cfg:    cfg,
		client: client,
		hooks:  hooks.WithDefaults(),
	}
}

// ProviderProtocol returns "openai-chat".
func (a *ChatProviderAdapter) ProviderProtocol() string {
	return config.ProtocolOpenAIChat
}

// =========================================================================
// FromCoreRequest — CoreRequest → *ChatRequest
// =========================================================================

// FromCoreRequest converts a CoreRequest into a *ChatRequest.
//
// Conversion steps:
//  1. Call hooks.MutateCoreRequest (plugin modifications to CoreRequest)
//  2. Map CoreRequest fields to ChatRequest fields
//  3. System instruction, messages, tools, sampling params
func (a *ChatProviderAdapter) FromCoreRequest(ctx context.Context, req *format.CoreRequest) (any, error) {
	if req == nil {
		return nil, fmt.Errorf("chat adapter: core request is nil")
	}

	// Step 1: Allow plugins to mutate the CoreRequest before conversion.
	a.hooks.MutateCoreRequest(ctx, req)

	// Step 2: Build the Chat request.
	chatReq := &ChatRequest{
		Model:    req.Model,
		Messages: make([]ChatMessage, 0, len(req.Messages)+1),
	}

	// System instruction → first message with role "system".
	if len(req.System) > 0 {
		sysContent := a.toChatSystemContent(req.System)
		if sysContent != "" {
			chatReq.Messages = append(chatReq.Messages, ChatMessage{
				Role:    "system",
				Content: sysContent,
			})
		}
	}

	// Messages.
	for _, msg := range req.Messages {
		chatMsg := a.toChatMessage(msg)
		chatReq.Messages = append(chatReq.Messages, chatMsg)
	}

	// Sampling parameters.
	if req.Temperature != nil {
		chatReq.Temperature = req.Temperature
	}
	if req.TopP != nil {
		chatReq.TopP = req.TopP
	}
	if req.MaxTokens > 0 {
		chatReq.MaxTokens = req.MaxTokens
	} else if a.cfg.DefaultMaxTokens > 0 {
		chatReq.MaxTokens = a.cfg.DefaultMaxTokens
	}
	if len(req.StopSequences) > 0 {
		chatReq.Stop = req.StopSequences
	}

	// Tools.
	if len(req.Tools) > 0 {
		chatReq.Tools = make([]ChatTool, 0, len(req.Tools))
		for _, t := range req.Tools {
			chatReq.Tools = append(chatReq.Tools, ChatTool{
				Type: "function",
				Function: FunctionDef{
					Name:        t.Name,
					Description: t.Description,
					Parameters:  t.InputSchema,
				},
			})
		}
	}

	// ToolChoice.
	if req.ToolChoice != nil {
		if req.ToolChoice.Raw != nil {
			chatReq.ToolChoice = req.ToolChoice.Raw
		} else {
			chatReq.ToolChoice = a.toChatToolChoice(*req.ToolChoice)
		}
	}

	return chatReq, nil
}

// =========================================================================
// ToCoreResponse — *ChatResponse → *CoreResponse
// =========================================================================

// ToCoreResponse converts a *ChatResponse into a *CoreResponse.
//
// Each choice in the response becomes a separate assistant message in
// Messages. Token usage is extracted from Usage.
func (a *ChatProviderAdapter) ToCoreResponse(ctx context.Context, resp any) (*format.CoreResponse, error) {
	chatResp, ok := resp.(*ChatResponse)
	if !ok {
		return nil, fmt.Errorf("chat adapter: expected *ChatResponse, got %T", resp)
	}

	messages := make([]format.CoreMessage, 0, len(chatResp.Choices))
	for _, choice := range chatResp.Choices {
		msg := a.choiceToCoreMessage(choice)
		messages = append(messages, msg)
	}

	// Determine overall status and stop reason from the first choice.
	status := "completed"
	var stopReason string
	if len(chatResp.Choices) > 0 {
		stopReason = a.mapFinishReason(chatResp.Choices[0].FinishReason)
		switch chatResp.Choices[0].FinishReason {
		case "length":
			status = "incomplete"
		case "content_filter":
			status = "failed"
		}
	}

	coreResp := &format.CoreResponse{
		ID:         chatResp.ID,
		Status:     status,
		Messages:   messages,
		StopReason: stopReason,
	}

	if chatResp.Usage != nil {
		coreResp.Usage = format.CoreUsage{
			InputTokens:  chatResp.Usage.PromptTokens,
			OutputTokens: chatResp.Usage.CompletionTokens,
			TotalTokens:  chatResp.Usage.TotalTokens,
		}
		if chatResp.Usage.PromptTokensDetails != nil {
			coreResp.Usage.CachedInputTokens = chatResp.Usage.PromptTokensDetails.CachedTokens
		}
	}

	return coreResp, nil
}

// =========================================================================
// bufferStreamEvent buffers raw ChatStreamChunk for trace capture.
func (a *ChatProviderAdapter) bufferStreamEvent(ev ChatStreamChunk) {
	a.streamMu.Lock()
	defer a.streamMu.Unlock()
	a.streamEvents = append(a.streamEvents, ev)
}

// StreamBuffer returns the buffered stream events for trace capture.
func (a *ChatProviderAdapter) StreamBuffer() []ChatStreamChunk {
	a.streamMu.Lock()
	defer a.streamMu.Unlock()
	return a.streamEvents
}

// ToCoreStream — <-chan ChatStreamChunk → <-chan CoreStreamEvent
// =========================================================================

// ToCoreStream consumes a channel of ChatStreamChunk (from streaming Chat
// Completions) and returns a channel of CoreStreamEvent.
//
// OpenAI Chat streaming uses delta-based SSE (unlike Gemini's full snapshots),
// so no delta computation is needed — each chunk's delta is directly mapped.
//
// Emitted event sequence per choice:
//   - core.content_block.started (first chunk with role="assistant")
//   - core.text.delta (chunks with content delta)
//   - core.content_block.done (chunk with finish_reason set)
//   - core.completed (final chunk with Usage)
func (a *ChatProviderAdapter) ToCoreStream(ctx context.Context, src any) (<-chan format.CoreStreamEvent, error) {
	ch, ok := src.(<-chan ChatStreamChunk)
	if !ok {
		return nil, fmt.Errorf("chat adapter: expected <-chan ChatStreamChunk, got %T", src)
	}

	events := make(chan format.CoreStreamEvent, 64)

	// Initialize stream event buffer for trace capture.
	a.streamMu.Lock()
	a.streamEvents = make([]ChatStreamChunk, 0, 64)
	a.streamMu.Unlock()

	go func() {
		defer close(events)

		// Per-choice state for streaming.
		type choiceState struct {
			started    bool
			blockIndex int // monotonically increasing content block index
		}
		choices := make(map[int]*choiceState)
		var seqNum int64
		var finalUsage *format.CoreUsage
		var lastModel string
		var seenCompletion bool

		emit := func(ev format.CoreStreamEvent) {
			seqNum++
			ev.SeqNum = seqNum
			select {
			case events <- ev:
			case <-ctx.Done():
			}
		}

		for {
			select {
			case <-ctx.Done():
				return
			case chunk, ok := <-ch:
				a.bufferStreamEvent(chunk)
				if !ok {
					// Channel closed — emit completion if not already done.
					if !seenCompletion {
						if finalUsage != nil {
							emit(format.CoreStreamEvent{
								Type:   format.CoreEventCompleted,
								Status: "completed",
								Model:  lastModel,
								Usage:  finalUsage,
							})
						} else {
							emit(format.CoreStreamEvent{
								Type:   format.CoreEventCompleted,
								Status: "completed",
								Model:  lastModel,
							})
						}
					}
					return
				}

				if chunk.Model != "" {
					lastModel = chunk.Model
				}

				// Process each choice in the chunk.
				for _, sc := range chunk.Choices {
					state := choices[sc.Index]
					if state == nil {
						state = &choiceState{blockIndex: sc.Index * 2}
						choices[sc.Index] = state
					}

					ci := sc.Index

					// Emit content_block.started on first appearance with role.
					if !state.started && sc.Delta.Role == "assistant" {
						state.started = true
						emit(format.CoreStreamEvent{
							Type:        format.CoreContentBlockStarted,
							Index:       state.blockIndex,
							ChoiceIndex: &ci,
							ContentBlock: &format.CoreContentBlock{
								Type: "text",
							},
						})
					}

					// Emit text delta.
					if sc.Delta.Content != "" {
						emit(format.CoreStreamEvent{
							Type:        format.CoreTextDelta,
							Index:       state.blockIndex,
							Delta:       sc.Delta.Content,
							ChoiceIndex: &ci,
						})
					}

					// Emit tool call args delta.
					for _, tc := range sc.Delta.ToolCalls {
						emit(format.CoreStreamEvent{
							Type:        format.CoreToolCallArgsDelta,
							Index:       state.blockIndex,
							Delta:       string(tc.Function.Arguments),
							ChoiceIndex: &ci,
						})
					}

					// Emit content_block.done when finish_reason is set.
					if sc.FinishReason != "" {
						stopReason := a.mapFinishReason(sc.FinishReason)
						emit(format.CoreStreamEvent{
							Type:        format.CoreContentBlockDone,
							Index:       state.blockIndex,
							StopReason:  stopReason,
							ChoiceIndex: &ci,
						})
					}
				}

				// Track usage from the last chunk.
				if chunk.Usage != nil {
					finalUsage = &format.CoreUsage{
						InputTokens:  chunk.Usage.PromptTokens,
						OutputTokens: chunk.Usage.CompletionTokens,
						TotalTokens:  chunk.Usage.TotalTokens,
					}
					if chunk.Usage.PromptTokensDetails != nil {
						finalUsage.CachedInputTokens = chunk.Usage.PromptTokensDetails.CachedTokens
					}
				}
			}
		}
	}()

	return events, nil
}

// =========================================================================
// Helpers: Core → Chat
// =========================================================================

// toChatSystemContent combines Core system content blocks into a single string.
func (a *ChatProviderAdapter) toChatSystemContent(blocks []format.CoreContentBlock) string {
	var text string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			text += b.Text
		case "reasoning":
			continue // skip reasoning content in system
		default:
			if b.Text != "" {
				text += b.Text
			}
		}
	}
	return text
}

// toChatMessage converts a CoreMessage to a ChatMessage.
func (a *ChatProviderAdapter) toChatMessage(msg format.CoreMessage) ChatMessage {
	chatMsg := ChatMessage{
		Role: a.mapRoleToChat(msg.Role),
	}

	// Separate content blocks and tool calls.
	var textBlocks []format.CoreContentBlock
	var toolUseBlocks []format.CoreContentBlock
	for _, b := range msg.Content {
		switch b.Type {
		case "text", "image":
			textBlocks = append(textBlocks, b)
		case "tool_use":
			toolUseBlocks = append(toolUseBlocks, b)
		case "tool_result":
			textBlocks = append(textBlocks, b)
		case "reasoning":
			continue // skip reasoning content
		default:
			if b.Text != "" {
				textBlocks = append(textBlocks, b)
			}
		}
	}

	// Set content (string for text-only, ContentPart array for multimodal).
	if len(textBlocks) > 0 {
		content := a.toChatContent(textBlocks)
		chatMsg.Content = content
	}

	// Set tool calls for assistant messages.
	if len(toolUseBlocks) > 0 {
		chatMsg.ToolCalls = make([]ToolCall, 0, len(toolUseBlocks))
		for _, b := range toolUseBlocks {
			chatMsg.ToolCalls = append(chatMsg.ToolCalls, ToolCall{
				ID:   b.ToolUseID,
				Type: "function",
				Function: ToolCallFunc{
					Name:      b.ToolName,
					Arguments: b.ToolInput,
				},
			})
		}
	}

	// Set tool_call_id for tool result messages.
	if msg.Role == "tool" && len(toolUseBlocks) == 0 {
		// Look for tool_call_id in the content blocks.
		for _, b := range msg.Content {
			if b.Type == "tool_result" {
				chatMsg.ToolCallID = b.ToolUseID
				// Convert tool_result content to string.
				var toolResultText string
				for _, tc := range b.ToolResultContent {
					toolResultText += tc.Text
				}
				chatMsg.Content = toolResultText
				break
			}
		}
	}

	return chatMsg
}

// toChatContent converts []CoreContentBlock to a Chat content value.
// Returns a string for text-only content, or []ContentPart for multimodal.
func (a *ChatProviderAdapter) toChatContent(blocks []format.CoreContentBlock) any {
	hasImage := false
	for _, b := range blocks {
		if b.Type == "image" {
			hasImage = true
			break
		}
	}

	if !hasImage {
		// Combine text blocks into a single string.
		var text string
		for _, b := range blocks {
			switch b.Type {
			case "text":
				text += b.Text
			case "tool_result":
				for _, tc := range b.ToolResultContent {
					text += tc.Text
				}
			default:
				text += b.Text
			}
		}
		return text
	}

	// Build ContentPart array for multimodal content.
	parts := make([]ContentPart, 0, len(blocks))
	for _, b := range blocks {
		switch b.Type {
		case "text":
			parts = append(parts, ContentPart{Type: "text", Text: b.Text})
		case "image":
			parts = append(parts, ContentPart{
				Type:     "image_url",
				ImageURL: &ImageURL{URL: b.ImageData, Detail: "auto"},
			})
		case "tool_result":
			var resultText string
			for _, tc := range b.ToolResultContent {
				resultText += tc.Text
			}
			if resultText != "" {
				parts = append(parts, ContentPart{Type: "text", Text: resultText})
			}
		default:
			if b.Text != "" {
				parts = append(parts, ContentPart{Type: "text", Text: b.Text})
			}
		}
	}
	return parts
}

// mapRoleToChat maps a Core role to a Chat API role.
// Core "assistant" → Chat "assistant" (same mapping).
// Other roles pass through directly.
func (a *ChatProviderAdapter) mapRoleToChat(role string) string {
	// OpenAI Chat uses "assistant" for model responses (same as Core).
	// Other roles: "user", "system", "tool" — pass through directly.
	switch role {
	case "assistant":
		return "assistant"
	case "user":
		return "user"
	case "tool":
		return "tool"
	case "system":
		return "system"
	default:
		return "user"
	}
}

// toChatToolChoice converts CoreToolChoice to json.RawMessage for Chat API.
func (a *ChatProviderAdapter) toChatToolChoice(tc format.CoreToolChoice) json.RawMessage {
	switch tc.Mode {
	case "none":
		data, _ := json.Marshal("none")
		return data
	case "auto":
		data, _ := json.Marshal("auto")
		return data
	case "any", "required":
		if tc.Name != "" {
			choice := map[string]any{
				"type": "function",
				"function": map[string]string{
					"name": tc.Name,
				},
			}
			data, _ := json.Marshal(choice)
			return data
		}
		data, _ := json.Marshal("auto")
		return data
	default:
		if tc.Name != "" {
			choice := map[string]any{
				"type": "function",
				"function": map[string]string{
					"name": tc.Name,
				},
			}
			data, _ := json.Marshal(choice)
			return data
		}
		data, _ := json.Marshal("auto")
		return data
	}
}

// =========================================================================
// Helpers: Chat → Core
// =========================================================================

// choiceToCoreMessage converts a Choice to a CoreMessage.
func (a *ChatProviderAdapter) choiceToCoreMessage(choice Choice) format.CoreMessage {
	content := a.fromChatContent(choice.Message.Content)

	// Add tool calls as tool_use content blocks.
	if len(choice.Message.ToolCalls) > 0 {
		for _, tc := range choice.Message.ToolCalls {
			content = append(content, format.CoreContentBlock{
				Type:      "tool_use",
				ToolUseID: tc.ID,
				ToolName:  tc.Function.Name,
				ToolInput: unquoteArguments(tc.Function.Arguments),
			})
		}
	}

	return format.CoreMessage{
		Role:    "assistant",
		Content: content,
	}
}

// fromChatContent converts Chat content (string or []ContentPart) to CoreContentBlocks.
func (a *ChatProviderAdapter) fromChatContent(content any) []format.CoreContentBlock {
	if content == nil {
		return nil
	}

	switch v := content.(type) {
	case string:
		if v == "" {
			return nil
		}
		return []format.CoreContentBlock{
			{Type: "text", Text: v},
		}
	case []any:
		blocks := make([]format.CoreContentBlock, 0, len(v))
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				blocks = append(blocks, a.fromContentPartMap(m))
			}
		}
		return blocks
	default:
		return nil
	}
}

// fromContentPartMap converts a ContentPart map to a CoreContentBlock.
func (a *ChatProviderAdapter) fromContentPartMap(m map[string]any) format.CoreContentBlock {
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
		return format.CoreContentBlock{
			Type:      "image",
			ImageData: url,
		}
	default:
		text, _ := m["text"].(string)
		if text != "" {
			return format.CoreContentBlock{Type: "text", Text: text}
		}
		return format.CoreContentBlock{Type: "text"}
	}
}

// mapFinishReason maps OpenAI Chat finish_reason to Core stop_reason.
func (a *ChatProviderAdapter) mapFinishReason(reason string) string {
	switch reason {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	case "content_filter":
		return "content_filter"
	default:
		return reason
	}
}

// unquoteArguments unwraps a JSON-string-encoded tool call argument.
// Chat Completions API returns function.arguments as a JSON string
// (e.g. `"{\"city\":\"Paris\"}"`). When decoded as json.RawMessage,
// this retains the outer string quotes. unquoteArguments strips them
// so Core's ToolCallArguments contains a raw JSON object.
func unquoteArguments(raw json.RawMessage) json.RawMessage {
	if len(raw) < 2 || raw[0] != '"' {
		return raw
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return raw
	}
	return json.RawMessage(s)
}
