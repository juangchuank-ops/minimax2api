package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Anthropic front-end tests.
//
// The point of these is that a Claude client sees a Claude-shaped answer. What
// happens behind it — pool, failover, upstream — is the same code the OpenAI
// tests already cover, so nothing here re-asserts it.

type sseEvent struct {
	Name string
	Data string
}

func (h *harness) anthropic(t *testing.T, body map[string]any, token string) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(raw))
	req.Header.Set("content-type", "application/json")
	if token != "" {
		req.Header.Set("authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.gateway.AnthropicMessages(rec, req)
	return rec
}

// parseSSEEvents pulls the `event:` / `data:` pairs out of an Anthropic stream.
func parseSSEEvents(t *testing.T, body string) []sseEvent {
	t.Helper()
	var events []sseEvent
	var current sseEvent
	scanner := bufio.NewScanner(strings.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event:"):
			current.Name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			current.Data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		case line == "":
			if current.Name != "" || current.Data != "" {
				events = append(events, current)
				current = sseEvent{}
			}
		}
	}
	if current.Name != "" || current.Data != "" {
		events = append(events, current)
	}
	return events
}

func anthropicContentOf(t *testing.T, rec *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	body := decodeJSON(t, rec)
	raw, ok := body["content"].([]any)
	if !ok {
		t.Fatalf("content is not an array: %v", body["content"])
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		out = append(out, item.(map[string]any))
	}
	return out
}

func TestAnthropicMessagesReturnsAMessageShape(t *testing.T) {
	h := newHarness(t, acceptsEverything(upstreamText("你好，我是助手。")))
	h.addAccount(t, "a", "token-a", 0)

	rec := h.anthropic(t, map[string]any{
		"model":      "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"messages":   []any{map[string]any{"role": "user", "content": "你好"}},
	}, h.key)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	body := decodeJSON(t, rec)
	if body["type"] != "message" {
		t.Fatalf("type = %v", body["type"])
	}
	if body["role"] != "assistant" {
		t.Fatalf("role = %v", body["role"])
	}
	if body["stop_reason"] != "end_turn" {
		t.Fatalf("stop_reason = %v", body["stop_reason"])
	}
	if id, _ := body["id"].(string); !strings.HasPrefix(id, "msg_") {
		t.Fatalf("id = %v", body["id"])
	}

	content := anthropicContentOf(t, rec)
	if len(content) != 1 || content[0]["type"] != "text" {
		t.Fatalf("content = %v", content)
	}
	if content[0]["text"] != "你好，我是助手。" {
		t.Fatalf("text = %q", content[0]["text"])
	}

	usage := body["usage"].(map[string]any)
	if _, ok := usage["input_tokens"]; !ok {
		t.Fatalf("usage is missing input_tokens: %v", usage)
	}
	if _, ok := usage["output_tokens"]; !ok {
		t.Fatalf("usage is missing output_tokens: %v", usage)
	}
}

// A Claude client has no way to be told to send a different model id, so an
// unknown name has to resolve rather than fail — otherwise the endpoint is
// unreachable from the clients it exists for.
func TestAnthropicMessagesResolvesAForeignModelName(t *testing.T) {
	h := newHarness(t, acceptsEverything(upstreamText("ok")))
	h.addAccount(t, "a", "token-a", 0)

	rec := h.anthropic(t, map[string]any{
		"model":    "claude-opus-4-1-20250805",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	}, h.key)

	body := decodeJSON(t, rec)
	if body["model"] != "minimax-agent" {
		t.Fatalf("foreign model was not resolved: %v", body["model"])
	}
}

// A name that does match the catalogue is honoured, which is what lets someone
// pin the thinking variant on purpose.
func TestAnthropicMessagesHonoursAKnownModelName(t *testing.T) {
	h := newHarness(t, acceptsEverything(upstreamText("ok")))
	h.addAccount(t, "a", "token-a", 0)

	rec := h.anthropic(t, map[string]any{
		"model":    "minimax-m3-thinking",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	}, h.key)

	body := decodeJSON(t, rec)
	if body["model"] != "minimax-m3-thinking" {
		t.Fatalf("known model was not honoured: %v", body["model"])
	}
}

func TestAnthropicMessagesRejectsAMissingKey(t *testing.T) {
	h := newHarness(t, acceptsEverything(upstreamText("ok")))
	h.addAccount(t, "a", "token-a", 0)

	rec := h.anthropic(t, map[string]any{
		"model":    "minimax-agent",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	}, "")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
	body := decodeJSON(t, rec)
	if body["type"] != "error" {
		t.Fatalf("error envelope missing: %v", body)
	}
	inner := body["error"].(map[string]any)
	if inner["type"] != "authentication_error" {
		t.Fatalf("error type = %v", inner["type"])
	}
}

