package minimax

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"minimax2api/internal/config"
)

// Region values. MiniMax runs two independent deployments behind the same
// product: the mainland one (agent.minimaxi.com) and the international one
// (agent.minimax.io). Accounts registered with a mainland phone number live on
// the former, accounts registered with an email or an overseas number on the
// latter, and a token from one is rejected by the other — so the region is a
// per-account property, not a global setting.
const (
	RegionCN     = "cn"
	RegionGlobal = "global"
)

// Default paths. Both are exposed as runtime settings because the upstream
// bundle is free to rename them; a capture that disagrees can be pasted into
// the console without a rebuild.
const (
	// DefaultSessionPath opens a conversation.
	//
	// The `/minimax-cloud/api/v1` prefix is load-bearing. Drop it and the
	// request still answers **200 — with a page of the SPA's HTML** — because it
	// lands on the Next.js catch-all route. Nothing about that response says
	// "wrong path", so the mistake survives every check that only looks at the
	// status code.
	DefaultSessionPath = "/minimax-cloud/api/v1/agent/{agent_id}/session"
	// DefaultMessagePath sends one turn. It shares a prefix with the session
	// path but answers on a different host, which is derived rather than
	// hardcoded — see streamHostFor.
	DefaultMessagePath = "/minimax-cloud/api/v1/session/{session_id}/message"
)

// DefaultAgentID is deliberately empty.
//
// The obvious-looking "general" is an agent *role*, not an id. The upstream
// accepts it with a 200 that carries no session at all — a failure shaped
// exactly like a success. The real id is a number (`443154487857417` for the
// general agent of one account) and differs per account, so it is discovered
// from the agent list rather than guessed. See Client.FetchAgents.
const DefaultAgentID = ""

// ErrInvalidCredential marks a token the upstream rejected.
var ErrInvalidCredential = errors.New("minimax credential rejected")

// ErrAgentIDUnknown marks an account whose agent id has not been resolved yet.
//
// It is separate from ErrInvalidCredential on purpose. Both look like "this
// account cannot be used", but one is the token's fault and the other is a
// missing piece of preparation — and the pool retires an account that reports
// the first. An account that has simply never been prepared must not be
// retired for it.
var ErrAgentIDUnknown = errors.New("minimax agent id unknown")

// MediaRef is a generated image or video returned inside the SSE stream.
type MediaRef struct {
	Kind string `json:"kind"`
	URL  string `json:"url"`
}

// Credential is everything needed to impersonate one browser session.
//
// The metadata fields (UUID, DeviceID, screen size) are not decoration: the
// `yy` header is computed over the query string built from them, so a token
// only works when it travels with the fingerprint it was issued to.
type Credential struct {
	Region       string
	Token        string
	UserID       string
	AgentID      string
	DeviceID     string
	UUID         string
	ScreenWidth  int
	ScreenHeight int
	// BaseURL optionally pins this account to a specific host, which is how a
	// self-hosted relay or a captured alternative domain gets used.
	BaseURL string
}

// Label returns a stable human-readable identifier for logs and audits.
func (c Credential) Label() string {
	if c.UserID != "" {
		return c.UserID
	}
	if len(c.Token) > 12 {
		return c.Token[:12] + "…"
	}
	return c.Token
}

// Result is the aggregated outcome of one completion.
type Result struct {
	Text       string
	Thinking   string
	Media      []MediaRef
	SessionID  string
	MessageID  string
	TurnID     string
	StopReason string
}

// Options describes one upstream call.
type Options struct {
	Credential Credential
	Text       string
	Mode       string
	// ClientIntent labels the kind of turn for the backend.
	//
	// It is not decoration. A plain chat turn and a video-generation turn can
	// carry the identical `content` — same `@plugin` mention, same options
	// block — and still end differently: without an intent the backend hands
	// the turn to the agent, which then has to find a tool that can render
	// video and reports honestly when it cannot; with `video_generation` the
	// backend dispatches the turn straight to the video service. The two paths
	// look the same from the outside right up until one of them produces a file.
	ClientIntent  string
	Images        []UploadedImage
	Timeout       time.Duration
	IdleTimeout   time.Duration
	OnDelta       func(string)
	OnThinking    func(string)
	OnProgress    func(string)
	DisableStream bool
}

