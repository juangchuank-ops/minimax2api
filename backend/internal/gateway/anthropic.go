package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"minimax2api/internal/minimax"
	"minimax2api/internal/store"
)

// Anthropic Messages API compatibility.
//
// This is a translation layer and nothing more. It speaks the shape Claude
// clients expect on both ends and hands the middle to exactly the same code the
// OpenAI endpoint uses — one account pool, one failover path, one audit trail.
// Nothing here reaches the upstream differently because of which front end the
// request arrived on.
//
// The model name is the one place the two differ in a way that matters.
// Anthropic clients send their own ids (`claude-sonnet-4-20250514` and the
// like) and there is no setting that would make them stop, so an unknown name
// resolves to the default chat model instead of failing. A name that does match
// the catalogue is honoured, which is what lets someone pin `minimax-m3-thinking`
// from a Claude client on purpose.

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    json.RawMessage    `json:"system"`
	Messages  []anthropicMessage `json:"messages"`
	Stream    bool               `json:"stream"`
	Tools     []anthropicTool    `json:"tools"`
}

type anthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// AnthropicMessages implements POST /v1/messages.
func (g *Gateway) AnthropicMessages(w http.ResponseWriter, r *http.Request) {
	out := &respWriter{ResponseWriter: w}
	started := time.Now()

	key, err := g.authenticate(r)
	if err != nil {
		writeAnthropicError(out, http.StatusUnauthorized, "authentication_error", err.Error())
		return
	}
	if err := g.limiter.acquire(key); err != nil {
		writeAnthropicError(out, http.StatusTooManyRequests, "rate_limit_error", err.Error())
		return
	}
	defer g.limiter.release(key)

	body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil {
		writeAnthropicError(out, http.StatusBadRequest, "invalid_request_error", "cannot read request body")
		return
	}

	var request anthropicRequest
	if err := json.Unmarshal(body, &request); err != nil {
		writeAnthropicError(out, http.StatusBadRequest, "invalid_request_error", "invalid JSON body")
		return
	}

	model := g.resolveAnthropicModel(request.Model)
	if model == nil {
		writeAnthropicError(out, http.StatusBadRequest, "invalid_request_error", "no chat model is available in the catalogue")
		return
	}

	prompt, images, err := g.anthropicPrompt(request.System, request.Messages)
	if err != nil {
		writeAnthropicError(out, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	if prompt == "" && len(images) == 0 {
		writeAnthropicError(out, http.StatusBadRequest, "invalid_request_error", "messages must contain text or image content")
		return
	}

	tools := anthropicToolSpecs(request.Tools)
	toolsEnabled := hasToolDeclarations(tools)
	if toolsEnabled {
		prompt = injectTools(prompt, tools)
	}

	mode := ""
	if model.ID == "minimax-m3-thinking" {
		mode = "think"
	}

	settings := g.settings()
	prompt = g.outgoingText(settings, model, prompt, minimax.VideoOptions{})

	splitter := newToolSplitter(toolsEnabled)
	stream := newAnthropicStream(out, model.ID, estimateTokens(prompt))

	var onDelta, onThinking func(string)
	if request.Stream {
		onDelta = func(text string) {
			if safe := splitter.push(text); safe != "" {
				stream.text(safe)
			}
		}
		onThinking = func(text string) {
			stream.thinking(text)
		}
		stream.start()
	}

	turn, lastErr := g.runTurn(r.Context(), turnRequest{
		Prompt:     prompt,
		Images:     images,
		Mode:       mode,
		Model:      model,
		SessionKey: "",
		Started:    started,
		OnDelta:    onDelta,
		OnThinking: onThinking,
		Committed:  func() bool { return out.wroteHeader },
	})

	promptTokens := estimateTokens(prompt)
	if lastErr != nil {
		g.recordAudit(r, key, model, turn.AccountName, started, http.StatusBadGateway, 0, promptTokens, 0, request.Stream, turn.Retries, string(body), "", lastErr.Error())
		if out.wroteHeader {
			stream.fail(lastErr.Error())
			return
		}
		writeAnthropicError(out, http.StatusBadGateway, "api_error", lastErr.Error())
		return
	}
	result := turn.Result
	if result == nil {
		writeAnthropicError(out, http.StatusBadGateway, "api_error", "upstream returned no result")
		return
	}

	text, calls := result.Text, []toolCall(nil)
	if toolsEnabled {
		text, calls = splitToolCalls(result.Text)
	}

	completionTokens := estimateTokens(result.Text + result.Thinking)
	g.store.RecordModelUsage(model.ID, 1, int64(promptTokens+completionTokens))
	g.store.BumpClientKeyUsage(key.ID)

	if request.Stream {
		if turn.Streamed {
			tail, held := splitter.finish()
			if tail != "" {
				stream.text(tail)
			}
			if held != nil {
				calls = held
			}
		} else {
			if result.Thinking != "" {
				stream.thinking(result.Thinking)
			}
			if text != "" {
				stream.text(text)
			}
		}
		stream.finish(calls, completionTokens)
		g.recordAudit(r, key, model, turn.AccountName, started, http.StatusOK, turn.FirstToken, promptTokens, completionTokens, true, turn.Retries, string(body), result.Text, "")
		return
	}

	content := make([]map[string]any, 0, 3)
	if result.Thinking != "" {
		content = append(content, map[string]any{"type": "thinking", "thinking": result.Thinking})
	}
	if text != "" || len(calls) == 0 {
		content = append(content, map[string]any{"type": "text", "text": text})
	}
	for _, call := range calls {
		content = append(content, map[string]any{
			"type":  "tool_use",
			"id":    call.ID,
			"name":  call.Name,
			"input": rawJSONObject(call.Arguments),
		})
	}

	stopReason := "end_turn"
	if len(calls) > 0 {
		stopReason = "tool_use"
	}

	response := map[string]any{
		"id":            "msg_" + randomID(24),
		"type":          "message",
		"role":          "assistant",
		"model":         model.ID,
		"content":       content,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":  promptTokens,
			"output_tokens": completionTokens,
		},
	}

	raw, _ := json.Marshal(response)
	g.recordAudit(r, key, model, turn.AccountName, started, http.StatusOK, turn.FirstToken, promptTokens, completionTokens, false, turn.Retries, string(body), string(raw), "")
	out.Header().Set("content-type", "application/json")
	out.WriteHeader(http.StatusOK)
	_, _ = out.Write(raw)
}

// resolveAnthropicModel picks the catalogue entry an Anthropic request maps to.
func (g *Gateway) resolveAnthropicModel(name string) *store.ModelConfig {
	if name != "" {
		if model, ok := g.store.ModelByID(name); ok && model.Enabled && model.Type == store.ModelTypeChat {
			return model
		}
	}
	for _, candidate := range []string{"minimax-agent", "minimax-m3"} {
		if model, ok := g.store.ModelByID(candidate); ok && model.Enabled && model.Type == store.ModelTypeChat {
			return model
		}
	}
	return nil
}

// anthropicToolSpecs converts Anthropic declarations to the internal shape.
//
// Anthropic puts the schema in `input_schema` while OpenAI nests it under
// `function.parameters`; everything downstream reads the OpenAI shape, so the
// conversion happens once, here.
func anthropicToolSpecs(tools []anthropicTool) []toolSpec {
	specs := make([]toolSpec, 0, len(tools))
	for _, tool := range tools {
		if strings.TrimSpace(tool.Name) == "" {
			continue
		}
		specs = append(specs, toolSpec{
			Type: "function",
			Function: toolFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.InputSchema,
			},
		})
	}
	return specs
}

