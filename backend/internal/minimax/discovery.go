package minimax

import (
	"context"
	"net/http"
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
// The account's other agents (coder, chat, mavis, verifier) are left alone;
// pinning a specific id is still possible through the global or per-account
// AgentID setting, which takes precedence over discovery.
const DefaultAgentRole = "general"

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
	for _, agent := range p.Agents {
		if agent.Role == DefaultAgentRole && agent.ID != "" {
			return agent.ID
		}
	}
	// The general agent is missing. Any agent beats no agent, so take the first
	// one that has an id rather than refusing to work.
	for _, agent := range p.Agents {
		if agent.ID != "" {
			return agent.ID
		}
	}
	return ""
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
	rawURL := c.buildURL(settings, cred, path, "")
	req, err := c.newRequest(ctx, settings, cred, method, rawURL, body)
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
