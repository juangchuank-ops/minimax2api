package gateway

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"minimax2api/internal/config"
	"minimax2api/internal/minimax"
	"minimax2api/internal/pool"
	"minimax2api/internal/store"
)

// Version is reported by /health.
const Version = "0.1.0"

// Gateway exposes the OpenAI-compatible surface.
type Gateway struct {
	store    *store.Store
	pool     *pool.Pool
	client   *minimax.Client
	settings func() config.Settings
	limiter  *limiter
}

func New(st *store.Store, p *pool.Pool, client *minimax.Client, settings func() config.Settings) *Gateway {
	return &Gateway{
		store:    st,
		pool:     p,
		client:   client,
		settings: settings,
		limiter:  newLimiter(),
	}
}

// respWriter records whether the response has already been committed.
type respWriter struct {
	http.ResponseWriter
	wroteHeader bool
}

func (w *respWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *respWriter) Write(data []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

func (w *respWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// ------------------------------------------------------------- chat handler

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
	User     string        `json:"user"`
	Tools    []toolSpec    `json:"tools"`
}

type chatMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type contentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	ImageURL struct {
		URL string `json:"url"`
	} `json:"image_url"`
}

// ChatCompletions implements POST /v1/chat/completions.
func (g *Gateway) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	out := &respWriter{ResponseWriter: w}
	started := time.Now()

	key, err := g.authenticate(r)
	if err != nil {
		writeError(out, http.StatusUnauthorized, err.Error())
		return
	}
	if err := g.limiter.acquire(key); err != nil {
		writeError(out, http.StatusTooManyRequests, err.Error())
		return
	}
	defer g.limiter.release(key)

	body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil {
		writeError(out, http.StatusBadRequest, "cannot read request body")
		return
	}

	var request chatRequest
	if err := json.Unmarshal(body, &request); err != nil {
		writeError(out, http.StatusBadRequest, "invalid JSON body")
		return
	}

	modelID := request.Model
	if modelID == "" {
		modelID = "minimax-agent"
	}
	model, ok := g.store.ModelByID(modelID)
	if !ok || !model.Enabled {
		writeError(out, http.StatusBadRequest, fmt.Sprintf("unknown or disabled model %q", modelID))
		return
	}
	// Every catalogue type is reachable here, and that is the point of the
	// catalogue rather than an accident. `/v1/models` cannot say what a model
	// *is* — the schema is id/object/created/owned_by and nothing else — so a
	// chat client fills its model picker with every id it finds and sends the
	// user's message to this endpoint whichever one was picked. A model that is
	// listed here and then refused here is therefore a trap, not a boundary:
	// the caller sees a 400 and has no way to tell it apart from a broken
	// gateway. Video entries were already accepted (they are rewritten into a
	// plugin reference); image entries are dispatched to the image turn below.
	// Only a type this build does not know is rejected.
	switch model.Type {
	case store.ModelTypeChat, store.ModelTypeVideo, store.ModelTypeImage:
	default:
		writeError(out, http.StatusBadRequest, fmt.Sprintf("model %q is not a chat model", modelID))
		return
	}

	prompt, images, err := g.buildPrompt(r.Context(), request.Messages)
	if err != nil {
		writeError(out, http.StatusBadRequest, err.Error())
		return
	}
	if prompt == "" && len(images) == 0 {
		writeError(out, http.StatusBadRequest, "messages must contain text or image content")
		return
	}

	if model.Type == store.ModelTypeImage {
		g.chatImage(out, r, key, model, request, prompt, images, body, started)
		return
	}

	// Every catalogue entry reaches the same upstream agent, so the model id is
	// not forwarded. It only steers local behaviour: the thinking variant asks
	// the adapter to surface the reasoning stream, and a video entry rewrites the
	// turn into a plugin reference with its generation parameters attached.
	mode := ""
	if model.ID == "minimax-m3-thinking" {
		mode = "think"
	}

	settings := g.settings()
	prompt = g.outgoingText(settings, model, prompt, minimax.VideoOptions{})

	// Tool declarations ride along in the turn text, because the upstream body
	// has no field that could carry them. tools.go records the evidence and the
	// cost of the emulation; nothing here should be read as native support.
	toolsEnabled := model.Type == store.ModelTypeChat && hasToolDeclarations(request.Tools)
	if toolsEnabled {
		prompt = injectTools(prompt, request.Tools)
	}

	splitter := newToolSplitter(toolsEnabled)
	var onDelta, onThinking func(string)

	var chunkMu sync.Mutex
	writeChunk := func(delta map[string]any, finish any) {
		chunkMu.Lock()
		defer chunkMu.Unlock()
		ensureStreamHeaders(out)
		writeChunkRaw(out, model, delta, finish)
		out.Flush()
	}

	if request.Stream {
		onDelta = func(text string) {
			if safe := splitter.push(text); safe != "" {
				writeChunk(map[string]any{"content": safe}, nil)
			}
		}
		onThinking = func(text string) {
			writeChunk(map[string]any{"reasoning_content": text}, nil)
		}
		// The role frame opens the message and goes out before the upstream is
		// called. That also means a streaming request has committed by the time
		// its first attempt is made: a retry would append a second answer to a
		// message the caller has already begun reading.
		writeChunk(map[string]any{"role": "assistant", "content": ""}, nil)
	}

	turn, lastErr := g.runTurn(r.Context(), turnRequest{
		Prompt:     prompt,
		Images:     images,
		Mode:       mode,
		Model:      model,
		SessionKey: request.User,
		Started:    started,
		OnDelta:    onDelta,
		OnThinking: onThinking,
		Committed:  func() bool { return out.wroteHeader },
	})

	accountName := turn.AccountName
	promptTokens := estimateTokens(prompt)
	if lastErr != nil {
		g.recordAudit(r, key, model, accountName, started, http.StatusBadGateway, 0, promptTokens, 0, request.Stream, turn.Retries, string(body), "", lastErr.Error())
		if out.wroteHeader {
			writeSSEError(out, lastErr.Error())
			return
		}
		writeError(out, http.StatusBadGateway, lastErr.Error())
		return
	}
	result := turn.Result
	if result == nil {
		writeError(out, http.StatusBadGateway, "upstream returned no result")
		return
	}

	// A call block is removed from the prose only when it parsed. A block that
	// is present but unreadable stays where it is: deleting text the caller
	// never got to see would be worse than showing it.
	text, calls := result.Text, []toolCall(nil)
	if toolsEnabled {
		text, calls = splitToolCalls(result.Text)
	}

	completionTokens := estimateTokens(result.Text + result.Thinking)
	g.store.RecordModelUsage(model.ID, 1, int64(promptTokens+completionTokens))
	g.store.BumpClientKeyUsage(key.ID)

	if request.Stream {
		if turn.Streamed {
			// The splitter owns the tail: every byte already emitted came out of
			// it, so only it knows what is still being held back.
			tail, held := splitter.finish()
			if tail != "" {
				writeChunk(map[string]any{"content": tail}, nil)
			}
			if held != nil {
				calls = held
			}
		} else {
			if result.Thinking != "" {
				writeChunk(map[string]any{"reasoning_content": result.Thinking}, nil)
			}
			if text != "" {
				writeChunk(map[string]any{"content": text}, nil)
			}
		}
		mediaURLs := g.persistMediaList(result.Media, prompt, model.ID, accountName)
		g.finishStream(out, model, mediaURLs, calls)
		g.recordAudit(r, key, model, accountName, started, http.StatusOK, turn.FirstToken, promptTokens, completionTokens, true, turn.Retries, string(body), result.Text, "")
		return
	}

	message := map[string]any{
		"role":              "assistant",
		"content":           text,
		"reasoning_content": result.Thinking,
	}
	finishReason := "stop"
	if len(calls) > 0 {
		message["tool_calls"] = toolCallsPayload(calls)
		finishReason = "tool_calls"
	}

	response := map[string]any{
		"id":      "chatcmpl-" + randomID(12),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model.ID,
		"choices": []any{
			map[string]any{
				"index":         0,
				"message":       message,
				"finish_reason": finishReason,
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      promptTokens + completionTokens,
		},
	}
	if len(result.Media) > 0 {
		response["media"] = g.persistMediaList(result.Media, prompt, model.ID, accountName)
	}

	raw, _ := json.Marshal(response)
	g.recordAudit(r, key, model, accountName, started, http.StatusOK, turn.FirstToken, promptTokens, completionTokens, false, turn.Retries, string(body), string(raw), "")
	out.Header().Set("content-type", "application/json")
	out.WriteHeader(http.StatusOK)
	_, _ = out.Write(raw)
}

