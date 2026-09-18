package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"minimax2api/internal/config"
	"minimax2api/internal/minimax"
	"minimax2api/internal/pool"
	"minimax2api/internal/store"
)

// These tests drive the full request path - auth, limiting, pool scheduling,
// upstream parsing, audit - against a stub upstream, so the gateway can be
// exercised end to end without a live MiniMax credential.

type harness struct {
	gateway  *Gateway
	store    *store.Store
	pool     *pool.Pool
	upstream *httptest.Server
	settings *config.Settings
	key      string
}

// upstreamText builds the SSE body a normal answer produces.
//
// The adapter dispatches on payload keys rather than on the numeric frame
// types, so this pins the documented shape without depending on the event enum.
func upstreamText(text string) string {
	chunk, _ := json.Marshal(map[string]any{
		"type":                6,
		"agent_message_chunk": map[string]any{"msg_content": text},
	})
	return "data:" + string(chunk) + "\n\n" +
		`data:{"type":9,"finish_reason":"stop"}` + "\n\n"
}

// upstreamThinking builds a reply that carries a separate reasoning stream.
func upstreamThinking(reasoning, text string) string {
	think, _ := json.Marshal(map[string]any{
		"type":           4,
		"thinking_chunk": map[string]any{"reasoning_content": reasoning},
	})
	answer, _ := json.Marshal(map[string]any{
		"type":                6,
		"agent_message_chunk": map[string]any{"msg_content": text},
	})
	return "data:" + string(think) + "\n\n" + "data:" + string(answer) + "\n\n"
}

// upstreamMedia builds a reply that produced media attachments.
func upstreamMedia(urls ...string) string {
	items := make([]any, 0, len(urls))
	for _, url := range urls {
		items = append(items, map[string]any{"image_url": url})
	}
	chunk, _ := json.Marshal(map[string]any{
		"type":                6,
		"agent_message_chunk": map[string]any{"msg_content": ""},
		"media":               items,
	})
	return "data:" + string(chunk) + "\n\n"
}

// upstreamFunc builds the stub upstream.
//
// The adapter makes two calls per completion - a session handshake and then the
// message stream - and the token travels in a header on both. Modelling a
// rejected credential therefore means answering 401 to the handshake, which is
// what accept returning false does. accept sees every request; stream only sees
// the message call.
func upstreamFunc(accept func(token string) bool, stream func(token string) string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("token")
		if accept != nil && !accept(token) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/message") {
			w.Header().Set("content-type", "text/event-stream")
			_, _ = io.WriteString(w, stream(token))
			return
		}
		w.Header().Set("content-type", "application/json")
		_, _ = io.WriteString(w, `{"session_id":"sess_test"}`)
	}
}

// acceptsEverything is the common stub: every credential works and every answer
// is the same body.
func acceptsEverything(body string) http.HandlerFunc {
	return upstreamFunc(nil, func(string) string { return body })
}

func newHarness(t *testing.T, upstream http.HandlerFunc) *harness {
	t.Helper()

	dir := t.TempDir()
	st, err := store.Open(dir, "admin", "admin12345")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	settings := config.DefaultSettings(dir)
	settings.Routing.CapacityWaitSec = 0 // fail fast instead of waiting for capacity
	settings.Routing.MaxAttempts = 3
	settings.Upstream.RequestTimeoutSec = 5
	settings.Upstream.StreamIdleTimeoutSec = 3
	settings.Media.AutoDownload = false

	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)
	settings.Upstream.BaseURL = server.URL

	settingsFn := func() config.Settings { return settings }
	client := minimax.New(settingsFn)
	p := pool.New(st, settingsFn)
	gw := New(st, p, client, settingsFn)

	key, err := st.CreateClientKey("test-key", 0, 0)
	if err != nil {
		t.Fatalf("create client key: %v", err)
	}

	return &harness{
		gateway:  gw,
		store:    st,
		pool:     p,
		upstream: server,
		settings: &settings,
		key:      key.Key,
	}
}

// addAccount inserts a routable account. The fingerprint is derived from the
// name so no two accounts in a test share one.
func (h *harness) addAccount(t *testing.T, name, token string, priority int) *store.Account {
	t.Helper()
	account := &store.Account{
		Name: name, Token: token, Region: store.RegionGlobal,
		UUID: "uuid-" + name, DeviceID: "device-" + name,
		Priority: priority, MaxConcurrent: 2, Enabled: true,
	}
	if err := h.store.AddAccount(account); err != nil {
		t.Fatalf("add account %s: %v", name, err)
	}
	return account
}

func (h *harness) chat(t *testing.T, body map[string]any, token string) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(raw))
	req.Header.Set("content-type", "application/json")
	if token != "" {
		req.Header.Set("authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.gateway.ChatCompletions(rec, req)
	return rec
}

func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not JSON: %v\nbody: %s", err, rec.Body.String())
	}
	return out
}

