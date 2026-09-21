package minimax

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"

	"minimax2api/internal/config"
)

// Default paths for the agent-side opening sequence.
const (
	// DefaultAgentListPath lists the agents belonging to an account.
	DefaultAgentListPath = "/minimax-cloud/api/v1/agent"
	// DefaultConfigPath is the account's agent-side record, created on first
	// read. See Prepare for why reading it is not optional.
	DefaultConfigPath = "/minimax-cloud/api/v1/config"
	// DefaultConnectionsPath completes the sequence the web client runs when it
	// opens the agent page.
	DefaultConnectionsPath = "/minimax-cloud/api/v1/channel/connections"
)

// DefaultAgentRole is the agent the gateway drives.
//
// `mavis` rather than `general`, on two pieces of evidence that agree: the
// captured web client opens its session against a mavis agent, and the skill
// that carries the video tooling (`mcode-tools-master`) is published under
// `Mavis/`. `general` — what earlier builds drove — answers normally and is
// kept as the fallback below, so an account without a mavis agent still works.
//
// The account's other agents (coder, chat, verifier) are left alone; pinning a
// specific id is still possible through the global or per-account AgentID
// setting, which takes precedence over discovery.
const DefaultAgentRole = "mavis"

// fallbackAgentRole is used when the account has no agent of the preferred
// role. It is the role earlier builds drove, so it is known to answer.
const fallbackAgentRole = "general"

// knownAgentRoles are the agent *kinds* the upstream uses.
//
// They are listed rather than inferred because the distinction cannot be made
// from the value alone: `general` is a perfectly ordinary-looking string and an
// earlier build shipped it as the default agent id. The upstream answers it with
// a 200 that opens no session, so accepting it makes every request look
// successful and produce nothing.
var knownAgentRoles = map[string]bool{
	"general":  true,
	"coder":    true,
	"chat":     true,
	"mavis":    true,
	"verifier": true,
}

// isAgentRole reports whether value names an agent kind rather than an agent.
func isAgentRole(value string) bool {
	return knownAgentRoles[strings.ToLower(strings.TrimSpace(value))]
}

// Agent is one entry from an account's agent list.
type Agent struct {
	// ID is the numeric handle that every URL wants.
	//
	// The upstream keeps it in a field called `name`, which is not a display
	// name at all — the entry carries no human-readable label. The kind of
	// agent lives in `agent_role`.
	ID string
	// Role is the agent's kind: general, coder, chat, mavis, verifier.
	Role string
	// RootSessionID is the conversation the web client opens for this agent.
	RootSessionID string
}

// PrepareResult is what the agent-side opening sequence observed.
type PrepareResult struct {
	// Agents is the account's agent list as read during the sequence.
	Agents []Agent
}

// AgentID picks the agent the gateway should drive, or "" when the account has
// none yet.
//
// Empty is a real state (a brand new account before its agents exist) and not
// an error worth failing on.
func (p *PrepareResult) AgentID() string {
	if p == nil {
		return ""
	}
	for _, role := range []string{DefaultAgentRole, fallbackAgentRole} {
		for _, agent := range p.Agents {
			if agent.Role == role && agent.ID != "" {
				return agent.ID
			}
		}
	}
	// Neither role is present. Any agent beats no agent, so take the first one
	// that has an id rather than refusing to work.
	for _, agent := range p.Agents {
		if agent.ID != "" {
			return agent.ID
		}
	}
	return ""
}

// ResolveAgentID returns the id to store for this account and whether it
// differs from the one already stored.
//
// A stored id is a *cache of an earlier discovery*, not an instruction: the role
// the gateway drives can change between builds, and an install that keeps the
// old id would keep driving the old agent with no symptom at all. That is why
// the stored id is revisited rather than only filled in when empty.
//
// The one thing it will not do is overwrite a hand-pinned id. An id that does
// not appear in the account's agent list did not come from a discovery, so it is
// left alone — the same rule the rest of the config follows, where a value that
// could not have been produced by the defaults is treated as intent.
func (p *PrepareResult) ResolveAgentID(stored string) (string, bool) {
	preferred := p.AgentID()
	if preferred == "" || preferred == stored {
		return stored, false
	}
	if stored == "" {
		return preferred, true
	}
	if !p.knowsAgent(stored) {
		return stored, false
	}
	return preferred, true
}