// UploadedImage is a remote image reference forwarded to the agent.
//
// MiniMax's attachment pipeline needs a signed upload we cannot reproduce from
// the outside, so images are passed through by URL and resolved upstream.
type UploadedImage struct {
	URL    string
	Name   string
	Width  int
	Height int
}

// Client talks to the MiniMax Agent web API.
type Client struct {
	http     *http.Client
	settings func() config.Settings
	// now is a test seam. `unix` and `yy` have to come from a single clock
	// reading, and the only way to prove they do is to hand the client a clock
	// that does not move. Nil means time.Now.
	now func() time.Time
}

// clock returns the client's time source. A zero-value Client is usable, which
// is why the nil case is handled here rather than at construction.
func (c *Client) clock() time.Time {
	if c.now == nil {
		return time.Now()
	}
	return c.now()
}

// New builds a client. settingsFn is re-read on every request so runtime
// configuration changes apply without a restart.
func New(settingsFn func() config.Settings) *Client {
	transport := &http.Transport{
		MaxIdleConns:        128,
		MaxIdleConnsPerHost: 32,
		IdleConnTimeout:     90 * time.Second,
	}
	return &Client{
		http: &http.Client{
			Transport: transport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) > 5 {
					return errors.New("too many redirects")
				}
				return nil
			},
		},
		settings: settingsFn,
	}
}

// ------------------------------------------------------------------ endpoint

func (c *Client) baseURL(settings config.Settings, cred Credential) string {
	if cred.BaseURL != "" {
		return strings.TrimRight(cred.BaseURL, "/")
	}
	if cred.Region == RegionCN && settings.Upstream.BaseURLCN != "" {
		return strings.TrimRight(settings.Upstream.BaseURLCN, "/")
	}
	return strings.TrimRight(settings.Upstream.BaseURL, "/")
}

// agentID resolves the agent to drive: the account's own id, else the global
// setting, else the default.
//
// A role name is never an id, and is skipped rather than used. The upstream
// answers one with a 200 that carries no session, so accepting `general` here
// makes every request look successful and produce nothing — and a settings file
// written by an earlier build holds exactly that value, because it was that
// build's default. Skipping it means such a file cannot take effect even before
// Normalize has repaired it.
func (c *Client) agentID(settings config.Settings, cred Credential) string {
	for _, candidate := range []string{cred.AgentID, settings.Upstream.AgentID} {
		if id := strings.TrimSpace(candidate); id != "" && !isAgentRole(id) {
			return id
		}
	}
	return DefaultAgentID
}

func sessionPath(settings config.Settings) string {
	if path := strings.TrimSpace(settings.Upstream.SessionPath); path != "" {
		return path
	}
	return DefaultSessionPath
}

func messagePath(settings config.Settings) string {
	if path := strings.TrimSpace(settings.Upstream.MessagePath); path != "" {
		return path
	}
	return DefaultMessagePath
}

// clientQuery renders the metadata query string in the exact order the captured
// web client sends it: 22 parameters, with `unix` fifth and `client`/`region`
// last.
//
// The order is part of the contract. `yy` is an MD5 over the encoded URL, so
// reordering the parameters changes the digest and the upstream rejects the
// request — with a message that names none of this. url.Values.Encode() sorts
// keys and therefore cannot be used here.
//
// Most of these are not load-bearing for routing; `uuid`, `device_id`,
// `user_id`, the screen size and the token are. The rest describe the same
// browser session and were present on the captured request, and sending a
// narrower set is what makes a request look like something other than the web
// client. They cost nothing.
//
// `unix` comes from the caller's clock rather than a reading taken here, because
// it has to be the same reading that feeds `yy`: a second time.Now() a
// millisecond later would sign a URL that was never sent.
func clientQuery(settings config.Settings, cred Credential, now time.Time) string {
	width := cred.ScreenWidth
	if width <= 0 {
		width = settings.Upstream.ScreenWidth
	}
	height := cred.ScreenHeight
	if height <= 0 {
		height = settings.Upstream.ScreenHeight
	}

	pairs := make([]string, 0, 24)
	add := func(key, value string) {
		if value == "" {
			return
		}
		pairs = append(pairs, key+"="+encodeURIComponent(value))
	}

	add("device_platform", "web")
	add("biz_id", "3")
	add("app_id", "3001")
	add("version_code", "22201")
	add("unix", strconv.FormatInt(now.UnixMilli(), 10))
	add("timezone_offset", "28800")
	add("sys_language", "en")
	add("lang", "en")
	add("uuid", cred.UUID)
	add("device_id", cred.DeviceID)
	add("os_name", "Windows")
	add("browser_name", "Chrome")
	add("device_memory", "16")
	add("cpu_core_num", "8")
	add("browser_language", "zh-CN")
	add("browser_platform", "Win32")
	add("user_id", cred.UserID)
	add("screen_width", strconv.Itoa(width))
	add("screen_height", strconv.Itoa(height))
	add("token", cred.Token)
	add("client", "web")
	add("region", "en")
	return strings.Join(pairs, "&")
}