// sseDataLines pulls every `data:` payload out of an SSE response.
func sseDataLines(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	var out []string
	scanner := bufio.NewScanner(strings.NewReader(rec.Body.String()))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			out = append(out, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	return out
}

func TestChatCompletionsRequiresClientKey(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream must not be called without a valid key")
	})

	rec := h.chat(t, map[string]any{"model": "minimax-agent", "messages": []any{map[string]any{"role": "user", "content": "hi"}}}, "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestChatCompletionsRejectsUnknownKey(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream must not be called with an unknown key")
	})

	rec := h.chat(t, map[string]any{"model": "minimax-agent", "messages": []any{map[string]any{"role": "user", "content": "hi"}}}, "sk-mm-wrong")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestChatCompletionsRejectsUnknownModel(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream must not be called for an unknown model")
	})

	rec := h.chat(t, map[string]any{"model": "gpt-4", "messages": []any{map[string]any{"role": "user", "content": "hi"}}}, h.key)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "model") {
		t.Fatalf("error should name the model: %s", rec.Body.String())
	}
}

func TestChatCompletionsReturnsOpenAIResponse(t *testing.T) {
	h := newHarness(t, upstreamFunc(
		func(token string) bool {
			if token != "token-good" {
				t.Errorf("token = %q, want the pooled account credential", token)
			}
			return true
		},
		func(string) string { return upstreamText("你好，世界") },
	))
	h.addAccount(t, "primary", "token-good", 10)

	rec := h.chat(t, map[string]any{
		"model":    "minimax-agent",
		"messages": []any{map[string]any{"role": "user", "content": "打个招呼"}},
	}, h.key)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	payload := decodeJSON(t, rec)

	if payload["object"] != "chat.completion" {
		t.Fatalf("object = %v", payload["object"])
	}
	if payload["model"] != "minimax-agent" {
		t.Fatalf("model = %v", payload["model"])
	}
	choices, _ := payload["choices"].([]any)
	if len(choices) != 1 {
		t.Fatalf("choices = %v", payload["choices"])
	}
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	if message["content"] != "你好，世界" {
		t.Fatalf("content = %v", message["content"])
	}
	if message["role"] != "assistant" {
		t.Fatalf("role = %v", message["role"])
	}
	if choice["finish_reason"] != "stop" {
		t.Fatalf("finish_reason = %v", choice["finish_reason"])
	}
	usage, _ := payload["usage"].(map[string]any)
	if usage["total_tokens"] == nil {
		t.Fatalf("usage missing: %v", payload["usage"])
	}

	// The pool must hand the account back after the request.
	if got := h.pool.Inflight(accountID(t, h, "primary")); got != 0 {
		t.Fatalf("inflight = %d after the request, want 0", got)
	}
}

// Reasoning arrives on its own frame and must land in reasoning_content rather
// than being concatenated into the answer.
func TestChatCompletionsSeparatesReasoning(t *testing.T) {
	h := newHarness(t, acceptsEverything(upstreamThinking("先想一下", "答案是 42")))
	h.addAccount(t, "primary", "token-good", 10)

	rec := h.chat(t, map[string]any{
		"model":    "minimax-m3-thinking",
		"messages": []any{map[string]any{"role": "user", "content": "6*7?"}},
	}, h.key)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	payload := decodeJSON(t, rec)
	choices, _ := payload["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	if message["content"] != "答案是 42" {
		t.Fatalf("content = %v, want only the answer", message["content"])
	}
	if message["reasoning_content"] != "先想一下" {
		t.Fatalf("reasoning_content = %v", message["reasoning_content"])
	}
}

