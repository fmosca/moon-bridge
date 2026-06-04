// Package server implements the Moon Bridge HTTP server.
//
// This file implements the /v1/chat/completions endpoint for OpenAI Chat
// Completions protocol clients (e.g. Chatwise, Continue, LangChain).
//
// The handler converts Chat Completions requests to the internal OpenAI
// Responses format, dispatches through the existing adapter pipeline, and
// converts the OpenAI Responses back to Chat Completions format.
package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"moonbridge/internal/config"
	"moonbridge/internal/format"
	"moonbridge/internal/protocol/chat"
	openai "moonbridge/internal/protocol/openai"
	"moonbridge/internal/service/provider"
)

// handleChatCompletions handles POST /v1/chat/completions.
func (server *Server) handleChatCompletions(writer http.ResponseWriter, request *http.Request) {
	log := slog.Default().With("path", request.URL.Path, "method", request.Method, "remote", request.RemoteAddr)
	log.Debug("received chat completions request")

	if request.Method != http.MethodPost {
		writeChatError(writer, http.StatusMethodNotAllowed, "method not allowed", "invalid_request_error", "method_not_allowed")
		return
	}

	server.sessionForRequest(request)

	body, err := io.ReadAll(request.Body)
	if err != nil {
		log.Error("failed to read request body", "error", err)
		writeChatError(writer, http.StatusBadRequest, "failed to read request body", "invalid_request_error", "invalid_request_body")
		return
	}

	var chatReq chat.ChatRequest
	if err := json.Unmarshal(body, &chatReq); err != nil {
		log.Warn("invalid JSON request body", "error", err)
		writeChatError(writer, http.StatusBadRequest, "invalid JSON request body", "invalid_request_error", "invalid_json")
		return
	}

	if chatReq.Model == "" {
		writeChatError(writer, http.StatusBadRequest, "model is required", "invalid_request_error", "missing_model")
		return
	}

	// Resolve route (reuse the same routing logic).
	resolvedRoute, resolveErr := server.resolveModelOrFallback(chatReq.Model)
	if resolveErr != nil {
		log.Warn("unknown model requested", "model", chatReq.Model)
		writeChatError(writer, http.StatusNotFound, fmt.Sprintf("unknown model: %q", chatReq.Model), "invalid_request_error", "model_not_found")
		return
	}

	preferred, ok := resolvedRoute.Preferred()
	if !ok {
		log.Error("no available provider for model", "model", chatReq.Model)
		writeChatError(writer, http.StatusBadGateway, fmt.Sprintf("no available provider for model %q", chatReq.Model), "server_error", "provider_error")
		return
	}

	log.Debug("route resolved", "model", chatReq.Model, "provider", preferred.ProviderKey, "upstream", preferred.UpstreamModel, "protocol", preferred.Protocol)

	// Check adapter registry exists.
	if server.adapterRegistry == nil {
		writeChatError(writer, http.StatusInternalServerError, "adapter path not configured", "server_error", "adapter_not_configured")
		return
	}

	// Ensure we have adapters for this protocol.
	if _, ok := server.adapterRegistry.GetProvider(preferred.Protocol); !ok {
		writeChatError(writer, http.StatusBadGateway, fmt.Sprintf("no adapter for protocol %q", preferred.Protocol), "server_error", "adapter_not_configured")
		return
	}

	// Get the chat client adapter for Core conversion.
	chatClient, ok := server.adapterRegistry.GetClient(config.ProtocolOpenAIChat)
	if !ok {
		writeChatError(writer, http.StatusInternalServerError, "no chat client adapter", "server_error", "adapter_fallback")
		return
	}

	// ChatRequest → CoreRequest.
	coreReq, err := chatClient.ToCoreRequest(request.Context(), &chatReq)
	if err != nil {
		log.Error("chat ToCoreRequest failed", "error", err)
		writeChatError(writer, http.StatusInternalServerError, fmt.Sprintf("request conversion failed: %v", err), "server_error", "conversion_error")
		return
	}

	// Convert CoreRequest → ResponsesRequest for the existing dispatch.
	responsesReq := coreRequestToResponsesRequest(coreReq)

	// Dispatch: non-streaming uses the response-recorder approach,
	// streaming uses a pipe to translate SSE formats.
	if chatReq.Stream {
		server.dispatchChatStream(writer, request, chatReq, responsesReq, resolvedRoute, chatClient)
	} else {
		server.dispatchChatNonStream(writer, request, chatReq, responsesReq, resolvedRoute, chatClient)
	}
}