// turnRequest is one call to the upstream: what to send, which pool to draw
// from, and how deltas leave.
type turnRequest struct {
	Prompt     string
	Images     []minimax.UploadedImage
	Mode       string
	Model      *store.ModelConfig
	SessionKey string
	Started    time.Time
	OnDelta    func(string)
	OnThinking func(string)
	// Committed reports whether the response has already begun. A retry is only
	// safe while nothing has been written: once the caller has seen a byte, a
	// second account would append a second answer to the first.
	Committed func() bool
}

// turnResult carries what the turn produced plus the bookkeeping an audit row
// needs.
type turnResult struct {
	Result      *minimax.Result
	AccountName string
	FirstToken  int64
	Retries     int
	Streamed    bool
}

// runTurn leases an account, calls the upstream, and retries across accounts
// while nothing has been committed to the client.
//
// Every endpoint shares this one path. It is a single function rather than two
// so that failover, cooldown accounting and token estimation cannot drift apart
// between the OpenAI and Anthropic front ends.
func (g *Gateway) runTurn(ctx context.Context, req turnRequest) (*turnResult, error) {
	settings := g.settings()
	out := &turnResult{}

	var (
		mu         sync.Mutex
		firstToken int64
		streamed   bool
	)

	markFirst := func() {
		mu.Lock()
		defer mu.Unlock()
		if firstToken == 0 {
			firstToken = time.Since(req.Started).Milliseconds()
		}
		streamed = true
	}

	var lastErr error
	for attempt := 0; attempt < settings.Routing.MaxAttempts; attempt++ {
		lease, err := g.pool.Acquire(ctx, req.SessionKey)
		if err != nil {
			return out, err
		}
		out.AccountName = displayName(lease.Account)
		if attempt > 0 {
			out.Retries = attempt
		}

		opts := minimax.Options{
			Credential:  CredentialOf(lease.Account),
			Text:        req.Prompt,
			Mode:        req.Mode,
			Images:      req.Images,
			Timeout:     timeoutFor(settings, req.Model),
			IdleTimeout: settings.StreamIdleTimeout(),
		}
		if req.OnDelta != nil {
			opts.OnDelta = func(text string) {
				markFirst()
				req.OnDelta(text)
			}
		}
		if req.OnThinking != nil {
			opts.OnThinking = func(text string) {
				markFirst()
				req.OnThinking(text)
			}
		}

		result, err := g.client.Completion(ctx, opts)
		lease.Release(err)

		lastErr = err
		if err == nil {
			mu.Lock()
			out.FirstToken, out.Streamed = firstToken, streamed
			mu.Unlock()
			if out.FirstToken == 0 {
				out.FirstToken = time.Since(req.Started).Milliseconds()
			}
			out.Result = result
			return out, nil
		}
		if ctx.Err() != nil || isPoolExhausted(err) {
			break
		}
		if req.Committed != nil && req.Committed() {
			break
		}
	}

	mu.Lock()
	out.FirstToken, out.Streamed = firstToken, streamed
	mu.Unlock()
	return out, lastErr
}