// buildURLWith assembles the exact URL that will be requested, so the same
// string can be fed to the signature. Substitution happens here rather than
// through net/url so the query order survives byte for byte.
func (c *Client) buildURLWith(base string, settings config.Settings, cred Credential, path, sessionID string, now time.Time) string {
	path = strings.ReplaceAll(path, "{agent_id}", c.agentID(settings, cred))
	path = strings.ReplaceAll(path, "{session_id}", sessionID)

	rawURL := strings.TrimRight(base, "/") + path
	if query := clientQuery(settings, cred, now); query != "" {
		// A path that carries its own query needs "&", not a second "?". The
		// difference is invisible in the URL and fatal to the signature.
		if strings.Contains(rawURL, "?") {
			rawURL += "&" + query
		} else {
			rawURL += "?" + query
		}
	}
	return rawURL
}

// buildURL targets the account's API host.
func (c *Client) buildURL(settings config.Settings, cred Credential, path, sessionID string, now time.Time) string {
	return c.buildURLWith(c.baseURL(settings, cred), settings, cred, path, sessionID, now)
}

// streamBaseURL is the host the conversation endpoint answers on.
//
// The captured web client posts turns to `agent-stream.<domain>` while every
// other call stays on `agent.<domain>`. The gateway used the API host for both
// for a long time, and the two are not interchangeable: the same path there is a
// *different entry point*, one that reaches the agent but hands it no rendering
// tool — so every turn came back as a plausible reply with no video and no
// error to explain why.
//
// An explicit setting wins, so a capture that disagrees can be pasted into the
// console without a rebuild.
func streamBaseURL(settings config.Settings, fallback string) string {
	if explicit := strings.TrimSpace(settings.Upstream.StreamBaseURL); explicit != "" {
		return strings.TrimRight(explicit, "/")
	}
	return streamHostFor(fallback)
}

// streamHostFor maps an API host to its streaming counterpart.
//
// Only the `agent.<domain>` shape is recognised, because that is the only one
// that has been observed — and the alternative to deriving it is hardcoding a
// hostname, which would then be wrong for every other deployment. A self-hosted
// relay and a test server therefore keep their own address rather than having a
// name invented for them.
//
// The mainland deployment runs the same web client, so the same shape is applied
// to it. That is an inference rather than a capture, which is why it is allowed
// to stand: a hostname that does not exist is a DNS error, not a silent success,
// so being wrong about it is loud. Setting StreamBaseURL overrides all of this.
func streamHostFor(base string) string {
	parsed, err := url.Parse(base)
	if err != nil {
		return base
	}
	host := parsed.Hostname()
	if !strings.HasPrefix(host, "agent.") {
		return base
	}
	target := "agent-stream." + strings.TrimPrefix(host, "agent.")
	if port := parsed.Port(); port != "" {
		target = net.JoinHostPort(target, port)
	}
	parsed.Host = target
	return parsed.String()
}

// requestTarget names the endpoint a request is aimed at.
//
// Paths carry {agent_id} / {session_id} placeholders, which are substituted
// without going through net/url so the query order survives byte for byte.
type requestTarget struct {
	// Path is the endpoint template.
	Path string
	// SessionID fills {session_id}.
	SessionID string
	// Stream routes the call to the conversation host, which is a different
	// entry point from the API host rather than another address for the same
	// one. See streamBaseURL.
	Stream bool
}