// dispatchChatNonStream handles non-streaming Chat Completions via the
// existing OpenAI adapter dispatch, with response format conversion.
func (server *Server) dispatchChatNonStream(
	w http.ResponseWriter,
	r *http.Request,
	chatReq chat.ChatRequest,
	responsesReq openai.ResponsesRequest,
	route *provider.ResolvedRoute,
	chatClient format.ClientAdapter,
) {
	log := slog.Default().With("model", chatReq.Model, "path", "chat-nonstream")
	ctx := r.Context()
	requestStart := time.Now()

	// Use an internal response recorder to capture the OpenAI response.
	rec := &chatResponseRecorder{header: make(http.Header)}

	// Dispatch through the existing adapter path.
	server.handleWithAdapters(rec, r, responsesReq, route)

	if rec.statusCode >= 400 {
		// Error was written in OpenAI format. Try to convert to Chat error.
		var errResp openai.ErrorResponse
		if json.Unmarshal(rec.body.Bytes(), &errResp) == nil {
			writeChatError(w, rec.statusCode, errResp.Error.Message, errResp.Error.Type, errResp.Error.Code)
		} else {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(rec.statusCode)
			w.Write(rec.body.Bytes())
		}
		return
	}

	// Parse OpenAI Response.
	var oaiResp openai.Response
	if err := json.Unmarshal(rec.body.Bytes(), &oaiResp); err != nil {
		log.Error("failed to parse OpenAI response", "error", err)
		writeChatError(w, http.StatusInternalServerError, "response parse error", "server_error", "internal_error")
		return
	}

	// OpenAI Response → CoreResponse → ChatResponse.
	oaiProvider, ok := server.adapterRegistry.GetProvider(config.ProtocolOpenAIResponse)
	if !ok {
		writeChatError(w, http.StatusInternalServerError, "no openai provider adapter", "server_error", "adapter_fallback")
		return
	}

	coreResp, err := oaiProvider.ToCoreResponse(ctx, &oaiResp)
	if err != nil {
		log.Error("ToCoreResponse failed", "error", err)
		writeChatError(w, http.StatusInternalServerError, "response conversion error", "server_error", "conversion_error")
		return
	}

	chatRespAny, err := chatClient.FromCoreResponse(ctx, coreResp)
	if err != nil {
		log.Error("chat FromCoreResponse failed", "error", err)
		writeChatError(w, http.StatusInternalServerError, "response conversion error", "server_error", "conversion_error")
		return
	}

	chatResp, ok := chatRespAny.(*chat.ChatResponse)
	if !ok {
		writeChatError(w, http.StatusInternalServerError, "unexpected response type", "server_error", "internal_error")
		return
	}

	chatResp.Model = chatReq.Model
	chat.WriteChatNonStreamResponse(w, chatResp)

	// Usage tracking.
	preferred, _ := route.Preferred()
	if server.pluginRegistry != nil {
		usage := zeroUsage(config.ProtocolOpenAIChat, "none")
	if coreResp.Usage.InputTokens > 0 || coreResp.Usage.OutputTokens > 0 {
		usage.NormalizedInputTokens = coreResp.Usage.InputTokens
		usage.NormalizedOutputTokens = coreResp.Usage.OutputTokens
		usage.NormalizedCacheRead = coreResp.Usage.CachedInputTokens
	}
		status := "success"
		if coreResp.Status == "failed" {
			status = "error"
		}
		server.onRequestCompleted(chatReq.Model, preferred.UpstreamModel, preferred.ProviderKey, requestStart, usage, 0, status, "")
	}

	log.Info("chat completions completed", "model", chatReq.Model, "provider", preferred.ProviderKey)
}