// knowsAgent reports whether the id appears in the account's agent list.
func (p *PrepareResult) knowsAgent(id string) bool {
	if p == nil {
		return false
	}
	for _, agent := range p.Agents {
		if agent.ID == id {
			return true
		}
	}
	return false
}

// Prepare runs the agent-side opening sequence: the three reads the web client
// makes when it opens the agent page, in the order it makes them.
//
// It is not optional, and it is not optional *here*. `/config` is what creates
// the account's record on the agent side, and a check-in claimed before that
// record exists is registered but never paid out: the claim endpoint still
// answers `claim_result=1`, success, while the points are silently dropped. So
// the sequence has to run before a claim, not after — and running it afterwards
// does not recover the day.
//
// The same call is also what makes a freshly registered account able to answer
// a message at all; before it, every send comes back
// `[1400010501] Environment Variables not configured`, a message that mentions
// nothing about initialisation.
//
// Best effort in the sense that the caller decides what a failure means: the
// check-in scheduler treats one as a reason to skip the claim, the console
// treats one as a probe error. Neither should treat it as a dead credential —
// and it does not: a 401 is still reported as ErrInvalidCredential, everything
// else as itself.
func (c *Client) Prepare(ctx context.Context, cred Credential) (*PrepareResult, error) {
	settings := c.settings()
	ctx, cancel := context.WithTimeout(ctx, signinTimeout(settings))
	defer cancel()

	result := &PrepareResult{}
	var firstErr error
	note := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	// A failure in the first call does not stop the rest: the sequence is three
	// independent reads, and collecting all of them gives a better error than
	// stopping at whichever happened to be listed first.
	if _, err := c.callAgent(ctx, settings, cred, http.MethodGet, configPath(settings), nil); err != nil {
		note(err)
	}
	if agents, err := c.fetchAgents(ctx, settings, cred); err != nil {
		note(err)
	} else {
		result.Agents = agents
	}
	if _, err := c.callAgent(ctx, settings, cred, http.MethodGet, connectionsPath(settings), nil); err != nil {
		note(err)
	}

	return result, firstErr
}

// fetchAgents lists an account's agents.
//
// This is what turns an agent *role* into an agent *id*. The gateway used to
// send the literal "general" as the id, which the upstream answers with a 200
// carrying no session at all — so every request looked successful and produced
// nothing, and the probe that was supposed to catch a bad credential reported
// the account healthy.
func (c *Client) fetchAgents(ctx context.Context, settings config.Settings, cred Credential) ([]Agent, error) {
	payload, err := c.callAgent(ctx, settings, cred, http.MethodGet, agentListPath(settings), nil)
	if err != nil {
		return nil, err
	}
	raw, ok := coreOf(payload)["agents"].([]any)
	if !ok {
		return nil, nil
	}
	agents := make([]Agent, 0, len(raw))
	for _, item := range raw {
		node, ok := item.(map[string]any)
		if !ok {
			continue
		}
		agents = append(agents, Agent{
			ID:            stringOf(node["name"]),
			Role:          stringOf(node["agent_role"]),
			RootSessionID: stringOf(node["root_session_id"]),
		})
	}
	return agents, nil
}

// ------------------------------------------------------------------ helpers

func agentListPath(settings config.Settings) string {
	if path := strings.TrimSpace(settings.Upstream.AgentListPath); path != "" {
		return path
	}
	return DefaultAgentListPath
}

func configPath(settings config.Settings) string {
	if path := strings.TrimSpace(settings.Upstream.ConfigPath); path != "" {
		return path
	}
	return DefaultConfigPath
}

func connectionsPath(settings config.Settings) string {
	if path := strings.TrimSpace(settings.Upstream.ConnectionsPath); path != "" {
		return path
	}
	return DefaultConnectionsPath
}

