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
	"strconv"
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
		// Prepared, because an account that is not is held out of rotation: a
		// missing realUserID answers a bare 401, which this pool reads as a dead
		// token, and a missing agent id opens no session at all.
		UserID: "1", AgentID: "443154487857417",
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

// --- video generation ------------------------------------------------------

// captureMessages records the `content` of every message turn sent upstream.
//
// The video integration lives entirely in that string — the plugin reference and
// the generation parameters have no request field of their own — so asserting on
// the request body is the only way to test it.
func (h *harness) captureMessages(t *testing.T) *[]string {
	t.Helper()
	var seen []string
	original := h.upstream.Config.Handler
	h.upstream.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/message") {
			raw, _ := io.ReadAll(r.Body)
			var payload struct {
				Content string `json:"content"`
			}
			if json.Unmarshal(raw, &payload) == nil {
				seen = append(seen, payload.Content)
			}
		}
		original.ServeHTTP(w, r)
	})
	return &seen
}

// A video model is not a chat model, but it is accepted on the chat surface:
// most clients only speak /v1/chat/completions, and refusing them there would
// make the feature unreachable for exactly the callers who need it.
func TestChatCompletionsRoutesAVideoModelThroughThePlugin(t *testing.T) {
	h := newHarness(t, acceptsEverything(upstreamText("已提交")))
	h.addAccount(t, "primary", "token-good", 10)
	seen := h.captureMessages(t)

	rec := h.chat(t, map[string]any{
		"model":    "minimax-h3-max",
		"messages": []any{map[string]any{"role": "user", "content": "一只猫在弹钢琴"}},
	}, h.key)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(*seen) != 1 {
		t.Fatalf("expected one upstream turn, got %d", len(*seen))
	}
	text := (*seen)[0]

	if !strings.HasPrefix(text, "@video-creater ") {
		t.Errorf("turn does not open with the plugin reference:/n%s", text)
	}
	if !strings.Contains(text, "一只猫在弹钢琴") {
		t.Errorf("prompt was dropped:/n%s", text)
	}
	// The upstream reads the block only at the very end of the message.
	if !strings.HasSuffix(text, "</video-generation-options>") {
		t.Errorf("options block is not last:/n%s", text)
	}
	if !strings.Contains(text, `"model":"MiniMax-H3-Max"`) {
		t.Errorf("the selected model did not reach the options block:/n%s", text)
	}
}

// A chat model must not be rewritten: the plugin reference would send every
// ordinary conversation to a video plugin.
func TestChatCompletionsLeavesChatModelsAlone(t *testing.T) {
	h := newHarness(t, acceptsEverything(upstreamText("你好")))
	h.addAccount(t, "primary", "token-good", 10)
	seen := h.captureMessages(t)

	rec := h.chat(t, map[string]any{
		"model":    "minimax-agent",
		"messages": []any{map[string]any{"role": "user", "content": "你好"}},
	}, h.key)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(*seen) != 1 || (*seen)[0] != "你好" {
		t.Fatalf("chat prompt was rewritten: %v", *seen)
	}
}