// dispatchChatStream handles streaming Chat Completions by translating
// between OpenAI Responses SSE and Chat Completions SSE.
func (server *Server) dispatchChatStream(
	w http.ResponseWriter,
	r *http.Request,
	chatReq chat.ChatRequest,
	responsesReq openai.ResponsesRequest,
	route *provider.ResolvedRoute,
	chatClient format.ClientAdapter,
) {
	log := slog.Default().With("model", chatReq.Model, "path", "chat-stream")

	// Create a pipe: write OpenAI SSE to pipeWriter, read/translate from pipeReader.
	pipeReader, pipeWriter := io.Pipe()

	// Create a response recorder that writes to the pipe.
	pipeRec := &chatStreamingRecorder{
		header: make(http.Header),
		writer:  pipeWriter,
	}

	// Start the OpenAI dispatch in a goroutine.
	go func() {
		defer pipeWriter.Close()
		server.handleWithAdapters(pipeRec, r, responsesReq, route)
	}()

	// Read OpenAI SSE events from the pipe and translate to Chat SSE.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	flusher, ok := w.(http.Flusher)
	if !ok {
		log.Warn("response writer does not support flushing")
	}

	scanner := bufio.NewScanner(pipeReader)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)

	sentRole := false
	var finishReason string
	chatID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	created := time.Now().Unix()

	for scanner.Scan() {
		line := scanner.Text()

		// Check for DONE marker.
		if line == "data: [DONE]" {
			// Send final chunk with finish_reason.
			if finishReason == "" {
				finishReason = "stop"
			}
			writeChatSSEChunk(w, chatID, chatReq.Model, created, 0, chat.Delta{}, finishReason, nil)
			if flusher != nil {
				flusher.Flush()
			}
			// Send Chat [DONE].
			io.WriteString(w, "data: [DONE]\n\n")
			if flusher != nil {
				flusher.Flush()
			}
			break
		}

		// Only process data: lines.
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "" {
			continue
		}

		// Parse the OpenAI SSE event data.
		var evt map[string]any
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			continue
		}

		eventType, _ := evt["type"].(string)

		switch eventType {
		case "response.output_text.delta":
			if !sentRole {
				sentRole = true
				writeChatSSEChunk(w, chatID, chatReq.Model, created, 0, chat.Delta{Role: "assistant"}, "", nil)
				if flusher != nil {
					flusher.Flush()
			}
			}
			delta, _ := evt["delta"].(string)
			writeChatSSEChunk(w, chatID, chatReq.Model, created, 0, chat.Delta{Content: delta}, "", nil)
			if flusher != nil {
				flusher.Flush()
			}

		case "response.function_call_arguments.delta":
			if !sentRole {
				sentRole = true
				writeChatSSEChunk(w, chatID, chatReq.Model, created, 0, chat.Delta{Role: "assistant"}, "", nil)
				if flusher != nil {
					flusher.Flush()
			}
			}
			// Extract tool call info from the event.
			var tc []chat.ToolCall
			if callID, ok := evt["call_id"].(string); ok {
				name := ""
				if n, ok2 := evt["name"].(string); ok2 {
					name = n
				}
				args, _ := evt["delta"].(string)
				tc = []chat.ToolCall{{
					ID:       callID,
					Type:     "function",
					Function: chat.ToolCallFunc{Name: name, Arguments: json.RawMessage(args)},
				}}
			}
			writeChatSSEChunk(w, chatID, chatReq.Model, created, 0, chat.Delta{ToolCalls: tc}, "", nil)
			if flusher != nil {
				flusher.Flush()
			}

		case "response.completed":
			// Extract usage and finish reason from the completed event.
			if resp, ok := evt["response"].(map[string]any); ok {
				if status, ok := resp["status"].(string); ok {
					switch status {
					case "completed":
						finishReason = "stop"
					case "incomplete":
						finishReason = "length"
					case "failed":
						finishReason = "stop"
					}
				}
				// Check for tool calls in output.
				if output, ok := resp["output"].([]any); ok {
					for _, item := range output {
						if m, ok := item.(map[string]any); ok {
							if t, _ := m["type"].(string); t == "function_call" {
								finishReason = "tool_calls"
							}
						}
					}
			}
				// Usage.
				var usage *chat.Usage
				if u, ok := resp["usage"].(map[string]any); ok {
					inputTokens, _ := u["input_tokens"].(float64)
					outputTokens, _ := u["output_tokens"].(float64)
					if inputTokens > 0 || outputTokens > 0 {
						usage = &chat.Usage{
							PromptTokens:     int(inputTokens),
							CompletionTokens:  int(outputTokens),
							TotalTokens:      int(inputTokens + outputTokens),
						}
					}
				}
				// Emit final chunk.
				if finishReason == "" {
					finishReason = "stop"
				}
				writeChatSSEChunk(w, chatID, chatReq.Model, created, 0, chat.Delta{}, finishReason, usage)
				if flusher != nil {
					flusher.Flush()
			}
			}

		case "response.output_item.added":
			// New output item — check if tool call.
			if item, ok := evt["item"].(map[string]any); ok {
				if t, _ := item["type"].(string); t == "function_call" {
					if !sentRole {
						sentRole = true
						writeChatSSEChunk(w, chatID, chatReq.Model, created, 0, chat.Delta{Role: "assistant"}, "", nil)
						if flusher != nil {
							flusher.Flush()
						}
					}
					callID, _ := item["call_id"].(string)
					name, _ := item["name"].(string)
					tc := []chat.ToolCall{{
						ID:       callID,
						Type:     "function",
						Index:    intPtr(0),
						Function: chat.ToolCallFunc{Name: name},
					}}
					writeChatSSEChunk(w, chatID, chatReq.Model, created, 0, chat.Delta{ToolCalls: tc}, "", nil)
					if flusher != nil {
						flusher.Flush()
				}
				}
			}

		case "response.reasoning_summary_text.delta":
			// Reasoning delta — skip in Chat format (no standard representation).

		default:
			// Skip other event types.
		}
	}

	log.Info("chat stream completed", "model", chatReq.Model)
}