// newRequest builds and signs a request.
//
// It takes a target rather than a finished URL because `unix`, `x-timestamp`
// and `yy` all have to come from one clock reading, and `yy` covers the encoded
// URL: assembling the URL anywhere else would need a second reading, and a few
// milliseconds of drift would make the digest describe a URL that was never
// sent. Keeping both halves here makes that impossible to get wrong from the
// outside.
func (c *Client) newRequest(ctx context.Context, settings config.Settings, cred Credential, method string, target requestTarget, body []byte) (*http.Request, error) {
	now := c.clock()

	base := c.baseURL(settings, cred)
	if target.Stream {
		base = streamBaseURL(settings, base)
	}
	rawURL := c.buildURLWith(base, settings, cred, target.Path, target.SessionID, now)

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return nil, err
	}

	bodyText := string(body)

	req.Header.Set("accept", "text/event-stream, application/json, */*")
	req.Header.Set("accept-language", settings.Upstream.Language)
	req.Header.Set("content-type", "application/json")
	req.Header.Set("origin", c.baseURL(settings, cred))
	req.Header.Set("referer", c.baseURL(settings, cred)+"/")
	req.Header.Set("user-agent", settings.Upstream.UserAgent)

	// The three headers the web bundle attaches to every API call.
	req.Header.Set("token", cred.Token)
	req.Header.Set("x-timestamp", strconv.FormatInt(now.Unix(), 10))
	req.Header.Set("x-signature", XSignature(now, bodyText))
	req.Header.Set("yy", YY(rawURL, bodyText, now))
	return req, nil
}

func (c *Client) do(req *http.Request, settings config.Settings) (*http.Response, error) {
	if settings.Upstream.Proxy == "" {
		return c.http.Do(req)
	}
	proxyURL, err := parseProxy(settings.Upstream.Proxy)
	if err != nil {
		return nil, err
	}
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: func(target *http.Request) (*url.URL, error) {
				// Loopback never goes through the proxy.
				//
				// A proxy is configured to reach the internet, and an upstream
				// on 127.0.0.1 is by definition not there. Sending it anyway
				// hands the request to a proxy that cannot reach it and gets
				// back `502` with an empty body — which reads as the upstream
				// being down rather than as a misrouted request. That is
				// exactly how a local test upstream fails when the console has
				// a proxy set, and it is worth being correct about beyond the
				// test: a per-account base URL pointing at a local mirror is a
				// supported thing to configure.
				if isLoopbackHost(target.URL.Hostname()) {
					return nil, nil
				}
				return proxyURL, nil
			},
			MaxIdleConns:        64,
			MaxIdleConnsPerHost: 16,
			IdleConnTimeout:     90 * time.Second,
		},
	}
	return client.Do(req)
}

// isLoopbackHost reports whether a host names the local machine.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// ------------------------------------------------------------ session create

// CreateSession opens a fresh upstream conversation and returns its id.
//
// One session per request is deliberate: the OpenAI surface is stateless, the
// client resends the whole history every turn, and reusing sessions would let
// context leak between unrelated callers.
func (c *Client) CreateSession(ctx context.Context, cred Credential) (string, error) {
	settings := c.settings()
	if strings.TrimSpace(cred.Token) == "" {
		return "", ErrInvalidCredential
	}
	// Refuse before spending a request. An empty agent id makes the URL
	// `/…/agent//session`, which the SPA catch-all answers with HTML and a 200 —
	// so the call would "succeed" while producing nothing usable.
	if agentID := c.agentID(settings, cred); strings.TrimSpace(agentID) == "" {
		return "", fmt.Errorf("%w: agent id unknown; the account has not been prepared", ErrAgentIDUnknown)
	}
	payload, err := json.Marshal(map[string]any{
		"agent_id":     c.agentID(settings, cred),
		"worktreeMode": false,
	})
	if err != nil {
		return "", err
	}

	req, err := c.newRequest(ctx, settings, cred, http.MethodPost,
		requestTarget{Path: sessionPath(settings)}, payload)
	if err != nil {
		return "", err
	}
	resp, err := c.do(req, settings)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", ErrInvalidCredential
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("minimax session HTTP %d: %s", resp.StatusCode, snippet(body))
	}

	id := sessionIDFrom(body)
	if id == "" {
		// A 200 without a session id is never a usable session: either the path
		// is wrong (an HTML shell came back) or the agent id is one the upstream
		// does not recognise. Say so, because the response itself will not.
		return "", fmt.Errorf("minimax session: no session id in the response (path %q, agent %q): %s",
			sessionPath(settings), c.agentID(settings, cred), snippet(body))
	}
	return id, nil
}