func TestVideoGenerationsReturnsMedia(t *testing.T) {
	h := newHarness(t, acceptsEverything(upstreamMedia("https://cdn/clip.mp4")))
	h.addAccount(t, "primary", "token-good", 10)
	seen := h.captureMessages(t)

	raw, _ := json.Marshal(map[string]any{
		"model": "minimax-h3-max", "prompt": "一只猫在弹钢琴",
		"duration": 8, "ratio": "9:16", "resolution": "480P",
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/videos/generations", bytes.NewReader(raw))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+h.key)
	rec := httptest.NewRecorder()
	h.gateway.VideoGenerations(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	payload := decodeJSON(t, rec)
	if payload["status"] != "succeeded" {
		t.Errorf("status = %v, want succeeded", payload["status"])
	}
	data, _ := payload["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("data = %v, want one video", payload["data"])
	}
	first, _ := data[0].(map[string]any)
	if first["url"] != "https://cdn/clip.mp4" {
		t.Errorf("url = %v", first["url"])
	}

	// Every parameter the caller supplied has to survive into the options block.
	if len(*seen) != 1 {
		t.Fatalf("expected one upstream turn, got %d", len(*seen))
	}
	for _, want := range []string{`"duration":8`, `"ratio":"9:16"`, `"resolution":"480P"`, `"model":"MiniMax-H3-Max"`} {
		if !strings.Contains((*seen)[0], want) {
			t.Errorf("options block is missing %s:/n%s", want, (*seen)[0])
		}
	}
}

// Omitting a parameter must not stall the turn.
//
// The plugin's skill asks the user to confirm every unspecified choice before it
// generates, and a headless call has nobody to answer — so a gap in the options
// is not a harmless default, it is a request that never produces anything.
func TestVideoGenerationsFillsInEveryUnspecifiedParameter(t *testing.T) {
	h := newHarness(t, acceptsEverything(upstreamMedia("https://cdn/clip.mp4")))
	h.addAccount(t, "primary", "token-good", 10)
	seen := h.captureMessages(t)

	raw, _ := json.Marshal(map[string]any{"prompt": "一只猫在弹钢琴"})
	req := httptest.NewRequest(http.MethodPost, "/v1/videos/generations", bytes.NewReader(raw))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+h.key)
	rec := httptest.NewRecorder()
	h.gateway.VideoGenerations(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(*seen) != 1 {
		t.Fatalf("expected one upstream turn, got %d", len(*seen))
	}
	text := (*seen)[0]
	// Defaults come from the settings, so read them rather than hardcoding.
	defaults := config.DefaultSettings(t.TempDir()).Video
	for _, want := range []string{
		`"duration":` + strconv.Itoa(defaults.DefaultDuration),
		`"ratio":"` + defaults.DefaultRatio + `"`,
		`"resolution":"` + defaults.DefaultResolution + `"`,
		`"model":"MiniMax-H3-Max"`, // the default video model
	} {
		if !strings.Contains(text, want) {
			t.Errorf("options block is missing %s:/n%s", want, text)
		}
	}
}

// A caller that gets no video back has to be able to tell "still running" from
// "failed" without reading prose, because the slow model legitimately returns
// nothing within any timeout.
func TestVideoGenerationsReportsPendingWhenNothingIsProduced(t *testing.T) {
	h := newHarness(t, acceptsEverything(upstreamText("任务已提交，稍后生成完成")))
	h.addAccount(t, "primary", "token-good", 10)

	raw, _ := json.Marshal(map[string]any{"model": "minimax-h3", "prompt": "一只猫在弹钢琴"})
	req := httptest.NewRequest(http.MethodPost, "/v1/videos/generations", bytes.NewReader(raw))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+h.key)
	rec := httptest.NewRecorder()
	h.gateway.VideoGenerations(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("a slow generation is not an error: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	payload := decodeJSON(t, rec)
	if payload["status"] != "pending" {
		t.Errorf("status = %v, want pending", payload["status"])
	}
	if detail, _ := payload["detail"].(string); !strings.Contains(detail, "任务已提交") {
		t.Errorf("the agent's own answer was dropped: %v", payload["detail"])
	}
}

// Naming a chat model on the video endpoint would send an ordinary conversation
// to a plugin, so it is refused rather than silently accepted.
func TestVideoGenerationsRejectsNonVideoModels(t *testing.T) {
	h := newHarness(t, acceptsEverything(upstreamText("never reached")))
	h.addAccount(t, "primary", "token-good", 10)

	for _, model := range []string{"minimax-agent", "minimax-image", "does-not-exist"} {
		raw, _ := json.Marshal(map[string]any{"model": model, "prompt": "一只猫"})
		req := httptest.NewRequest(http.MethodPost, "/v1/videos/generations", bytes.NewReader(raw))
		req.Header.Set("content-type", "application/json")
		req.Header.Set("authorization", "Bearer "+h.key)
		rec := httptest.NewRecorder()
		h.gateway.VideoGenerations(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("model %q: status = %d, want 400", model, rec.Code)
		}
	}
}

func TestVideoGenerationsRequiresAPrompt(t *testing.T) {
	h := newHarness(t, acceptsEverything(upstreamText("never reached")))
	h.addAccount(t, "primary", "token-good", 10)

	raw, _ := json.Marshal(map[string]any{"model": "minimax-h3-max"})
	req := httptest.NewRequest(http.MethodPost, "/v1/videos/generations", bytes.NewReader(raw))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+h.key)
	rec := httptest.NewRecorder()
	h.gateway.VideoGenerations(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// captureMessageBodies records the whole body of every message turn.
//
// captureMessages keeps only `content`, which is enough for the prompt-shaped
// part of the integration. The parts that live in their own request fields —
// `client_intent` above all — are invisible to it.
func (h *harness) captureMessageBodies(t *testing.T) *[]map[string]any {
	t.Helper()
	var seen []map[string]any
	original := h.upstream.Config.Handler
	h.upstream.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/message") {
			raw, _ := io.ReadAll(r.Body)
			var payload map[string]any
			if json.Unmarshal(raw, &payload) == nil {
				seen = append(seen, payload)
			}
		}
		original.ServeHTTP(w, r)
	})
	return &seen
}

func (h *harness) video(t *testing.T, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/videos/generations", bytes.NewReader(raw))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+h.key)
	rec := httptest.NewRecorder()
	h.gateway.VideoGenerations(rec, req)
	return rec
}

// A video turn has to carry `client_intent`, and a chat turn must not.
//
// This asserts *what is sent*, not what it achieves. The web client labels
// video turns and we follow it; whether the label is what unlocks the video
// service is still unproven — a live run that carried it produced no tool call
// and no file. Do not read this test as evidence that the field does anything
// beyond travelling.
func TestVideoTurnsCarryTheVideoClientIntent(t *testing.T) {
	h := newHarness(t, acceptsEverything(upstreamText("已提交")))
	h.addAccount(t, "primary", "token-good", 10)
	bodies := h.captureMessageBodies(t)

	// The chat turn is sent first so its absence is asserted on the same run,
	// rather than assumed from a separate one.
	if rec := h.chat(t, map[string]any{
		"model":    "minimax-agent",
		"messages": []any{map[string]any{"role": "user", "content": "你好"}},
	}, h.key); rec.Code != http.StatusOK {
		t.Fatalf("chat status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec := h.video(t, map[string]any{"model": "minimax-h3-max", "prompt": "一只猫在弹钢琴"}); rec.Code != http.StatusOK {
		t.Fatalf("video status = %d, body = %s", rec.Code, rec.Body.String())
	}

	if len(*bodies) != 2 {
		t.Fatalf("captured %d turns, want 2", len(*bodies))
	}
	if got, present := (*bodies)[0]["client_intent"]; present {
		t.Errorf("an ordinary chat turn carried client_intent = %v", got)
	}
	if got := (*bodies)[1]["client_intent"]; got != minimax.VideoClientIntent {
		t.Errorf("video turn client_intent = %v, want %q", got, minimax.VideoClientIntent)
	}
}

// A finished video is not in the conversation stream, and a gateway that only
// reads the stream reports a success as emptiness.
//
// The file lands in the account's drive and is announced by a separate lookup:
// the turn's summaries carry its node id, and the drive's `download-url`
// sub-resource turns that node into a signed link. Both are plain GETs, so the
// cost of looking is one extra round trip on a turn that already cost points.
func TestVideoGenerationsFindsAFileThatOnlyTheDriveKnowsAbout(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/message"):
			w.Header().Set("content-type", "text/event-stream")
			_, _ = io.WriteString(w, upstreamText("已提交，正在生成"))
		case strings.HasSuffix(r.URL.Path, "/input-summaries"):
			w.Header().Set("content-type", "application/json")
			_, _ = io.WriteString(w, `{"summaries":[{"artifacts":[`+
				`{"category":"videos","file_ext":"mp4","mime_type":"video/mp4",`+
				`"name":"444160978935873.mp4","node_id":"444166461083748",`+
				`"size_bytes":801051,"created_at":9999999999999}]}],`+
				`"base_resp":{"status_code":0,"status_msg":"ok"}}`)
		case strings.HasSuffix(r.URL.Path, "/download-url"):
			w.Header().Set("content-type", "application/json")
			// The drive answers without a scheme; the gateway has to add one.
			_, _ = io.WriteString(w, `{"download_url":"matrix-internal.oss.example/Mavis/1/files/2/3.mp4?sig=x",`+
				`"base_resp":{"status_code":0,"status_msg":"ok"}}`)
		default:
			w.Header().Set("content-type", "application/json")
			_, _ = io.WriteString(w, `{"session_id":"sess_test"}`)
		}
	})
	h.addAccount(t, "primary", "token-good", 10)

	rec := h.video(t, map[string]any{"model": "minimax-h3-max", "prompt": "一只猫在弹钢琴"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	payload := decodeJSON(t, rec)
	if payload["status"] != "succeeded" {
		t.Fatalf("status = %v, want succeeded (body %s)", payload["status"], rec.Body.String())
	}
	if _, present := payload["detail"]; present {
		t.Errorf("a succeeded turn still carried prose: %v", payload["detail"])
	}
	data, _ := payload["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("data = %v, want one video", payload["data"])
	}
	first, _ := data[0].(map[string]any)
	if first["url"] != "https://matrix-internal.oss.example/Mavis/1/files/2/3.mp4?sig=x" {
		t.Errorf("url = %v, want the drive link with a scheme", first["url"])
	}
}

// A drive lookup that fails must not turn a good turn into an error, and must
// not be mistaken for "this turn produced nothing" either — the turn's own
// answer is what the caller gets.
func TestVideoGenerationsSurvivesADriveThatDoesNotAnswer(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/message"):
			w.Header().Set("content-type", "text/event-stream")
			_, _ = io.WriteString(w, upstreamText("已提交"))
		case strings.HasSuffix(r.URL.Path, "/input-summaries"):
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, "boom")
		default:
			w.Header().Set("content-type", "application/json")
			_, _ = io.WriteString(w, `{"session_id":"sess_test"}`)
		}
	})
	h.addAccount(t, "primary", "token-good", 10)

	rec := h.video(t, map[string]any{"model": "minimax-h3-max", "prompt": "一只猫在弹钢琴"})
	if rec.Code != http.StatusOK {
		t.Fatalf("a broken lookup is not the caller's error: status = %d, body = %s", rec.Code, rec.Body.String())
	}
	payload := decodeJSON(t, rec)
	if payload["status"] != "pending" {
		t.Errorf("status = %v, want pending", payload["status"])
	}
	if detail, _ := payload["detail"].(string); !strings.Contains(detail, "已提交") {
		t.Errorf("the agent's own answer was dropped: %v", payload["detail"])
	}
}