// coreRequestToResponsesRequest converts a CoreRequest to an OpenAI ResponsesRequest
// for dispatch through the existing adapter pipeline.
func coreRequestToResponsesRequest(coreReq *format.CoreRequest) openai.ResponsesRequest {
	req := openai.ResponsesRequest{
		Model:           coreReq.Model,
		Stream:           coreReq.Stream,
		MaxOutputTokens:  coreReq.MaxTokens,
	}

	if coreReq.Temperature != nil {
		req.Temperature = coreReq.Temperature
	}
	if coreReq.TopP != nil {
		req.TopP = coreReq.TopP
	}

	// Build input from messages (OpenAI Responses uses "input" which can be
	// strings or structured messages).
	var inputMessages []any
	for _, msg := range coreReq.Messages {
		inputMsg := map[string]any{"role": msg.Role}
		var contentParts []any
		for _, block := range msg.Content {
			switch block.Type {
			case "text":
				contentParts = append(contentParts, map[string]any{"type": "text", "text": block.Text})
			case "image":
				contentParts = append(contentParts, map[string]any{"type": "input_image", "image_url": block.ImageData})
			case "tool_use":
				// Tool use in assistant message.
				argsStr := string(block.ToolInput)
				inputMessages = append(inputMessages, map[string]any{
					"type":    "function_call",
					"call_id": block.ToolUseID,
				"name":    block.ToolName,
				"arguments": argsStr,
			})
				continue // Don't add as content part.
			case "tool_result":
				// Tool result as function_call_output.
				resultText := ""
				for _, rb := range block.ToolResultContent {
					resultText += rb.Text
				}
				inputMessages = append(inputMessages, map[string]any{
					"type":    "function_call_output",
				"call_id": block.ToolUseID,
				"output":  resultText,
			})
				continue
			case "reasoning":
				contentParts = append(contentParts, map[string]any{"type": "reasoning", "text": block.ReasoningText})
			default:
				if block.Text != "" {
					contentParts = append(contentParts, map[string]any{"type": "text", "text": block.Text})
			}
			}
		}
		if len(contentParts) > 0 {
			if len(contentParts) == 1 {
				if m, ok := contentParts[0].(map[string]any); ok {
					if t, _ := m["type"].(string); t == "text" {
						if text, _ := m["text"].(string); text != "" {
							inputMsg["content"] = text
						}
					}
				}
			}
			if inputMsg["content"] == nil {
				inputMsg["content"] = contentParts
			}
		}
		// Only append if this wasn't handled as a function_call/function_call_output.
		if msg.Role != "" && !(msg.Role == "assistant" && len(contentParts) == 0 && len(msg.Content) > 0 && msg.Content[0].Type == "tool_use") {
			if msg.Role == "user" && len(msg.Content) > 0 && len(msg.Content) == 1 && msg.Content[0].Type == "tool_result" {
				// Already handled as function_call_output.
			} else {
				inputMessages = append(inputMessages, inputMsg)
			}
		}
	}

	// Instructions from system.
	if len(coreReq.System) > 0 {
		var sysTexts []string
		for _, block := range coreReq.System {
			if block.Type == "text" && block.Text != "" {
				sysTexts = append(sysTexts, block.Text)
			}
		}
		if len(sysTexts) > 0 {
			req.Instructions = strings.Join(sysTexts, "\n")
		}
	}

	// Encode input as JSON.
	if len(inputMessages) > 0 {
		inputJSON, _ := json.Marshal(inputMessages)
		req.Input = json.RawMessage(inputJSON)
	}

	// Tools.
	if len(coreReq.Tools) > 0 {
		for _, t := range coreReq.Tools {
			req.Tools = append(req.Tools, openai.Tool{
				Type:        "function",
				Name:        t.Name,
				Description:  t.Description,
				Parameters:  t.InputSchema,
			})
		}
	}

	// Tool choice.
	if coreReq.ToolChoice != nil {
		if coreReq.ToolChoice.Raw != nil {
			req.ToolChoice = coreReq.ToolChoice.Raw
		} else {
			switch coreReq.ToolChoice.Mode {
			case "auto":
				req.ToolChoice = json.RawMessage(`"auto"`)
			case "none":
				req.ToolChoice = json.RawMessage(`"none"`)
			case "required":
				req.ToolChoice = json.RawMessage(`"required"`)
			default:
				req.ToolChoice = json.RawMessage(`"auto"`)
			}
		}
	}

	// Parallel tool calls.
	if coreReq.Extensions != nil {
		if ptc, ok := coreReq.Extensions["parallel_tool_calls"]; ok {
			if b, ok := ptc.(bool); ok {
				req.ParallelToolCalls = &b
			}
		}
	}

	return req
}