// anthropicPrompt flattens an Anthropic conversation the way buildPrompt
// flattens an OpenAI one, so both front ends reach the upstream with the same
// text for the same conversation.
func (g *Gateway) anthropicPrompt(system json.RawMessage, messages []anthropicMessage) (string, []minimax.UploadedImage, error) {
	var builder strings.Builder
	var images []minimax.UploadedImage

	if text := anthropicBlocksText(system); text != "" {
		builder.WriteString("[系统指令] " + text + "\n\n")
	}

	for _, message := range messages {
		text, urls := anthropicBlocks(message.Content)
		switch message.Role {
		case "assistant":
			builder.WriteString("助手：" + text + "\n")
		default:
			if len(messages) > 1 {
				builder.WriteString("用户：" + text + "\n")
			} else {
				builder.WriteString(text + "\n")
			}
		}
		for _, raw := range urls {
			image, err := g.resolveImage(raw)
			if err != nil {
				return "", nil, err
			}
			images = append(images, image)
		}
	}

	return strings.TrimSpace(builder.String()), images, nil
}

// anthropicBlocksText reads the text out of a system value, which may be a bare
// string or an array of blocks.
func anthropicBlocksText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var text string
		if json.Unmarshal(raw, &text) == nil {
			return strings.TrimSpace(text)
		}
		return ""
	}
	text, _ := anthropicBlocks(raw)
	return text
}