// ensureStreamHeaders writes the SSE preamble exactly once, so every caller
// that emits a chunk gets the same headers no matter who writes first.
func ensureStreamHeaders(out *respWriter) {
	if out.wroteHeader {
		return
	}
	out.Header().Set("content-type", "text/event-stream")
	out.Header().Set("cache-control", "no-cache")
	out.Header().Set("connection", "keep-alive")
	out.Header().Set("x-accel-buffering", "no")
	out.WriteHeader(http.StatusOK)
}

// finishStream terminates the SSE response with whatever is left: media URLs
// and any tool calls the model asked for.
//
// Text that never streamed is written by the caller, not here — it needs the
// same call-block handling as the streamed path, and splitting that logic in
// two is how the two paths drift apart.
func (g *Gateway) finishStream(out *respWriter, model *store.ModelConfig, mediaURLs []string, calls []toolCall) {
	ensureStreamHeaders(out)
	if len(mediaURLs) > 0 {
		writeChunkRaw(out, model, map[string]any{"media": mediaURLs}, nil)
	}
	if len(calls) > 0 {
		writeChunkRaw(out, model, map[string]any{"tool_calls": streamedToolCallDeltas(calls)}, "tool_calls")
	} else {
		writeChunkRaw(out, model, map[string]any{}, "stop")
	}
	_, _ = io.WriteString(out, "data: [DONE]\n\n")
	out.Flush()
}

// streamedToolCallDeltas renders calls in the incremental shape OpenAI streams.
// The arguments arrive whole rather than token by token; a client concatenates
// deltas, so one complete delta is a valid sequence of one.
func streamedToolCallDeltas(calls []toolCall) []map[string]any {
	out := make([]map[string]any, 0, len(calls))
	for index, call := range calls {
		out = append(out, map[string]any{
			"index": index,
			"id":    call.ID,
			"type":  "function",
			"function": map[string]any{
				"name":      call.Name,
				"arguments": call.Arguments,
			},
		})
	}
	return out
}

func writeChunkRaw(out *respWriter, model *store.ModelConfig, delta map[string]any, finish any) {
	payload := map[string]any{
		"id":      "chatcmpl-" + randomID(12),
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model.ID,
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
	}
	raw, _ := json.Marshal(payload)
	_, _ = fmt.Fprintf(out, "data: %s\n\n", raw)
}

// ------------------------------------------------------------ prompt build