// writeChatSSEChunk writes a single Chat Completions streaming chunk.
func writeChatSSEChunk(w io.Writer, id, model string, created int64, index int, delta chat.Delta, finishReason string, usage *chat.Usage) {
	chunk := chat.ChatStreamChunk{
		ID:      id,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   model,
		Choices: []chat.StreamChoice{{
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
		return
	}
	w.Write([]byte("data: "))
	w.Write(data)
	w.Write([]byte("\n\n"))
}

// writeChatError writes a Chat Completions compatible error response.
func writeChatError(w http.ResponseWriter, statusCode int, message, errType, code string) {
	errResp := map[string]any{
		"error": map[string]string{
			"message": message,
			"type":    errType,
			"code":   code,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(errResp)
}

// intPtr returns a pointer to the given int.
func intPtr(i int) *int {
	return &i
}

// chatBytesBuilder is a bytes.Buffer for capturing response bodies.
type chatBytesBuilder struct {
	buf []byte
}

func (b *chatBytesBuilder) Write(data []byte) (int, error) {
	b.buf = append(b.buf, data...)
	return len(data), nil
}

func (b *chatBytesBuilder) Bytes() []byte {
	return b.buf
}

// chatResponseRecorder captures an HTTP response for non-streaming Chat dispatch.
type chatResponseRecorder struct {
	header    http.Header
	body      chatBytesBuilder
	statusCode int
}

func (r *chatResponseRecorder) Header() http.Header {
	return r.header
}

func (r *chatResponseRecorder) Write(data []byte) (int, error) {
	return r.body.Write(data)
}

func (r *chatResponseRecorder) WriteHeader(code int) {
	r.statusCode = code
}

// chatStreamingRecorder wraps an io.Writer for streaming Chat dispatch.
// It satisfies http.ResponseWriter by forwarding Write calls to the underlying
// writer while capturing headers and status code.
type chatStreamingRecorder struct {
	header   http.Header
	writer   io.Writer
	statusCode int
}

func (r *chatStreamingRecorder) Header() http.Header {
	return r.header
}

func (r *chatStreamingRecorder) Write(data []byte) (int, error) {
	return r.writer.Write(data)
}

func (r *chatStreamingRecorder) WriteHeader(code int) {
	r.statusCode = code
}