// sessionIDFrom digs a session identifier out of a create-session response.
//
// The shape has been observed as {"data":{"session_id":...}} but the field has
// also appeared at the top level, and a bare string body is possible, so all
// three are accepted rather than failing on a cosmetic upstream change.
func sessionIDFrom(body []byte) string {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return ""
	}
	var parsed any
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		// Not JSON at all. A wrong path is answered with the SPA's HTML shell
		// and a 200, and treating that page as the session id builds a URL out
		// of an entire web page — which the edge then rejects with a 400 that
		// looks like an upstream block rather than a bug on this side. Refuse
		// to guess instead.
		return ""
	}
	// A bare JSON string is a legitimate shape for this field upstream.
	if text, ok := parsed.(string); ok {
		return strings.TrimSpace(text)
	}
	for _, key := range []string{"session_id", "sessionId", "id", "sessionID"} {
		if found := deepString(parsed, key); found != "" {
			return found
		}
	}
	return ""
}

// ------------------------------------------------------------------ message

// Completion performs one upstream call and returns the aggregated result.
func (c *Client) Completion(ctx context.Context, opts Options) (*Result, error) {
	settings := c.settings()
	if strings.TrimSpace(opts.Credential.Token) == "" {
		return nil, ErrInvalidCredential
	}
	if opts.Text == "" && len(opts.Images) == 0 {
		return nil, errors.New("empty prompt")
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = settings.RequestTimeout()
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	sessionID, err := c.CreateSession(ctx, opts.Credential)
	if err != nil {
		return nil, err
	}
	return c.sendMessage(ctx, opts, sessionID)
}

// sendMessage posts the prompt to an existing session and consumes the stream.
func (c *Client) sendMessage(ctx context.Context, opts Options, sessionID string) (*Result, error) {
	settings := c.settings()
	cred := opts.Credential

	turnID := randomUUID()
	payload, err := json.Marshal(buildMessageBody(settings, opts, turnID))
	if err != nil {
		return nil, err
	}

	// Stream: the conversation endpoint answers on `agent-stream.<domain>`, and
	// the same path on the API host is a different entry point. See
	// streamBaseURL.
	req, err := c.newRequest(ctx, settings, cred, http.MethodPost, requestTarget{
		Path:      messagePath(settings),
		SessionID: sessionID,
		Stream:    true,
	}, payload)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req, settings)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, ErrInvalidCredential
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("minimax message HTTP %d: %s", resp.StatusCode, snippet(body))
	}

	result := &Result{SessionID: sessionID, TurnID: turnID}
	if err := c.consumeStream(resp.Body, opts, result); err != nil {
		return result, err
	}
	result.Media = dedupeMedia(result.Media)
	if result.Text == "" && result.Thinking == "" && len(result.Media) == 0 {
		return result, errors.New("upstream returned an empty response")
	}
	return result, nil
}

// buildMessageBody renders the request body for one turn.
//
// The agent expects the whole conversation in a single `content` string, which
// is what the gateway already produces when it flattens OpenAI messages.
func buildMessageBody(settings config.Settings, opts Options, turnID string) map[string]any {
	body := map[string]any{
		"content":      opts.Text,
		"turn_id":      turnID,
		"worktreeMode": false,
	}

	if len(opts.Images) > 0 {
		attachments := make([]any, 0, len(opts.Images))
		for _, image := range opts.Images {
			attachments = append(attachments, map[string]any{
				"type": "image",
				"url":  image.URL,
				"name": image.Name,
			})
		}
		body["attachments"] = attachments
	}

	// `model` is an object upstream, not a string. It is only attached when the
	// console supplies a template, because an empty object is rejected.
	if template := strings.TrimSpace(settings.Upstream.ModelPayload); template != "" {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(template), &parsed); err == nil {
			body["model"] = parsed
		}
	}
	if opts.Mode != "" {
		body["mode"] = opts.Mode
	}
	// Only sent when the caller names one. An invented intent would be worse
	// than no intent, and the plain chat path has always worked without it.
	if intent := strings.TrimSpace(opts.ClientIntent); intent != "" {
		body["client_intent"] = intent
	}
	return body
}