// callAgent issues a signed call on the chat-side request builder.
//
// The agent endpoints sit in the same `/minimax-cloud/api/v1` family as the
// check-in ones but take the chat query string, and they are the same calls the
// console's probe makes — so they go through the same builder as a real
// completion. Keeping one builder means a change here cannot make discovery
// succeed against an upstream that a real request would fail against.
func (c *Client) callAgent(ctx context.Context, settings config.Settings, cred Credential, method, path string, body []byte) (map[string]any, error) {
	if strings.TrimSpace(cred.Token) == "" {
		return nil, ErrInvalidCredential
	}
	req, err := c.newRequest(ctx, settings, cred, method, requestTarget{Path: path}, body)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req, settings)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return decodeJSONResponse(resp, "agent")
}

// AgentProbe is what one diagnostic call observed.
//
// The body comes back as it arrived rather than decoded, because the question
// being asked is usually "what did the upstream actually say" — and the reply
// that matters most is the one that does not decode at all. A page of the SPA's
// HTML is the classic case: it is how a wrong path answers, and it looks like
// success to anything that only checks the status.
type AgentProbe struct {
	// Status is the HTTP status code.
	Status int
	// Body is the response body, truncated to keep a console readable.
	Body string
	// URL is the URL that was signed and requested, with every credential value
	// replaced by a placeholder.
	//
	// Seeing the *shape* is what makes a signature failure diagnosable: `yy`
	// covers this string, so a wrong parameter order and a wrong digest produce
	// the same rejection. The values themselves are the account's credentials
	// and have no business in a log, so they are redacted rather than dropped —
	// the shape is the useful part.
	URL string
}

// ProbeEndpoint makes one signed request and reports the raw result.
//
// Exported for diagnostics, not for the request path. It answers the question a
// completion cannot: *what is this account actually able to do?* The skills
// listing and the config call are readable without spending anything, which
// makes this the cheap way to tell "the gateway composed the wrong request"
// apart from "this account has no working execution channel" — two failures that
// look identical from the outside, right up until one of them costs money to
// disprove.
//
// `stream` routes the call to the conversation host instead of the API host.
// They are different entry points rather than two addresses for one, so a path
// that answers on one can 404 on the other — and that is worth being able to see
// without sending a real turn.
//
// Not to be confused with Probe, which opens and discards a session to validate
// a credential.
//
// The path may carry its own query string.
func (c *Client) ProbeEndpoint(ctx context.Context, cred Credential, method, path string, body []byte, stream bool) (AgentProbe, error) {
	settings := c.settings()
	if strings.TrimSpace(cred.Token) == "" {
		return AgentProbe{}, ErrInvalidCredential
	}
	req, err := c.newRequest(ctx, settings, cred, method, requestTarget{Path: path, Stream: stream}, body)
	if err != nil {
		return AgentProbe{}, err
	}
	probe := AgentProbe{URL: redactURL(req.URL.String())}

	resp, err := c.do(req, settings)
	if err != nil {
		return probe, err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	probe.Status = resp.StatusCode
	probe.Body = strings.TrimSpace(string(raw))
	return probe, nil
}

// redactURL blanks the credential-bearing query values while leaving the rest of
// the URL — including the parameter order — exactly as it was signed.
func redactURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "<unparseable url>"
	}
	query := parsed.Query()
	redacted := false
	for _, key := range []string{"token", "uuid", "device_id", "user_id"} {
		if query.Get(key) != "" {
			query.Set(key, "<redacted>")
			redacted = true
		}
	}
	if !redacted {
		return raw
	}
	// Rebuilt rather than re-encoded: Values.Encode() sorts keys, and the order
	// is part of what this function exists to show.
	original := strings.SplitN(parsed.RawQuery, "&", -1)
	for i, pair := range original {
		key, _, found := strings.Cut(pair, "=")
		if !found {
			continue
		}
		switch key {
		case "token", "uuid", "device_id", "user_id":
			original[i] = key + "=<redacted>"
		}
	}
	parsed.RawQuery = strings.Join(original, "&")
	return parsed.String()
}