// anthropicBlocks walks a content value and returns its text plus any image
// URLs.
//
// Tool traffic is rendered back into prose. The upstream has no tool channel to
// carry `tool_use` / `tool_result` blocks (see tools.go), so a conversation that
// contains them still has to arrive as words: the call the assistant made and
// the result the client produced are both part of the context the next answer
// depends on. Dropping them would silently change the question.
func anthropicBlocks(raw json.RawMessage) (string, []string) {
	if len(raw) == 0 {
		return "", nil
	}
	if raw[0] == '"' {
		var text string
		if json.Unmarshal(raw, &text) == nil {
			return strings.TrimSpace(text), nil
		}
		return "", nil
	}

	var blocks []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		Name  string          `json:"name"`
		ID    string          `json:"id"`
		Input json.RawMessage `json:"input"`
		// tool_result carries its payload under `content`, which may be a
		// string or an array of text blocks.
		Content json.RawMessage `json:"content"`
		Source  struct {
			Type string `json:"type"`
			URL  string `json:"url"`
		} `json:"source"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", nil
	}

	var parts []string
	var images []string
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if text := strings.TrimSpace(block.Text); text != "" {
				parts = append(parts, text)
			}
		case "thinking":
			// Prior reasoning is context the model produced; replaying it adds
			// nothing and costs tokens on every turn.
		case "image":
			if block.Source.URL != "" {
				images = append(images, block.Source.URL)
			}
		case "tool_use":
			parts = append(parts, fmt.Sprintf("[调用了工具 %s，参数 %s]", block.Name, string(block.Input)))
		case "tool_result":
			result := anthropicBlocksText(block.Content)
			parts = append(parts, fmt.Sprintf("[工具 %s 返回] %s", block.ID, result))
		default:
			if text := strings.TrimSpace(block.Text); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n"), images
}

// rawJSONObject keeps an arguments string as an object for the Anthropic
// response, where `input` is typed as a map rather than as a string.
func rawJSONObject(arguments string) json.RawMessage {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" || trimmed[0] != '{' {
		return json.RawMessage("{}")
	}
	return json.RawMessage(trimmed)
}

// ---------------------------------------------------------------- streaming

// anthropicStream emits the event sequence an Anthropic client expects.
//
// Blocks are opened lazily and in the order the model produces them, because
// the index is positional: a turn that thinks before it speaks opens thinking at
// 0 and text at 1, and a turn that never thinks opens text at 0. Deciding the
// indices up front would mean either an empty thinking block or a wrong index.
type anthropicStream struct {
	out          *respWriter
	messageID    string
	modelID      string
	inputTokens  int
	nextIndex    int
	thinkingAt   int
	textAt       int
	thinkingOpen bool
	textOpen     bool
}

func newAnthropicStream(out *respWriter, modelID string, inputTokens int) *anthropicStream {
	return &anthropicStream{
		out:         out,
		messageID:   "msg_" + randomID(24),
		modelID:     modelID,
		inputTokens: inputTokens,
		thinkingAt:  -1,
		textAt:      -1,
	}
}

func (s *anthropicStream) event(name string, payload map[string]any) {
	ensureStreamHeaders(s.out)
	raw, _ := json.Marshal(payload)
	_, _ = fmt.Fprintf(s.out, "event: %s\ndata: %s\n\n", name, raw)
	s.out.Flush()
}

func (s *anthropicStream) start() {
	s.event("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            s.messageID,
			"type":          "message",
			"role":          "assistant",
			"model":         s.modelID,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         map[string]any{"input_tokens": s.inputTokens, "output_tokens": 0},
		},
	})
}

func (s *anthropicStream) openThinking() {
	if s.thinkingOpen {
		return
	}
	s.thinkingAt = s.nextIndex
	s.nextIndex++
	s.thinkingOpen = true
	s.event("content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         s.thinkingAt,
		"content_block": map[string]any{"type": "thinking", "thinking": ""},
	})
}

func (s *anthropicStream) openText() {
	if s.textOpen {
		return
	}
	s.textAt = s.nextIndex
	s.nextIndex++
	s.textOpen = true
	s.event("content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         s.textAt,
		"content_block": map[string]any{"type": "text", "text": ""},
	})
}

func (s *anthropicStream) thinking(text string) {
	if text == "" {
		return
	}
	s.openThinking()
	s.event("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": s.thinkingAt,
		"delta": map[string]any{"type": "thinking_delta", "thinking": text},
	})
}

func (s *anthropicStream) text(delta string) {
	if delta == "" {
		return
	}
	s.openText()
	s.event("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": s.textAt,
		"delta": map[string]any{"type": "text_delta", "text": delta},
	})
}

// finish closes every open block and terminates the message.
func (s *anthropicStream) finish(calls []toolCall, outputTokens int) {
	if s.thinkingOpen {
		s.event("content_block_stop", map[string]any{"type": "content_block_stop", "index": s.thinkingAt})
		s.thinkingOpen = false
	}
	if s.textOpen {
		s.event("content_block_stop", map[string]any{"type": "content_block_stop", "index": s.textAt})
		s.textOpen = false
	}
	// A turn that produced nothing still needs one block, or the client is left
	// with a message whose content array is empty.
	if s.nextIndex == 0 {
		s.openText()
		s.event("content_block_stop", map[string]any{"type": "content_block_stop", "index": s.textAt})
		s.textOpen = false
	}

	stopReason := "end_turn"
	for _, call := range calls {
		index := s.nextIndex
		s.nextIndex++
		s.event("content_block_start", map[string]any{
			"type":  "content_block_start",
			"index": index,
			"content_block": map[string]any{
				"type":  "tool_use",
				"id":    call.ID,
				"name":  call.Name,
				"input": map[string]any{},
			},
		})
		s.event("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": index,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": call.Arguments},
		})
		s.event("content_block_stop", map[string]any{"type": "content_block_stop", "index": index})
		stopReason = "tool_use"
	}

	s.event("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": outputTokens},
	})
	s.event("message_stop", map[string]any{"type": "message_stop"})
}

// fail reports an upstream error inside an already-open stream.
func (s *anthropicStream) fail(message string) {
	s.event("error", map[string]any{
		"type":  "error",
		"error": map[string]any{"type": "api_error", "message": message},
	})
}

// writeAnthropicError emits the error envelope Anthropic clients parse.
func writeAnthropicError(w http.ResponseWriter, status int, kind, message string) {
	raw, _ := json.Marshal(map[string]any{
		"type":  "error",
		"error": map[string]any{"type": kind, "message": message},
	})
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}