func TestChatCompletionsStreamsSSE(t *testing.T) {
	h := newHarness(t, acceptsEverything(upstreamText("流式回答")))
	h.addAccount(t, "primary", "token-good", 10)

	rec := h.chat(t, map[string]any{
		"model":    "minimax-agent",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
		"stream":   true,
	}, h.key)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("content-type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}
	// Proxies must not buffer a stream.
	if rec.Header().Get("x-accel-buffering") != "no" {
		t.Fatalf("x-accel-buffering = %q", rec.Header().Get("x-accel-buffering"))
	}

	lines := sseDataLines(t, rec)
	if len(lines) < 3 {
		t.Fatalf("expected several SSE chunks, got %v", lines)
	}
	if lines[len(lines)-1] != "[DONE]" {
		t.Fatalf("stream must end with [DONE], got %q", lines[len(lines)-1])
	}

	var sawRole, sawContent, sawStop bool
	for _, line := range lines {
		if line == "[DONE]" {
			continue
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			t.Fatalf("chunk is not JSON: %v (%q)", err, line)
		}
		if chunk["object"] != "chat.completion.chunk" {
			t.Fatalf("object = %v", chunk["object"])
		}
		choices, _ := chunk["choices"].([]any)
		if len(choices) != 1 {
			t.Fatalf("choices = %v", choices)
		}
		entry, _ := choices[0].(map[string]any)
		delta, _ := entry["delta"].(map[string]any)
		if delta["role"] == "assistant" {
			sawRole = true
		}
		if delta["content"] == "流式回答" {
			sawContent = true
		}
		if entry["finish_reason"] == "stop" {
			sawStop = true
		}
	}
	if !sawRole || !sawContent || !sawStop {
		t.Fatalf("missing frames: role=%v content=%v stop=%v", sawRole, sawContent, sawStop)
	}
}

// A rejected credential must be marked invalid and the request retried on the
// next account in the pool.
func TestChatCompletionsFailsOverToHealthyAccount(t *testing.T) {
	var attempts []string
	h := newHarness(t, upstreamFunc(
		func(token string) bool {
			attempts = append(attempts, token)
			return token != "token-bad"
		},
		func(string) string { return upstreamText("换号成功") },
	))

	bad := h.addAccount(t, "broken", "token-bad", 1) // picked first
	h.addAccount(t, "healthy", "token-good", 50)

	rec := h.chat(t, map[string]any{
		"model":    "minimax-agent",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	}, h.key)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 after failover; body = %s", rec.Code, rec.Body.String())
	}
	payload := decodeJSON(t, rec)
	choices, _ := payload["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	if message["content"] != "换号成功" {
		t.Fatalf("content = %v", message["content"])
	}

	// The rejected account fails at the handshake, so it contributes exactly one
	// attempt and the healthy account is tried afterwards.
	if len(attempts) == 0 || attempts[0] != "token-bad" {
		t.Fatalf("attempts = %v, want the rejected account tried first", attempts)
	}
	if !slices.Contains(attempts, "token-good") {
		t.Fatalf("attempts = %v, want a retry on the healthy account", attempts)
	}

	// The rejected account must be quarantined so it stops being scheduled.
	updated, ok := h.store.AccountByID(bad.ID)
	if !ok {
		t.Fatal("account disappeared")
	}
	if updated.Status != store.StatusInvalid {
		t.Fatalf("status = %q, want %q after a 401", updated.Status, store.StatusInvalid)
	}
	if updated.LastError == "" {
		t.Fatal("LastError should explain why the account was quarantined")
	}
}

func TestChatCompletionsReportsNoAccount(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream must not be called when the pool is empty")
	})

	rec := h.chat(t, map[string]any{
		"model":    "minimax-agent",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	}, h.key)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
}

func TestChatCompletionsRecordsAudit(t *testing.T) {
	h := newHarness(t, acceptsEverything(upstreamText("审计")))
	h.addAccount(t, "primary", "token-good", 10)

	if rec := h.chat(t, map[string]any{
		"model":    "minimax-agent",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	}, h.key); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	audits := h.store.ListAudits()
	if len(audits) != 1 {
		t.Fatalf("audits = %d, want 1", len(audits))
	}
	audit := audits[0]
	if audit.Status != http.StatusOK {
		t.Fatalf("audit status = %d", audit.Status)
	}
	if audit.Model != "minimax-agent" {
		t.Fatalf("audit model = %q", audit.Model)
	}
	if audit.AccountName != "primary" {
		t.Fatalf("audit account = %q", audit.AccountName)
	}
	if audit.LatencyMs < 0 {
		t.Fatalf("audit latency = %d", audit.LatencyMs)
	}
}

// A failed call must still be audited, with the upstream reason attached.
func TestChatCompletionsAuditsFailures(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = fmt.Fprint(w, "upstream is down")
	})
	h.addAccount(t, "primary", "token-good", 10)

	rec := h.chat(t, map[string]any{
		"model":    "minimax-agent",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	}, h.key)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}

	audits := h.store.ListAudits()
	if len(audits) == 0 {
		t.Fatal("a failed request must still produce an audit record")
	}
	if audits[0].Error == "" {
		t.Fatal("audit should carry the upstream error")
	}
}

func TestChatCompletionsEnforcesRateLimit(t *testing.T) {
	h := newHarness(t, acceptsEverything(upstreamText("ok")))
	h.addAccount(t, "primary", "token-good", 10)

	// Replace the unlimited key with a 1 RPM one.
	limited, err := h.store.CreateClientKey("limited", 1, 4)
	if err != nil {
		t.Fatalf("create limited key: %v", err)
	}

	body := map[string]any{"model": "minimax-agent", "messages": []any{map[string]any{"role": "user", "content": "hi"}}}
	if rec := h.chat(t, body, limited.Key); rec.Code != http.StatusOK {
		t.Fatalf("first call status = %d", rec.Code)
	}
	rec := h.chat(t, body, limited.Key)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second call status = %d, want 429", rec.Code)
	}
}