// ------------------------------------------------------------------- stream

func (c *Client) consumeStream(body io.ReadCloser, opts Options, result *Result) error {
	settings := c.settings()
	idle := opts.IdleTimeout
	if idle <= 0 {
		idle = settings.StreamIdleTimeout()
	}

	reader := bufio.NewReaderSize(body, 64*1024)
	var buffer strings.Builder
	idleTimer := time.AfterFunc(idle, func() { _ = body.Close() })
	defer idleTimer.Stop()

	chunk := make([]byte, 16*1024)
	for {
		n, err := reader.Read(chunk)
		if n > 0 {
			idleTimer.Reset(idle)
			buffer.Write(chunk[:n])
			text := buffer.String()
			for {
				index := strings.Index(text, "\n\n")
				if index < 0 {
					break
				}
				frame := text[:index]
				text = text[index+2:]
				if err := c.handleFrame(frame, opts, result); err != nil {
					return err
				}
			}
			buffer.Reset()
			buffer.WriteString(text)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}
	}
	if tail := strings.TrimSpace(buffer.String()); tail != "" {
		if err := c.handleFrame(tail, opts, result); err != nil {
			return err
		}
	}
	return nil
}

type sseFrame struct {
	Event string
	Data  map[string]any
	Raw   string
}

func parseFrame(block string) sseFrame {
	frame := sseFrame{Event: "message"}
	var data strings.Builder
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "event:"):
			frame.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	frame.Raw = data.String()
	if frame.Raw != "" && frame.Raw != "[DONE]" {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(frame.Raw), &parsed); err == nil {
			frame.Data = parsed
		}
	}
	return frame
}

// handleFrame dispatches one SSE frame.
//
// The documented shape is `{"type":6,"agent_message_chunk":{"msg_content":"…"}}`,
// but the frame types are numeric and undocumented, so dispatch is driven by
// the payload keys instead of the type code. That keeps the adapter working
// when the bundle renumbers its event enum.
func (c *Client) handleFrame(block string, opts Options, result *Result) error {
	frame := parseFrame(block)
	if frame.Data == nil {
		if frame.Raw != "" && !strings.HasPrefix(frame.Raw, "{") {
			// Some frames carry a bare token or a keep-alive comment.
			if message := strings.TrimSpace(frame.Raw); message != "" && message != "[DONE]" && frame.Event != "message" {
				return errors.New(message)
			}
		}
		return nil
	}

	if message := upstreamError(frame.Data); message != "" {
		return errors.New(message)
	}

	for _, key := range []string{"session_id", "sessionId"} {
		if found := deepString(frame.Data, key); found != "" && result.SessionID == "" {
			result.SessionID = found
		}
	}
	for _, key := range []string{"message_id", "messageId", "msg_id"} {
		if found := deepString(frame.Data, key); found != "" {
			result.MessageID = found
		}
	}

	// Thinking first: an agent that exposes its reasoning nests it under a
	// chunk of its own, and those deltas must not be appended to the answer.
	if thinking := firstDeepString(frame.Data, thinkingKeys); thinking != "" {
		result.Thinking += thinking
		if opts.OnThinking != nil {
			opts.OnThinking(thinking)
		}
	}

	if text := firstDeepString(frame.Data, contentKeys); text != "" {
		result.Text += text
		if opts.OnDelta != nil {
			opts.OnDelta(text)
		}
	}

	collectMediaDeep(frame.Data, result)
	if status := deepString(frame.Data, "finish_reason"); status != "" {
		result.StopReason = status
	}
	return nil
}

// contentKeys are the payload fields that carry visible answer text, in
// priority order. msg_content is the one confirmed by the web client.
var contentKeys = []string{"msg_content", "msgContent", "content", "text", "delta", "answer"}

// thinkingKeys are the reasoning equivalents.
var thinkingKeys = []string{"reasoning_content", "thinking_content", "reason_content", "think_content", "reasoning", "thinking"}

