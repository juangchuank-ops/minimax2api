package minimax

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"minimax2api/internal/config"
)

// --- agent id selection ----------------------------------------------------

func TestPrepareResultPicksTheGeneralAgent(t *testing.T) {
	prepared := &PrepareResult{Agents: []Agent{
		{ID: "443154487857415", Role: "chat"},
		{ID: "443154487857417", Role: "general"},
		{ID: "443154487857418", Role: "coder"},
	}}
	if got := prepared.AgentID(); got != "443154487857417" {
		t.Errorf("AgentID = %q, want the general agent's numeric id", got)
	}
}

// TestPrepareResultFallsBackToAnyAgent: a usable agent beats a missing one, and
// an account whose roles have all been renamed is still worth driving.
func TestPrepareResultFallsBackToAnyAgent(t *testing.T) {
	prepared := &PrepareResult{Agents: []Agent{
		{ID: "", Role: "general"},
		{ID: "443154487857415", Role: "renamed"},
	}}
	if got := prepared.AgentID(); got != "443154487857415" {
		t.Errorf("AgentID = %q, want the first agent that has an id", got)
	}
}

func TestPrepareResultWithoutAgentsHasNoID(t *testing.T) {
	var prepared *PrepareResult
	if got := prepared.AgentID(); got != "" {
		t.Errorf("nil result = %q, want empty", got)
	}
	if got := (&PrepareResult{}).AgentID(); got != "" {
		t.Errorf("empty result = %q, want empty", got)
	}
}

// --- session creation guards ----------------------------------------------

// TestCreateSessionRefusesAnUnknownAgentID pins the guard that turns a silent
// nothing into an honest error.
//
// An empty agent id makes the URL `/…/agent//session`, which the SPA's
// catch-all answers with 200 and a page of HTML. Nothing in that response says
// "wrong path", so the call used to look successful while opening no session at
// all — and the probe that was supposed to catch a bad account reported it
// healthy.
func TestCreateSessionRefusesAnUnknownAgentID(t *testing.T) {
	settings := config.DefaultSettings(t.TempDir())
	settings.Upstream.AgentID = ""
	client := New(func() config.Settings { return settings })

	_, err := client.CreateSession(context.Background(), Credential{Token: "token"})
	if !errors.Is(err, ErrAgentIDUnknown) {
		t.Fatalf("err = %v, want ErrAgentIDUnknown", err)
	}
	// The whole point of the guard is not to spend the request.
	if !errors.Is(err, ErrAgentIDUnknown) || errors.Is(err, ErrInvalidCredential) {
		t.Errorf("a missing agent id must not look like a dead token: %v", err)
	}
}

// TestCreateSessionRejectsAnHTMLAnswer pins the other half: a 200 that is not
// JSON must not be mistaken for a session id. Returning the page as the id built
// a request URL containing an entire web page, which the edge then rejected with
// a 400 that read like an upstream block.
func TestCreateSessionRejectsAnHTMLAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body>MiniMax</body></html>`))
	}))
	defer server.Close()

	settings := config.DefaultSettings(t.TempDir())
	settings.Upstream.BaseURL = server.URL
	settings.Upstream.AgentID = "443154487857417"
	client := New(func() config.Settings { return settings })

	id, err := client.CreateSession(context.Background(), Credential{Token: "token"})
	if err == nil {
		t.Fatalf("an HTML page was accepted as a session id: %q", id)
	}
	if id != "" {
		t.Errorf("id = %q, want empty", id)
	}
}

// TestCreateSessionReadsTheNumericIDFromTheTopLevel documents the shape the
// upstream actually returns: the session id sits at the top level, and the agent
// it belongs to comes back under `agent_name`.
func TestCreateSessionReadsTheNumericIDFromTheTopLevel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"agent_name":"443154487857417","session_id":"443323308642590",` +
			`"base_resp":{"status_code":0,"status_msg":"ok"}}`))
	}))
	defer server.Close()

	settings := config.DefaultSettings(t.TempDir())
	settings.Upstream.BaseURL = server.URL
	settings.Upstream.AgentID = "443154487857417"
	client := New(func() config.Settings { return settings })

	id, err := client.CreateSession(context.Background(), Credential{Token: "token"})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if id != "443323308642590" {
		t.Errorf("id = %q, want the top-level session_id", id)
	}
}

// --- credit grants ---------------------------------------------------------

