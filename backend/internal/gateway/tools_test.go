package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"
)

// Function-calling tests.
//
// These cover the emulation as an emulation: what the prompt gains, what the
// parser accepts, what it refuses to guess at, and what a client sees on both
// transports. Nothing here asserts that the upstream understands a tool schema,
// because it does not — see tools.go.

func TestInjectToolsAddsDeclarationsToThePrompt(t *testing.T) {
	tools := []toolSpec{{
		Type: "function",
		Function: toolFunction{
			Name:        "get_weather",
			Description: "Look up the weather",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`),
		},
	}}

	prompt := injectTools("北京天气怎么样", tools)

	for _, want := range []string{"get_weather", "Look up the weather", "北京天气怎么样", toolOpenTag} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("injected prompt is missing %q:\n%s", want, prompt)
		}
	}
}

func TestInjectToolsLeavesAnEmptyListAlone(t *testing.T) {
	if got := injectTools("原样", nil); got != "原样" {
		t.Fatalf("prompt changed with no tools: %q", got)
	}
	// A declaration with no name cannot be called, so it must not produce a
	// format block either — the model would be told to answer in a shape it has
	// no valid target for.
	if got := injectTools("原样", []toolSpec{{Type: "function"}}); got != "原样" {
		t.Fatalf("prompt changed for an unusable declaration: %q", got)
	}
}

func TestSplitToolCallsReadsATrailingBlock(t *testing.T) {
	text := "我查一下。\n" + toolOpenTag + "\n" +
		`{"name": "get_weather", "arguments": {"city": "北京"}}` + "\n" +
		toolCloseTag

	prose, calls := splitToolCalls(text)

	if prose != "我查一下。" {
		t.Fatalf("prose = %q", prose)
	}
	if len(calls) != 1 {
		t.Fatalf("expected one call, got %d", len(calls))
	}
	if calls[0].Name != "get_weather" {
		t.Fatalf("name = %q", calls[0].Name)
	}
	if !strings.Contains(calls[0].Arguments, "北京") {
		t.Fatalf("arguments lost their payload: %q", calls[0].Arguments)
	}
	if calls[0].ID == "" {
		t.Fatal("call has no id")
	}
}

func TestSplitToolCallsHandlesSeveralCalls(t *testing.T) {
	text := "ok\n" + toolOpenTag + "\n" +
		`{"name": "a", "arguments": {}}` + "\n" +
		`{"name": "b", "arguments": {"n": 2}}` + "\n" +
		toolCloseTag

	_, calls := splitToolCalls(text)
	if len(calls) != 2 {
		t.Fatalf("expected two calls, got %d", len(calls))
	}
	if calls[0].Name != "a" || calls[1].Name != "b" {
		t.Fatalf("names = %q, %q", calls[0].Name, calls[1].Name)
	}
}

func TestSplitToolCallsAcceptsArgumentsAsAString(t *testing.T) {
	// Models routinely stringify the arguments object. The mistake is
	// unambiguous, so it is accepted rather than turned into an empty call.
	text := toolOpenTag + "\n" +
		`{"name": "ping", "arguments": "{\"host\": \"example.com\"}"}` + "\n" +
		toolCloseTag

	_, calls := splitToolCalls(text)
	if len(calls) != 1 {
		t.Fatalf("expected one call, got %d", len(calls))
	}
	if !strings.Contains(calls[0].Arguments, "example.com") {
		t.Fatalf("arguments = %q", calls[0].Arguments)
	}
}

func TestSplitToolCallsKeepsTextItCouldNotParse(t *testing.T) {
	// A block that is present but unreadable is left in place. Deleting prose
	// the caller never saw would be worse than showing a malformed block, and
	// inventing a call from it would be worse still.
	text := "答案在这。\n" + toolOpenTag + "\nnot json at all\n" + toolCloseTag

	prose, calls := splitToolCalls(text)
	if prose != text {
		t.Fatalf("unparsable block was removed: %q", prose)
	}
	if len(calls) != 0 {
		t.Fatalf("expected no calls, got %d", len(calls))
	}
}

func TestSplitToolCallsKeepsProseAfterTheBlock(t *testing.T) {
	text := "先说一句\n" + toolOpenTag + "\n" +
		`{"name": "ping", "arguments": {}}` + "\n" +
		toolCloseTag + "\n然后再说一句"

	prose, calls := splitToolCalls(text)
	if len(calls) != 1 {
		t.Fatalf("expected one call, got %d", len(calls))
	}
	if !strings.Contains(prose, "先说一句") || !strings.Contains(prose, "然后再说一句") {
		t.Fatalf("prose around the block was lost: %q", prose)
	}
}

func TestSplitToolCallsLeavesPlainTextAlone(t *testing.T) {
	text := "就是一段普通回答，没有工具。"
	prose, calls := splitToolCalls(text)
	if prose != text || calls != nil {
		t.Fatalf("plain text was touched: %q / %v", prose, calls)
	}
}

func TestToolSplitterPassesThroughWhenDisabled(t *testing.T) {
	splitter := newToolSplitter(false)
	if got := splitter.push("hello"); got != "hello" {
		t.Fatalf("disabled splitter swallowed a delta: %q", got)
	}
	if tail, calls := splitter.finish(); tail != "" || calls != nil {
		t.Fatalf("disabled splitter produced %q / %v", tail, calls)
	}
}

func TestToolSplitterHoldsBackAnOpeningTagSplitAcrossDeltas(t *testing.T) {
	splitter := newToolSplitter(true)

	// The tag arrives in pieces. Nothing that could still become a tag may be
	// emitted, and everything before it must be.
	var emitted strings.Builder
	emitted.WriteString(splitter.push("答案是"))
	emitted.WriteString(splitter.push("<tool_"))
	emitted.WriteString(splitter.push("calls>\n"))
	emitted.WriteString(splitter.push(`{"name": "ping", "arguments": {}}`))
	emitted.WriteString(splitter.push("\n" + toolCloseTag))

	if emitted.String() != "答案是" {
		t.Fatalf("prose emitted = %q", emitted.String())
	}
	tail, calls := splitter.finish()
	if tail != "" {
		t.Fatalf("unexpected tail %q", tail)
	}
	if len(calls) != 1 || calls[0].Name != "ping" {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestToolSplitterReleasesAHeldPrefixThatWasNotATag(t *testing.T) {
	splitter := newToolSplitter(true)

	// `<tool` looks like the start of a tag and is withheld; the next delta
	// proves it was not, so the whole thing has to come back out — and no
	// multi-byte character may be cut in half on the way, because a streamed
	// byte cannot be taken back.
	//
	// The last few bytes stay held until finish: they could still have been the
	// beginning of a tag right up to the end of the stream.
	var emitted strings.Builder
	emitted.WriteString(splitter.push("看这里 <tool"))
	emitted.WriteString(splitter.push("box 不是标签"))
	tail, calls := splitter.finish()
	emitted.WriteString(tail)

	if emitted.String() != "看这里 <toolbox 不是标签" {
		t.Fatalf("emitted = %q", emitted.String())
	}
	if !utf8.ValidString(emitted.String()) {
		t.Fatalf("emitted invalid UTF-8: %q", emitted.String())
	}
	if calls != nil {
		t.Fatalf("a plain sentence produced calls: %+v", calls)
	}
}

func TestChatCompletionsReportsToolCalls(t *testing.T) {
	answer := "我查一下。\n" + toolOpenTag + "\n" +
		`{"name": "get_weather", "arguments": {"city": "北京"}}` + "\n" +
		toolCloseTag

	h := newHarness(t, acceptsEverything(upstreamText(answer)))
	h.addAccount(t, "a", "token-a", 0)

	rec := h.chat(t, map[string]any{
		"model":    "minimax-agent",
		"messages": []any{map[string]any{"role": "user", "content": "北京天气"}},
		"tools": []any{map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":       "get_weather",
				"parameters": map[string]any{"type": "object"},
			},
		}},
	}, h.key)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	body := decodeJSON(t, rec)
	choices := body["choices"].([]any)
	choice := choices[0].(map[string]any)

	if choice["finish_reason"] != "tool_calls" {
		t.Fatalf("finish_reason = %v", choice["finish_reason"])
	}
	message := choice["message"].(map[string]any)
	if message["content"] != "我查一下。" {
		t.Fatalf("content = %q", message["content"])
	}
	calls, ok := message["tool_calls"].([]any)
	if !ok || len(calls) != 1 {
		t.Fatalf("tool_calls = %v", message["tool_calls"])
	}
	call := calls[0].(map[string]any)
	fn := call["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Fatalf("function name = %v", fn["name"])
	}
	if !strings.Contains(fn["arguments"].(string), "北京") {
		t.Fatalf("arguments = %v", fn["arguments"])
	}
}

func TestChatCompletionsWithoutToolsLeavesTheBlockAlone(t *testing.T) {
	// The same body, sent by a client that declared nothing. Without a
	// declaration there is no agreement about the block's meaning, so it is
	// ordinary text and must survive untouched.
	answer := "看这段：" + toolOpenTag + `{"name":"x","arguments":{}}` + toolCloseTag

	h := newHarness(t, acceptsEverything(upstreamText(answer)))
	h.addAccount(t, "a", "token-a", 0)

	rec := h.chat(t, map[string]any{
		"model":    "minimax-agent",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	}, h.key)

	body := decodeJSON(t, rec)
	choice := body["choices"].([]any)[0].(map[string]any)
	message := choice["message"].(map[string]any)

	if choice["finish_reason"] != "stop" {
		t.Fatalf("finish_reason = %v", choice["finish_reason"])
	}
	if message["content"] != answer {
		t.Fatalf("content was altered: %q", message["content"])
	}
	if _, present := message["tool_calls"]; present {
		t.Fatal("tool_calls appeared without a declaration")
	}
}

func TestChatCompletionsStreamsToolCallsAtTheEnd(t *testing.T) {
	answer := "我查一下。\n" + toolOpenTag + "\n" +
		`{"name": "get_weather", "arguments": {"city": "北京"}}` + "\n" +
		toolCloseTag

	h := newHarness(t, acceptsEverything(upstreamText(answer)))
	h.addAccount(t, "a", "token-a", 0)

	rec := h.chat(t, map[string]any{
		"model":    "minimax-agent",
		"messages": []any{map[string]any{"role": "user", "content": "北京天气"}},
		"stream":   true,
		"tools": []any{map[string]any{
			"type":     "function",
			"function": map[string]any{"name": "get_weather"},
		}},
	}, h.key)

	var (
		prose     strings.Builder
		callNames []string
		finish    string
	)
	for _, line := range sseDataLines(t, rec) {
		if line == "[DONE]" {
			continue
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			t.Fatalf("chunk is not JSON: %s", line)
		}
		choice := chunk["choices"].([]any)[0].(map[string]any)
		delta := choice["delta"].(map[string]any)
		if text, ok := delta["content"].(string); ok {
			prose.WriteString(text)
		}
		if raw, ok := delta["tool_calls"].([]any); ok {
			for _, item := range raw {
				fn := item.(map[string]any)["function"].(map[string]any)
				callNames = append(callNames, fn["name"].(string))
			}
		}
		if reason, ok := choice["finish_reason"].(string); ok && reason != "" {
			finish = reason
		}
	}

	// The newline separating the prose from the block may already have gone out
	// as an earlier delta, so the streamed text can carry trailing space that
	// the buffered path trims away. Nothing else may differ.
	if strings.TrimSpace(prose.String()) != "我查一下。" {
		t.Fatalf("streamed prose = %q", prose.String())
	}
	if len(callNames) != 1 || callNames[0] != "get_weather" {
		t.Fatalf("streamed calls = %v", callNames)
	}
	if finish != "tool_calls" {
		t.Fatalf("finish_reason = %q", finish)
	}
}