// upstreamError extracts an error message from a frame, if it carries one.
func upstreamError(payload map[string]any) string {
	if raw, ok := payload["error"].(map[string]any); ok {
		if message, ok := raw["message"].(string); ok && message != "" {
			return message
		}
	}
	if raw, ok := payload["error"].(string); ok && raw != "" {
		return raw
	}
	for _, key := range []string{"error_msg", "error_message", "err_msg", "status_msg"} {
		if message, ok := payload[key].(string); ok && message != "" {
			// status_msg is "success" on the happy path.
			if strings.EqualFold(message, "success") || message == "ok" {
				continue
			}
			return message
		}
	}
	// Numeric status codes: anything non-zero in these fields is a failure.
	for _, key := range []string{"status_code", "code", "ret_code"} {
		if value, ok := payload[key].(float64); ok && value != 0 {
			return fmt.Sprintf("upstream status %d", int(value))
		}
	}
	return ""
}

// firstDeepString returns the first non-empty string stored under any of the
// given keys, searching nested objects breadth-first.
func firstDeepString(payload any, keys []string) string {
	for _, key := range keys {
		if found := deepString(payload, key); found != "" {
			return found
		}
	}
	return ""
}

// deepString finds a string value for key anywhere in a decoded JSON tree.
func deepString(payload any, key string) string {
	switch node := payload.(type) {
	case map[string]any:
		if value, ok := node[key].(string); ok && value != "" {
			return value
		}
		for _, value := range node {
			if found := deepString(value, key); found != "" {
				return found
			}
		}
	case []any:
		for _, value := range node {
			if found := deepString(value, key); found != "" {
				return found
			}
		}
	}
	return ""
}

// collectMediaDeep walks the payload for finished image or video URLs.
func collectMediaDeep(payload any, result *Result) {
	switch node := payload.(type) {
	case map[string]any:
		for key, value := range node {
			lower := strings.ToLower(key)
			if raw, ok := value.(string); ok {
				if strings.HasPrefix(raw, "http") && looksLikeMediaKey(lower) {
					result.Media = append(result.Media, MediaRef{Kind: mediaKind(lower), URL: raw})
				}
				continue
			}
			collectMediaDeep(value, result)
		}
	case []any:
		for _, value := range node {
			collectMediaDeep(value, result)
		}
	}
}

// looksLikeMediaKey is deliberately narrow: a bare "url" field appears on
// avatars and citation links as often as on generated media, so only explicit
// media-suffixed keys are trusted.
func looksLikeMediaKey(key string) bool {
	if strings.Contains(key, "icon") || strings.Contains(key, "avatar") {
		return false
	}
	return strings.Contains(key, "image_url") || strings.Contains(key, "video_url") ||
		strings.Contains(key, "cover_url") || strings.Contains(key, "file_url")
}

func mediaKind(key string) string {
	if strings.Contains(key, "video") {
		return "video"
	}
	return "image"
}

func dedupeMedia(items []MediaRef) []MediaRef {
	seen := make(map[string]struct{}, len(items))
	out := make([]MediaRef, 0, len(items))
	for _, item := range items {
		key := item.URL
		if index := strings.Index(key, "?"); index >= 0 {
			key = key[:index]
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	return out
}

// ------------------------------------------------------------------- probe

// Probe validates a credential by opening a session and immediately discarding
// it. Creating a session is the cheapest authenticated call available: it
// proves the token and fingerprint pair is accepted without spending a
// conversation turn.
func (c *Client) Probe(ctx context.Context, cred Credential) (int64, error) {
	started := time.Now()
	settings := c.settings()
	timeout := 30 * time.Second
	if settings.Upstream.RequestTimeoutSec > 0 && settings.Upstream.RequestTimeoutSec < 60 {
		timeout = settings.RequestTimeout()
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if _, err := c.CreateSession(ctx, cred); err != nil {
		return 0, err
	}
	return time.Since(started).Milliseconds(), nil
}

// ------------------------------------------------------------------ helpers

func snippet(body []byte) string {
	text := strings.TrimSpace(string(body))
	if len(text) > 300 {
		text = text[:300] + "…"
	}
	return text
}