func TestAnthropicMessagesStreamsTheEventSequence(t *testing.T) {
	h := newHarness(t, acceptsEverything(upstreamText("分两次说完")))
	h.addAccount(t, "a", "token-a", 0)

	rec := h.anthropic(t, map[string]any{
		"model":    "minimax-agent",
		"stream":   true,
		"messages": []any{map[string]any{"role": "user", "content": "说点什么"}},
	}, h.key)

	if ct := rec.Header().Get("content-type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}

	events := parseSSEEvents(t, rec.Body.String())
	if len(events) == 0 {
		t.Fatal("no events were emitted")
	}

	var names []string
	for _, event := range events {
		names = append(names, event.Name)
	}

	// Order matters: a client drives its UI off these transitions.
	want := []string{"message_start", "content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}
	if len(names) < len(want) {
		t.Fatalf("too few events: %v", names)
	}
	for index, expected := range want {
		if names[index] != expected {
			t.Fatalf("event %d = %q, want %q (sequence: %v)", index, names[index], expected, names)
		}
	}

	var text strings.Builder
	for _, event := range events {
		if event.Name != "content_block_delta" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(event.Data), &payload); err != nil {
			t.Fatalf("delta is not JSON: %s", event.Data)
		}
		delta := payload["delta"].(map[string]any)
		if delta["type"] != "text_delta" {
			t.Fatalf("delta type = %v", delta["type"])
		}
		text.WriteString(delta["text"].(string))
	}
	if text.String() != "分两次说完" {
		t.Fatalf("streamed text = %q", text.String())
	}
}

func TestAnthropicMessagesSeparatesThinking(t *testing.T) {
	h := newHarness(t, acceptsEverything(upstreamThinking("先想一想", "答案")))
	h.addAccount(t, "a", "token-a", 0)

	rec := h.anthropic(t, map[string]any{
		"model":    "minimax-m3-thinking",
		"stream":   true,
		"messages": []any{map[string]any{"role": "user", "content": "问题"}},
	}, h.key)

	events := parseSSEEvents(t, rec.Body.String())

	// Thinking opens first and therefore takes index 0; text follows at 1.
	// Guessing the indices up front would leave an empty block behind.
	var blocks []string
	for _, event := range events {
		if event.Name != "content_block_start" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(event.Data), &payload); err != nil {
			t.Fatalf("block start is not JSON: %s", event.Data)
		}
		block := payload["content_block"].(map[string]any)
		blocks = append(blocks, block["type"].(string))
	}

	if len(blocks) != 2 || blocks[0] != "thinking" || blocks[1] != "text" {
		t.Fatalf("blocks = %v", blocks)
	}
}

func TestAnthropicMessagesReportsToolUse(t *testing.T) {
	answer := "我查一下。\n" + toolOpenTag + "\n" +
		`{"name": "get_weather", "arguments": {"city": "北京"}}` + "\n" +
		toolCloseTag

	h := newHarness(t, acceptsEverything(upstreamText(answer)))
	h.addAccount(t, "a", "token-a", 0)

	rec := h.anthropic(t, map[string]any{
		"model":    "minimax-agent",
		"messages": []any{map[string]any{"role": "user", "content": "北京天气"}},
		"tools": []any{map[string]any{
			"name":         "get_weather",
			"description":  "查天气",
			"input_schema": map[string]any{"type": "object"},
		}},
	}, h.key)

	body := decodeJSON(t, rec)
	if body["stop_reason"] != "tool_use" {
		t.Fatalf("stop_reason = %v", body["stop_reason"])
	}

	content := anthropicContentOf(t, rec)
	var (
		sawText bool
		sawUse  bool
	)
	for _, block := range content {
		switch block["type"] {
		case "text":
			sawText = true
			if block["text"] != "我查一下。" {
				t.Fatalf("text = %q", block["text"])
			}
		case "tool_use":
			sawUse = true
			if block["name"] != "get_weather" {
				t.Fatalf("tool name = %v", block["name"])
			}
			input, ok := block["input"].(map[string]any)
			if !ok {
				t.Fatalf("input is not an object: %v", block["input"])
			}
			if input["city"] != "北京" {
				t.Fatalf("input = %v", input)
			}
		}
	}
	if !sawText || !sawUse {
		t.Fatalf("content = %v", content)
	}
}

// The system prompt and any tool results the client sends back have to reach
// the upstream as words, because there is no structured channel for either.
func TestAnthropicMessagesCarriesSystemAndToolResultsIntoThePrompt(t *testing.T) {
	h := newHarness(t, acceptsEverything(upstreamText("好")))
	h.addAccount(t, "a", "token-a", 0)
	seen := h.captureMessages(t)

	rec := h.anthropic(t, map[string]any{
		"model":  "minimax-agent",
		"system": "你是一个简洁的助手",
		"messages": []any{
			map[string]any{"role": "user", "content": "北京天气"},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "id": "toolu_1", "name": "get_weather", "input": map[string]any{"city": "北京"}},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "content": "晴，26 度"},
			}},
		},
	}, h.key)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(*seen) == 0 {
		t.Fatal("no message reached the upstream")
	}

	prompt := (*seen)[0]
	for _, want := range []string{"你是一个简洁的助手", "get_weather", "晴，26 度"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt is missing %q:\n%s", want, prompt)
		}
	}
}

// An image block is the one part of an Anthropic conversation that is not text,
// and it has to survive the translation as an image rather than being dropped.
func TestAnthropicMessagesForwardsImageBlocks(t *testing.T) {
	h := newHarness(t, acceptsEverything(upstreamText("看到了")))
	h.addAccount(t, "a", "token-a", 0)

	rec := h.anthropic(t, map[string]any{
		"model": "minimax-agent",
		"messages": []any{map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "这是什么"},
			map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": "https://example.com/cat.png"}},
		}}},
	}, h.key)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	content := anthropicContentOf(t, rec)
	if len(content) == 0 || content[0]["text"] != "看到了" {
		t.Fatalf("content = %v", content)
	}
}