func (g *Gateway) buildPrompt(ctx context.Context, messages []chatMessage) (string, []minimax.UploadedImage, error) {
	if len(messages) == 0 {
		return "", nil, nil
	}
	var builder strings.Builder
	var images []minimax.UploadedImage

	for _, message := range messages {
		text, urls := parseContent(message.Content)
		switch message.Role {
		case "system":
			builder.WriteString("[系统指令] " + text + "\n\n")
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

// resolveImage turns an OpenAI image reference into something the agent can
// fetch on its own.
//
// Remote URLs pass through untouched. Inline data URIs are written into the
// media directory and republished under Media.PublicBaseURL, because the agent
// downloads attachments server-side and cannot dereference a data: URI. When no
// public base URL is configured the request is rejected with an actionable
// message rather than being forwarded to fail opaquely upstream.
func (g *Gateway) resolveImage(raw string) (minimax.UploadedImage, error) {
	if !strings.HasPrefix(raw, "data:") {
		return minimax.UploadedImage{URL: raw, Name: imageName(raw)}, nil
	}

	index := strings.Index(raw, ",")
	if index < 0 {
		return minimax.UploadedImage{}, fmt.Errorf("invalid data URI")
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw[index+1:]))
	if err != nil {
		return minimax.UploadedImage{}, fmt.Errorf("invalid base64 image payload: %w", err)
	}

	settings := g.settings()
	if strings.TrimSpace(settings.Media.PublicBaseURL) == "" {
		return minimax.UploadedImage{}, fmt.Errorf("inline base64 images require the media public base URL to be configured; otherwise send a publicly reachable image URL")
	}
	if err := os.MkdirAll(settings.Media.GeneratedDir, 0o755); err != nil {
		return minimax.UploadedImage{}, fmt.Errorf("prepare media directory: %w", err)
	}

	name := "inline_" + randomID(12) + ".png"
	target := filepath.Join(settings.Media.GeneratedDir, name)
	if err := os.WriteFile(target, decoded, 0o644); err != nil {
		return minimax.UploadedImage{}, fmt.Errorf("store inline image: %w", err)
	}
	return minimax.UploadedImage{
		URL:  strings.TrimRight(settings.Media.PublicBaseURL, "/") + "/" + name,
		Name: name,
	}, nil
}

// imageName derives a filename from a URL, falling back to a stable default.
func imageName(raw string) string {
	index := strings.LastIndex(raw, "/")
	if index < 0 {
		return "image.png"
	}
	candidate := raw[index+1:]
	if query := strings.Index(candidate, "?"); query >= 0 {
		candidate = candidate[:query]
	}
	if candidate == "" {
		return "image.png"
	}
	return candidate
}

func parseContent(raw json.RawMessage) (string, []string) {
	if len(raw) == 0 {
		return "", nil
	}
	var plain string
	if err := json.Unmarshal(raw, &plain); err == nil {
		return plain, nil
	}
	var parts []contentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", nil
	}
	var text strings.Builder
	var urls []string
	for _, part := range parts {
		switch part.Type {
		case "text", "input_text":
			text.WriteString(part.Text)
		case "image_url", "input_image":
			if part.ImageURL.URL != "" {
				urls = append(urls, part.ImageURL.URL)
			}
		}
	}
	return text.String(), urls
}

// ------------------------------------------------------------ image handler

// imageModelID is the catalogue entry image generation runs under.
//
// One id, not a family: image generation is a single capability upstream, so
// there is nothing to choose between. The entry still earns its place — it is
// what the console can switch off and what usage is counted against.
const imageModelID = "minimax-image"

type imageRequest struct {
	Prompt string `json:"prompt"`
	// N and Model are accepted and ignored.
	//
	// Image generation has exactly one model, and how many pictures come back
	// is the upstream's decision rather than a parameter. Honouring either
	// could therefore only ever mean rejecting the value the client guessed.
	// Read `imageModelID` for what actually runs.
	N              int    `json:"n"`
	ResponseFormat string `json:"response_format"`
	Model          string `json:"model"`
}

// imageModel returns the catalogue entry image generation runs under.
func (g *Gateway) imageModel() (*store.ModelConfig, error) {
	model, ok := g.store.ModelByID(imageModelID)
	if !ok || !model.Enabled {
		return nil, errors.New("image generation is disabled")
	}
	return model, nil
}

// runImageTurn sends one image request through the pool and reports what came
// back. It does no authentication, no limiting and no auditing, because it is
// called from two endpoints whose request shapes have nothing in common and
// only agree from here down.
func (g *Gateway) runImageTurn(
	ctx context.Context,
	prompt string,
	images []minimax.UploadedImage,
) (*minimax.Result, string, error) {
	settings := g.settings()
	var (
		lastErr     error
		accountName string
		result      *minimax.Result
	)

	for attempt := 0; attempt < settings.Routing.MaxAttempts; attempt++ {
		lease, err := g.pool.Acquire(ctx, "")
		if err != nil {
			lastErr = err
			break
		}
		accountName = displayName(lease.Account)
		result, err = g.client.Completion(ctx, minimax.Options{
			Credential:  CredentialOf(lease.Account),
			Text:        prompt,
			Mode:        "image",
			Images:      images,
			Timeout:     settings.RequestTimeout(),
			IdleTimeout: settings.StreamIdleTimeout(),
		})
		lease.Release(err)

		lastErr = err
		if err == nil || isPoolExhausted(err) {
			break
		}
	}

	if lastErr != nil {
		return nil, accountName, lastErr
	}
	return result, accountName, nil
}

// chatImage answers a chat completion whose model is an image model.
//
// Both ends are translations. The request is a conversation, but an image turn
// has no use for one — only the flattened prompt and any attached picture
// survive. The response is a chat message, but the product is pictures, and an
// OpenAI chat response has nowhere to put one: the only shape every client
// already renders is markdown, so that is what the pictures travel in.
//
// A turn that produced nothing is still a 200 carrying the agent's own words.
// That is deliberate and matches the video endpoint: the agent is the only
// party that knows *why* nothing came back, and turning its explanation into an
// error code would throw away the one useful thing the turn produced.
func (g *Gateway) chatImage(
	out *respWriter,
	r *http.Request,
	key *store.ClientKey,
	model *store.ModelConfig,
	request chatRequest,
	prompt string,
	images []minimax.UploadedImage,
	body []byte,
	started time.Time,
) {
	result, accountName, err := g.runImageTurn(r.Context(), prompt, images)
	if err != nil {
		g.recordAudit(r, key, model, accountName, started, http.StatusBadGateway, 0, 0, 0, request.Stream, 0, string(body), "", err.Error())
		if out.wroteHeader {
			writeSSEError(out, err.Error())
			return
		}
		writeError(out, http.StatusBadGateway, err.Error())
		return
	}

	urls := g.absoluteMediaURLs(r, g.persistMediaList(result.Media, prompt, model.ID, accountName))
	content := mediaMarkdown(result.Text, urls)
	promptTokens := estimateTokens(prompt)

	g.store.RecordModelUsage(model.ID, 1, 0)
	g.store.BumpClientKeyUsage(key.ID)

	if request.Stream {
		// Nothing streamed as it arrived — an image turn is one blocking call —
		// so the whole message goes out as a single chunk before the stream is
		// closed.
		ensureStreamHeaders(out)
		writeChunkRaw(out, model, map[string]any{"content": content}, nil)
		out.Flush()
		g.finishStream(out, model, nil, nil)
		g.recordAudit(r, key, model, accountName, started, http.StatusOK, 0, promptTokens, 0, true, 0, string(body), content, "")
		return
	}

	response := map[string]any{
		"id":      "chatcmpl-" + randomID(12),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model.ID,
		"choices": []any{
			map[string]any{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": content},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     promptTokens,
			"completion_tokens": 0,
			"total_tokens":      promptTokens,
		},
	}
	if len(urls) > 0 {
		response["media"] = urls
	}
	raw, _ := json.Marshal(response)
	g.recordAudit(r, key, model, accountName, started, http.StatusOK, 0, promptTokens, 0, false, 0, string(body), string(raw), "")

	out.Header().Set("content-type", "application/json")
	out.WriteHeader(http.StatusOK)
	_, _ = out.Write(raw)
}

// mediaMarkdown renders one chat message out of what an image turn produced.
//
// The agent's prose goes first and the pictures after it, in that order,
// because the prose is where a refusal or a caveat lives and nobody reads a
// caveat placed underneath four images. A turn with neither still has to say
// something: an empty assistant message reads as a client-side bug and sends
// the caller looking in the wrong place.
func mediaMarkdown(prose string, urls []string) string {
	var builder strings.Builder
	if trimmed := strings.TrimSpace(prose); trimmed != "" {
		builder.WriteString(trimmed)
	}
	for _, url := range urls {
		if builder.Len() > 0 {
			builder.WriteString("\n\n")
		}
		builder.WriteString("![](" + url + ")")
	}
	if builder.Len() == 0 {
		return "The upstream turn produced neither an image nor an explanation."
	}
	return builder.String()
}

// absoluteMediaURLs makes stored media fetchable by the caller.
//
// Stored media is served from this gateway at a path, and a path is worthless
// in a chat message: the client renders markdown in its own context, where
// `/media/…` points at nothing. The configured public base URL wins when there
// is one, because that is the address the operator has declared reachable;
// otherwise the request's own origin is used, which is right both for a direct
// call and for a proxy that passes the original host through.
func (g *Gateway) absoluteMediaURLs(r *http.Request, urls []string) []string {
	base := strings.TrimRight(strings.TrimSpace(g.settings().Media.PublicBaseURL), "/")
	if base == "" {
		base = requestBase(r)
	}
	out := make([]string, 0, len(urls))
	for _, url := range urls {
		if base != "" && strings.HasPrefix(url, "/") {
			url = base + url
		}
		out = append(out, url)
	}
	return out
}

// requestBase reconstructs the origin the caller reached us on, preferring the
// proxy's view when there is one.
func requestBase(r *http.Request) string {
	if r == nil {
		return ""
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwarded := firstHeaderValue(r, "X-Forwarded-Proto"); forwarded == "http" || forwarded == "https" {
		scheme = forwarded
	}
	host := firstHeaderValue(r, "X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	if host == "" {
		return ""
	}
	return scheme + "://" + host
}

// firstHeaderValue reads the first entry of a possibly comma-joined header.
func firstHeaderValue(r *http.Request, name string) string {
	raw := r.Header.Get(name)
	if index := strings.Index(raw, ","); index >= 0 {
		raw = raw[:index]
	}
	return strings.TrimSpace(raw)
}

// ImageGenerations implements POST /v1/images/generations.
func (g *Gateway) ImageGenerations(w http.ResponseWriter, r *http.Request) {
	out := &respWriter{ResponseWriter: w}
	started := time.Now()

	key, err := g.authenticate(r)
	if err != nil {
		writeError(out, http.StatusUnauthorized, err.Error())
		return
	}
	if err := g.limiter.acquire(key); err != nil {
		writeError(out, http.StatusTooManyRequests, err.Error())
		return
	}
	defer g.limiter.release(key)

	body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	var request imageRequest
	if err := json.Unmarshal(body, &request); err != nil {
		writeError(out, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(request.Prompt) == "" {
		writeError(out, http.StatusBadRequest, "prompt is required")
		return
	}

	model, err := g.imageModel()
	if err != nil {
		writeError(out, http.StatusServiceUnavailable, err.Error())
		return
	}

	settings := g.settings()
	result, accountName, err := g.runImageTurn(r.Context(), request.Prompt, nil)
	if err != nil {
		g.recordAudit(r, key, model, accountName, started, http.StatusBadGateway, 0, 0, 0, false, 0, string(body), "", err.Error())
		writeError(out, http.StatusBadGateway, err.Error())
		return
	}

	urls := g.persistMediaList(result.Media, request.Prompt, model.ID, accountName)
	items := make([]any, 0, len(urls))
	for index, url := range urls {
		if request.ResponseFormat == "b64_json" {
			path := filepath.Join(settings.Media.GeneratedDir, filepath.Base(url))
			if data, err := os.ReadFile(path); err == nil {
				items = append(items, map[string]any{"b64_json": base64.StdEncoding.EncodeToString(data)})
				continue
			}
		}
		source := ""
		if index < len(result.Media) {
			source = result.Media[index].URL
		}
		items = append(items, map[string]any{"url": url, "source_url": source})
	}

	g.store.RecordModelUsage(model.ID, 1, 0)
	g.store.BumpClientKeyUsage(key.ID)
	response := map[string]any{"created": time.Now().Unix(), "data": items}
	raw, _ := json.Marshal(response)
	g.recordAudit(r, key, model, accountName, started, http.StatusOK, 0, 0, 0, false, 0, string(body), string(raw), "")

	out.Header().Set("content-type", "application/json")
	out.WriteHeader(http.StatusOK)
	_, _ = out.Write(raw)
}

// ------------------------------------------------------------ video handler

// videoRequest is the body of POST /v1/videos/generations.
//
// The shape follows the OpenAI image endpoint rather than any MiniMax one,
// because there is no MiniMax video endpoint to follow: the parameters below
// are the four keys the plugin's own options block accepts, plus the prompt.
type videoRequest struct {
	Prompt string `json:"prompt"`
	Model  string `json:"model"`
	// Duration is whole seconds. It has no default worth guessing: the upstream
	// bills by it, so an absent value falls back to the console setting and is
	// reported back in the response.
	Duration   int    `json:"duration"`
	Ratio      string `json:"ratio"`
	Resolution string `json:"resolution"`
	// ImageURL is the first frame for image-to-video, and ImageURLs carries a
	// multi-reference set for the models that accept one.
	ImageURL  string   `json:"image_url"`
	ImageURLs []string `json:"image_urls"`
}

// VideoGenerations implements POST /v1/videos/generations.
//
// This is a synchronous surface onto an asynchronous upstream. The fast H3
// variant finishes in about twenty seconds and returns a playable URL in the
// same turn, which is what this endpoint is for. The slow H3.0 model is
// documented at 15–30 minutes, and no HTTP response can hold that: the call
// returns whatever the agent has produced by the timeout — usually a task
// identifier and a status — so the caller learns the task was accepted and
// nothing was lost. Treating a slow model as if it were fast would only turn a
// usable partial answer into a gateway timeout.
func (g *Gateway) VideoGenerations(w http.ResponseWriter, r *http.Request) {
	out := &respWriter{ResponseWriter: w}
	started := time.Now()

	key, err := g.authenticate(r)
	if err != nil {
		writeError(out, http.StatusUnauthorized, err.Error())
		return
	}
	if err := g.limiter.acquire(key); err != nil {
		writeError(out, http.StatusTooManyRequests, err.Error())
		return
	}
	defer g.limiter.release(key)

	body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	var request videoRequest
	if err := json.Unmarshal(body, &request); err != nil {
		writeError(out, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(request.Prompt) == "" {
		writeError(out, http.StatusBadRequest, "prompt is required")
		return
	}

	settings := g.settings()

	modelID := request.Model
	if modelID == "" {
		modelID = defaultVideoModel
	}
	model, ok := g.store.ModelByID(modelID)
	if !ok || !model.Enabled || model.Type != store.ModelTypeVideo {
		writeError(out, http.StatusBadRequest, fmt.Sprintf("unknown or disabled video model %q", modelID))
		return
	}

	options, err := (minimax.VideoOptions{
		Model:      model.UpstreamModel,
		Ratio:      request.Ratio,
		Resolution: request.Resolution,
		Duration:   request.Duration,
	}).Normalize(minimax.VideoOptions{
		Ratio:      settings.Video.DefaultRatio,
		Resolution: settings.Video.DefaultResolution,
		Duration:   settings.Video.DefaultDuration,
	})
	if err != nil {
		writeError(out, http.StatusBadRequest, err.Error())
		return
	}

	// Reference frames ride along as ordinary attachments; the plugin reads them
	// from the turn rather than from the options block, which only carries the
	// four generation parameters.
	images := make([]minimax.UploadedImage, 0, len(request.ImageURLs)+1)
	for _, raw := range append([]string{request.ImageURL}, request.ImageURLs...) {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		image, err := g.resolveImage(raw)
		if err != nil {
			writeError(out, http.StatusBadRequest, err.Error())
			return
		}
		images = append(images, image)
	}

	prompt := g.outgoingText(settings, model, request.Prompt, options)

	var (
		lastErr     error
		accountName string
		usedCred    minimax.Credential
		result      *minimax.Result
	)

	for attempt := 0; attempt < settings.Routing.MaxAttempts; attempt++ {
		lease, err := g.pool.Acquire(r.Context(), "")
		if err != nil {
			lastErr = err
			break
		}
		accountName = displayName(lease.Account)
		usedCred = CredentialOf(lease.Account)
		result, err = g.client.Completion(r.Context(), minimax.Options{
			Credential:   usedCred,
			Text:         prompt,
			Images:       images,
			ClientIntent: minimax.VideoClientIntent,
			Timeout:      settings.VideoTimeout(),
			IdleTimeout:  settings.StreamIdleTimeout(),
		})
		lease.Release(err)
		lastErr = err
		if err == nil || isPoolExhausted(err) {
			break
		}
	}

	if lastErr != nil {
		g.recordAudit(r, key, model, accountName, started, http.StatusBadGateway, 0, 0, 0, false, 0, string(body), "", lastErr.Error())
		writeError(out, http.StatusBadGateway, lastErr.Error())
		return
	}

	// The file a video turn produces is not in the stream. It lands in the
	// account's drive and is announced by a separate lookup, so a turn can
	// succeed and still look empty from here. Both sources are merged rather
	// than one replacing the other: the stream has been known to carry a cover
	// image, and dropping it because the drive answered too would lose the only
	// thing that did arrive. A failed lookup is swallowed on purpose — the
	// turn's own result is the answer, and a broken lookup must not become an
	// error the caller has to decode.
	if result.SessionID != "" {
		if fromDrive, err := g.client.SessionMedia(r.Context(), usedCred, result.SessionID, started.UnixMilli()); err == nil {
			result.Media = mergeMedia(result.Media, fromDrive)
		}
	}

	urls := g.persistMediaList(result.Media, request.Prompt, model.ID, accountName)
	items := make([]any, 0, len(urls))
	for index, url := range urls {
		source := ""
		if index < len(result.Media) {
			source = result.Media[index].URL
		}
		items = append(items, map[string]any{"url": url, "source_url": source})
	}

	g.store.RecordModelUsage(model.ID, 1, 0)
	g.store.BumpClientKeyUsage(key.ID)

	response := map[string]any{
		"created": time.Now().Unix(),
		"model":   model.ID,
		"data":    items,
	}
	// A turn that produced no media still returns 200 with an empty `data` and
	// the agent's own words, because "submitted, here is the task" is a useful
	// answer and an error code is not.
	//
	// `status` is deliberately only two-valued: it reports what this turn
	// produced, which is the one thing the gateway actually knows. It does NOT
	// distinguish "a task is running" from "the upstream never executed
	// anything". That difference lives only in the agent's prose, and a live
	// check confirmed the second case is real — the account had no connector
	// tool available, the turn still cost points, and nothing was ever
	// produced. Guessing between the two by matching keywords in the prose
	// would be worse than admitting the limit, so `detail` carries the prose
	// and the caller reads it.
	if len(items) > 0 {
		response["status"] = "succeeded"
	} else {
		response["status"] = "pending"
		response["detail"] = result.Text
	}
	raw, _ := json.Marshal(response)
	g.recordAudit(r, key, model, accountName, started, http.StatusOK, 0, 0, 0, false, 0, string(body), string(raw), "")

	out.Header().Set("content-type", "application/json")
	out.WriteHeader(http.StatusOK)
	_, _ = out.Write(raw)
}

// defaultVideoModel is used when a caller names none. It is the fast variant:
// the only video model that can answer a synchronous request, and the cheapest
// way for a caller to discover the endpoint works.
const defaultVideoModel = "minimax-h3-max"

// outgoingText turns a prompt into the text actually sent upstream.
//
// For chat models that is the prompt itself. For video models it is a different
// kind of message entirely: the plugin reference plus the generation parameters,
// because MiniMax-H3 is not selectable as a model and the parameters have no
// place in the request body. See internal/minimax/video.go.
func (g *Gateway) outgoingText(
	settings config.Settings,
	model *store.ModelConfig,
	prompt string,
	options minimax.VideoOptions,
) string {
	if model.Type != store.ModelTypeVideo {
		return prompt
	}
	if options.Model == "" {
		options.Model = model.UpstreamModel
	}
	if options.Ratio == "" {
		options.Ratio = settings.Video.DefaultRatio
	}
	if options.Resolution == "" {
		options.Resolution = settings.Video.DefaultResolution
	}
	if options.Duration <= 0 {
		options.Duration = settings.Video.DefaultDuration
	}
	return minimax.BuildVideoPrompt(prompt, settings.Video.PluginName, options, settings.Video.OptionsTag)
}

// timeoutFor picks the budget for one turn.
//
// A video turn and a chat turn have nothing in common: a chat turn that takes a
// minute is broken, and a video turn that takes a minute has not started yet.
func timeoutFor(settings config.Settings, model *store.ModelConfig) time.Duration {
	if model.Type == store.ModelTypeVideo {
		return settings.VideoTimeout()
	}
	return settings.RequestTimeout()
}

// Models implements GET /v1/models.
func (g *Gateway) Models(w http.ResponseWriter, r *http.Request) {
	if _, err := g.authenticate(r); err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	data := make([]any, 0, 4)
	for _, model := range g.store.ListModels() {
		if !model.Enabled {
			continue
		}
		data = append(data, map[string]any{"id": model.ID, "object": "model", "created": 0, "owned_by": "minimax"})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

// Health implements GET /health.
func (g *Gateway) Health(w http.ResponseWriter, r *http.Request) {
	total, active, cooldown, disabled, invalid, routable := g.pool.Summary()
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": Version,
		"pool": map[string]any{
			"total": total, "active": active, "cooldown": cooldown,
			"disabled": disabled, "invalid": invalid, "routable": routable,
		},
	})
}

// ------------------------------------------------------------- media helper

// mergeMedia concatenates two media lists, keeping the first occurrence of each
// URL.
//
// The stream and the drive describe the same turn from two angles, so an overlap
// is expected rather than exceptional — and a caller that receives the same video
// twice has no way to tell a duplicate from a second render.
func mergeMedia(primary, extra []minimax.MediaRef) []minimax.MediaRef {
	if len(extra) == 0 {
		return primary
	}
	seen := make(map[string]bool, len(primary)+len(extra))
	out := make([]minimax.MediaRef, 0, len(primary)+len(extra))
	for _, list := range [][]minimax.MediaRef{primary, extra} {
		for _, item := range list {
			if item.URL == "" || seen[item.URL] {
				continue
			}
			seen[item.URL] = true
			out = append(out, item)
		}
	}
	return out
}

func (g *Gateway) persistMediaList(media []minimax.MediaRef, prompt, model, account string) []string {
	urls := make([]string, 0, len(media))
	for _, item := range media {
		urls = append(urls, g.persistMedia(item, prompt, model, account))
	}
	return urls
}

func (g *Gateway) persistMedia(media minimax.MediaRef, prompt, model, account string) string {
	settings := g.settings()
	item := &store.MediaItem{
		ID:          "media_" + randomID(8),
		Kind:        media.Kind,
		SourceURL:   media.URL,
		URL:         media.URL,
		Prompt:      prompt,
		Model:       model,
		AccountName: account,
		CreatedAt:   time.Now(),
	}

	if settings.Media.AutoDownload {
		extension := ".bin"
		switch media.Kind {
		case "image":
			extension = ".png"
		case "video":
			extension = ".mp4"
		}
		filename := item.ID + extension
		target := filepath.Join(settings.Media.GeneratedDir, filename)

		// Downloads go through the same proxy as the API. Generated media is
		// served from MiniMax's CDN, and the account's egress fence applies to it
		// too — so fetching it from the local address fails the same way an
		// unproxied API call does, as a reset that looks like the CDN is down.
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, media.URL, nil)
		if err == nil {
			if resp, err := minimax.DownloadClient(settings).Do(req); err == nil {
				if resp.StatusCode < 400 {
					if file, err := os.Create(target); err == nil {
						if _, err := io.Copy(file, io.LimitReader(resp.Body, 256<<20)); err == nil {
							item.URL = "/media/" + filename
						} else {
							_ = os.Remove(target)
						}
						_ = file.Close()
					}
				}
				_ = resp.Body.Close()
			}
		}
		cancel()
	}

	g.store.AddMedia(item)
	return item.URL
}

// --------------------------------------------------------------- audit glue

func (g *Gateway) recordAudit(
	r *http.Request,
	key *store.ClientKey,
	model *store.ModelConfig,
	accountName string,
	started time.Time,
	status int,
	firstTokenMs int64,
	promptTokens, completionTokens int,
	stream bool,
	retries int,
	requestBody, responseBody, errText string,
) {
	settings := g.settings()
	audit := &store.Audit{
		ID:               "req_" + randomID(10),
		CreatedAt:        time.Now(),
		KeyName:          key.Name,
		Model:            model.ID,
		AccountName:      accountName,
		Status:           status,
		LatencyMs:        time.Since(started).Milliseconds(),
		FirstTokenMs:     firstTokenMs,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		Stream:           stream,
		Retries:          retries,
		IP:               clientIP(r),
		UserAgent:        r.Header.Get("user-agent"),
		Error:            errText,
	}
	if settings.Audit.RecordBody {
		audit.RequestBody = clampBody(requestBody, settings.Audit.BodyLimitBytes)
		audit.ResponseBody = clampBody(responseBody, settings.Audit.BodyLimitBytes)
	}
	g.store.AppendAudit(audit)
}

func clampBody(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}

// ------------------------------------------------------------------ helpers

func (g *Gateway) authenticate(r *http.Request) (*store.ClientKey, error) {
	raw := strings.TrimSpace(r.Header.Get("authorization"))
	if raw == "" {
		raw = strings.TrimSpace(r.Header.Get("x-api-key"))
	}
	if len(raw) > 7 && strings.EqualFold(raw[:7], "bearer ") {
		raw = strings.TrimSpace(raw[7:])
	}
	if raw == "" {
		return nil, fmt.Errorf("missing API key")
	}
	key, ok := g.store.ClientKeyByValue(raw)
	if !ok {
		return nil, fmt.Errorf("invalid API key")
	}
	if !key.Enabled {
		return nil, fmt.Errorf("API key disabled")
	}
	return key, nil
}

// CredentialOf projects a pooled account into the upstream identity bundle.
// It is exported because the admin console probes accounts with the same
// mapping, and a second copy of these eight fields would be a second place to
// forget when the protocol grows a field.
func CredentialOf(account *store.Account) minimax.Credential {
	return minimax.Credential{
		Region:       account.Region,
		Token:        account.Token,
		UserID:       account.UserID,
		AgentID:      account.AgentID,
		DeviceID:     account.DeviceID,
		UUID:         account.UUID,
		ScreenWidth:  account.ScreenWidth,
		ScreenHeight: account.ScreenHeight,
		BaseURL:      account.BaseURL,
	}
}

func displayName(account *store.Account) string {
	if account.Name != "" {
		return account.Name
	}
	if account.Identifier != "" {
		return account.Identifier
	}
	return store.MaskToken(account.Token)
}

func isPoolExhausted(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "no account available") || strings.Contains(message, "all accounts are busy")
}

func clientIP(r *http.Request) string {
	if forwarded := r.Header.Get("x-forwarded-for"); forwarded != "" {
		return strings.TrimSpace(strings.Split(forwarded, ",")[0])
	}
	host := r.RemoteAddr
	if index := strings.LastIndex(host, ":"); index >= 0 {
		host = host[:index]
	}
	return host
}

func randomID(length int) string {
	buf := make([]byte, (length+1)/2)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)[:length]
}

func estimateTokens(text string) int {
	if text == "" {
		return 0
	}
	return len([]rune(text))/2 + 1
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, "encode failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": message, "type": "minimax2api_error"}})
}

func writeSSEError(w http.ResponseWriter, message string) {
	raw, _ := json.Marshal(map[string]any{"error": map[string]any{"message": message}})
	_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", raw)
}