func TestModelsEndpoint(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	h.gateway.Models(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d, want 401", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("authorization", "Bearer "+h.key)
	rec = httptest.NewRecorder()
	h.gateway.Models(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	payload := decodeJSON(t, rec)
	if payload["object"] != "list" {
		t.Fatalf("object = %v", payload["object"])
	}
	data, _ := payload["data"].([]any)
	if len(data) == 0 {
		t.Fatal("model list is empty")
	}
	ids := map[string]bool{}
	for _, item := range data {
		entry, _ := item.(map[string]any)
		id, _ := entry["id"].(string)
		ids[id] = true
		if entry["object"] != "model" {
			t.Fatalf("entry object = %v", entry["object"])
		}
	}
	for _, want := range []string{"minimax-agent", "minimax-m3-thinking", "minimax-image"} {
		if !ids[want] {
			t.Fatalf("model list is missing %s: %v", want, ids)
		}
	}
}

func TestHealthReportsPoolSummary(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {})
	h.addAccount(t, "one", "token-a", 10)
	h.addAccount(t, "two", "token-b", 20)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	h.gateway.Health(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	payload := decodeJSON(t, rec)
	if payload["status"] != "ok" {
		t.Fatalf("status = %v", payload["status"])
	}
	summary, _ := payload["pool"].(map[string]any)
	if summary["total"] != float64(2) {
		t.Fatalf("pool total = %v, want 2", summary["total"])
	}
	if summary["routable"] != float64(2) {
		t.Fatalf("pool routable = %v, want 2", summary["routable"])
	}
}

func TestImageGenerationsReturnsMedia(t *testing.T) {
	h := newHarness(t, acceptsEverything(upstreamMedia("https://cdn/one.png", "https://cdn/two.png")))
	h.addAccount(t, "primary", "token-good", 10)

	raw, _ := json.Marshal(map[string]any{"prompt": "a blue circle", "n": 2})
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(raw))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+h.key)
	rec := httptest.NewRecorder()
	h.gateway.ImageGenerations(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	payload := decodeJSON(t, rec)
	data, _ := payload["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("data = %v, want 2 images", payload["data"])
	}
	first, _ := data[0].(map[string]any)
	if first["url"] != "https://cdn/one.png" {
		t.Fatalf("url = %v", first["url"])
	}
}

// An inline data URI cannot be fetched by the upstream, so the request must be
// refused with an actionable message rather than forwarded to fail opaquely.
func TestChatCompletionsRejectsInlineImageWithoutPublicBaseURL(t *testing.T) {
	h := newHarness(t, acceptsEverything(upstreamText("never reached")))
	h.addAccount(t, "primary", "token-good", 10)

	rec := h.chat(t, map[string]any{
		"model": "minimax-agent",
		"messages": []any{map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "text", "text": "what is this"},
				map[string]any{"type": "image_url", "image_url": map[string]any{
					"url": "data:image/png;base64,iVBORw0KGgo=",
				}},
			},
		}},
	}, h.key)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "public base URL") {
		t.Fatalf("error should explain how to fix it: %s", rec.Body.String())
	}
}

// A remote image URL is forwarded as an attachment without being downloaded.
func TestChatCompletionsForwardsRemoteImage(t *testing.T) {
	var body string
	h := newHarness(t, upstreamFunc(
		nil,
		func(string) string { return upstreamText("看到了") },
	))
	// Wrap the stub to capture the message payload.
	original := h.upstream.Config.Handler
	h.upstream.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/message") {
			raw, _ := io.ReadAll(r.Body)
			body = string(raw)
		}
		original.ServeHTTP(w, r)
	})
	h.addAccount(t, "primary", "token-good", 10)

	rec := h.chat(t, map[string]any{
		"model": "minimax-agent",
		"messages": []any{map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "text", "text": "what is this"},
				map[string]any{"type": "image_url", "image_url": map[string]any{
					"url": "https://example.com/cat.png",
				}},
			},
		}},
	}, h.key)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(body, "https://example.com/cat.png") {
		t.Fatalf("message body should carry the remote image: %s", body)
	}
}

func accountID(t *testing.T, h *harness, name string) string {
	t.Helper()
	for _, account := range h.store.ListAccounts() {
		if account.Name == name {
			return account.ID
		}
	}
	t.Fatalf("account %q not found", name)
	return ""
}

// Guard against the harness drifting from the real configuration defaults.
func TestHarnessUsesFastCapacityWait(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {})
	if got := h.settings.CapacityWait(); got != 0 {
		t.Fatalf("capacity wait = %v, want 0 so pool-exhaustion tests stay fast", got)
	}
	if h.settings.RequestTimeout() > 5*time.Second {
		t.Fatalf("request timeout = %v, tests should not hang", h.settings.RequestTimeout())
	}
}