// TestCreditGrantsReadsTheStringAmounts pins the shape used for reconciliation:
// money arrives quoted, and the grant's timestamp is what decides whether a
// check-in paid out.
func TestCreditGrantsReadsTheStringAmounts(t *testing.T) {
	var gotMethod, gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"details":[{"credit_type":2,"granted_amount":"400.00",` +
			`"remaining_amount":"395.82","granted_at_ms":1789784822364,` +
			`"expire_at_ms":1792339200000}],"total_count":1,` +
			`"base_resp":{"status_code":0,"status_msg":"ok"}}`))
	}))
	defer server.Close()

	settings := config.DefaultSettings(t.TempDir())
	settings.Upstream.BaseURL = server.URL
	client := New(func() config.Settings { return settings })

	grants, err := client.CreditGrants(context.Background(), Credential{Token: "token", UserID: "1"})
	if err != nil {
		t.Fatalf("CreditGrants: %v", err)
	}
	// GET, not POST: the POST form of this path answers 404, which is easy to
	// read as "the endpoint is gone".
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotPath != DefaultCreditDetailsPath {
		t.Errorf("path = %q, want %q", gotPath, DefaultCreditDetailsPath)
	}
	if len(grants) != 1 {
		t.Fatalf("grants = %d, want 1", len(grants))
	}
	grant := grants[0]
	if grant.Granted != 400 || grant.Remaining != 395.82 {
		t.Errorf("amounts = %v/%v, want 400/395.82", grant.Granted, grant.Remaining)
	}
	if grant.GrantedAt.UnixMilli() != 1789784822364 {
		t.Errorf("grantedAt = %v, want the epoch millis from the response", grant.GrantedAt)
	}
}

// TestCreditGrantsTreatsAMissingListAsEmpty: an account with no credits answers
// `{"total_count":0}` and omits the array entirely, which is not a broken
// response.
func TestCreditGrantsTreatsAMissingListAsEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"total_count":0,"base_resp":{"status_code":0,"status_msg":"ok"}}`))
	}))
	defer server.Close()

	settings := config.DefaultSettings(t.TempDir())
	settings.Upstream.BaseURL = server.URL
	client := New(func() config.Settings { return settings })

	grants, err := client.CreditGrants(context.Background(), Credential{Token: "token", UserID: "1"})
	if err != nil {
		t.Fatalf("CreditGrants: %v", err)
	}
	if len(grants) != 0 {
		t.Errorf("grants = %v, want none", grants)
	}
}

// --- default drift ---------------------------------------------------------

// TestConfigDefaultsAgreeWithThePackageConstants pins a duplication that is easy
// to forget and impossible to see.
//
// The runtime settings carry their own copy of every path, because config cannot
// import this package (the dependency runs the other way) and because an
// operator may override any of them. But a settings value beats the constant, so
// a stale copy in config silently wins — and a wrong path here fails in ways
// that never look like a wrong path.
//
// A live run against the real upstream is what caught the session path and the
// agent id both being left behind in config: the first returned the SPA's HTML
// with a 200, the second returned a 200 that opened no session. Neither is
// visible from a status code, and every unit test in this package passed.
func TestConfigDefaultsAgreeWithThePackageConstants(t *testing.T) {
	settings := config.DefaultSettings(t.TempDir())
	upstream := settings.Upstream

	cases := []struct{ name, got, want string }{
		{"session path", upstream.SessionPath, DefaultSessionPath},
		{"message path", upstream.MessagePath, DefaultMessagePath},
		{"agent id", upstream.AgentID, DefaultAgentID},
		{"user info path", upstream.UserInfoPath, DefaultUserInfoPath},
		{"agent list path", upstream.AgentListPath, DefaultAgentListPath},
		{"config path", upstream.ConfigPath, DefaultConfigPath},
		{"connections path", upstream.ConnectionsPath, DefaultConnectionsPath},
		{"credit details path", settings.Signin.CreditDetailsPath, DefaultCreditDetailsPath},
		{"credit path", settings.Signin.CreditPath, DefaultCreditPath},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("the config default for %s is %q but the package constant is %q; "+
				"the settings value wins, so the constant never takes effect",
				tc.name, tc.got, tc.want)
		}
	}
}

// TestTheDefaultAgentIDIsNotARoleName: `general` looks exactly like an agent id
// and is not one. The upstream accepts it with a 200 carrying no session, so
// every request appears to succeed and produces nothing.
func TestTheDefaultAgentIDIsNotARoleName(t *testing.T) {
	if DefaultAgentID == DefaultAgentRole {
		t.Errorf("the default agent id is the role name %q, which opens no session", DefaultAgentID)
	}
	if got := config.DefaultSettings(t.TempDir()).Upstream.AgentID; got == DefaultAgentRole {
		t.Errorf("the config default agent id is the role name %q", got)
	}
}

// TestAgentIDIgnoresARoleName pins the read-time half of a repair that also
// happens in Normalize.
//
// A settings file written by an earlier build holds `general` as the agent id,
// because that was that build's default. The upstream answers a role name with a
// 200 that opens no session, so using one makes every request look successful
// and produce nothing. Ignoring it here means the stale value cannot take effect
// even on an instance that has not been restarted through Normalize yet.
func TestAgentIDIgnoresARoleName(t *testing.T) {
	settings := config.DefaultSettings(t.TempDir())
	settings.Upstream.AgentID = DefaultAgentRole

	client := &Client{settings: func() config.Settings { return settings }}

	// The account's own id is discovered, so it must win over the stale global.
	if got := client.agentID(settings, Credential{AgentID: "443154487857417"}); got != "443154487857417" {
		t.Errorf("agentID = %q, want the account's own id", got)
	}
	// With nothing discovered, the role name must not be handed back as an id —
	// returning it is exactly the failure this guards.
	if got := client.agentID(settings, Credential{}); got != "" {
		t.Errorf("agentID = %q, want empty rather than the role name %q", got, DefaultAgentRole)
	}
	// A per-account value is not exempt: it came from the same settings file.
	if got := client.agentID(settings, Credential{AgentID: DefaultAgentRole}); got != "" {
		t.Errorf("agentID = %q, want empty rather than the role name", got)
	}
}

// TestAgentIDKeepsARealID: the guard must reject role names without rejecting
// ids that merely contain one.
func TestAgentIDKeepsARealID(t *testing.T) {
	for _, id := range []string{"443154487857417", "general-purpose-1", "0"} {
		settings := config.DefaultSettings(t.TempDir())
		settings.Upstream.AgentID = id
		client := &Client{settings: func() config.Settings { return settings }}
		if got := client.agentID(settings, Credential{}); got != id {
			t.Errorf("agentID = %q, want the configured id %q kept", got, id)
		}
	}
}
