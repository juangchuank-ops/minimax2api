package main

// entryprobe answers one question without spending anything: which entry point
// a message has to be posted to.
//
// It sends requests that are *bound to fail* — a session id that does not exist
// — so the upstream never runs an agent turn. What matters is not that they fail
// but *how*: a wrong path is answered by the SPA catch-all with a full page of
// HTML (sometimes with a 200), while a correct path is answered with a JSON
// error. That difference is the whole answer.
//
// Credentials come from the environment and are never printed, written or
// logged. The URL that the probe reports is redacted by the client itself.
//
//	go run ./cmd/entryprobe

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"minimax2api/internal/config"
	"minimax2api/internal/minimax"
)

func main() {
	proxy := os.Getenv("MM_PROXY")
	cred := minimax.Credential{
		Region:   minimax.RegionGlobal,
		Token:    os.Getenv("MM_TOKEN"),
		UserID:   os.Getenv("MM_USER_ID"),
		DeviceID: os.Getenv("MM_DEVICE_ID"),
		UUID:     os.Getenv("MM_UUID"),
		// The captured browser was 1536x864; the screen size is part of `yy`.
		ScreenWidth:  1536,
		ScreenHeight: 864,
	}
	if cred.Token == "" || cred.UserID == "" || cred.DeviceID == "" || cred.UUID == "" {
		fmt.Println("credentials missing from the environment")
		os.Exit(2)
	}

	settings := config.DefaultSettings(os.TempDir())
	settings.Upstream.Proxy = proxy
	settings.Upstream.ScreenWidth = 1536
	settings.Upstream.ScreenHeight = 864

	client := minimax.New(func() config.Settings { return settings })
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	host, _ := os.Hostname()
	exe, _ := os.Executable()
	wd, _ := os.Getwd()
	fmt.Println("== where this ran ==")
	fmt.Printf("  hostname  : %s\n  executable: %s\n  workdir   : %s\n", host, exe, wd)
	fmt.Printf("  proxy     : %s\n", orNone(proxy))
	fmt.Printf("  egress IP : %s\n", egressIP(ctx, proxy))

	fmt.Println("\n== free GETs (does the 22-parameter query still pass on the API host?) ==")
	for _, path := range []string{
		"/minimax-cloud/api/v1/config",
		"/minimax-cloud/api/v1/agent",
		"/minimax-cloud/api/v1/skill?agent_name=&include_plugin_skills=true",
	} {
		probe, err := client.ProbeEndpoint(ctx, cred, http.MethodGet, path, nil, false)
		report(path, probe, err)
	}

	fmt.Println("\n== entry-point comparison (POST to a session that does not exist) ==")
	fmt.Println("   a wrong path is answered with HTML, a correct one with JSON")
	cases := []struct {
		label  string
		path   string
		stream bool
	}{
		{"API host    + /minimax-cloud/ (new)", "/minimax-cloud/api/v1/session/1/message", false},
		{"stream host + /minimax-cloud/ (new)", "/minimax-cloud/api/v1/session/1/message", true},
		{"API host    + /archon/        (old)", "/archon/api/v1/session/1/message", false},
		{"stream host + /archon/        (old)", "/archon/api/v1/session/1/message", true},
	}
	for _, c := range cases {
		probe, err := client.ProbeEndpoint(ctx, cred, http.MethodPost, c.path, []byte("{}"), c.stream)
		fmt.Printf("\n  %s\n", c.label)
		report("", probe, err)
	}

	// The comparison above rejects before routing: a session id that does not
	// exist never reaches the handler that would care which entry point it came
	// in through. So the same four combinations are run again against a session
	// that *does* exist — still with no content, so the turn is refused at
	// validation and nothing runs.
	fmt.Println("\n== same four, against a real session (still no content, so nothing runs) ==")
	prepared, err := client.Prepare(ctx, cred)
	if err != nil {
		fmt.Printf("  prepare failed: %v\n", err)
		return
	}
	agentID := prepared.AgentID()
	if agentID == "" {
		fmt.Println("  the account reports no agents")
		return
	}
	cred.AgentID = agentID
	fmt.Printf("  agent id: %s… (role %s)\n", head(agentID, 6), roleOf(prepared, agentID))

	sessionID, err := client.CreateSession(ctx, cred)
	if err != nil {
		fmt.Printf("  create session failed: %v\n", err)
		return
	}
	fmt.Printf("  session id: %s…\n", head(sessionID, 6))

	live := []struct {
		label  string
		path   string
		stream bool
	}{
		{"API host    + /minimax-cloud/ (new)", fillSession("/minimax-cloud/api/v1/session/{session_id}/message", sessionID), false},
		{"stream host + /minimax-cloud/ (new)", fillSession("/minimax-cloud/api/v1/session/{session_id}/message", sessionID), true},
		{"API host    + /archon/        (old)", fillSession("/archon/api/v1/session/{session_id}/message", sessionID), false},
		{"stream host + /archon/        (old)", fillSession("/archon/api/v1/session/{session_id}/message", sessionID), true},
	}
	for _, c := range live {
		probe, err := client.ProbeEndpoint(ctx, cred, http.MethodPost, c.path, []byte("{}"), c.stream)
		fmt.Printf("\n  %s\n", c.label)
		report("", probe, err)
	}
}

func fillSession(path, sessionID string) string {
	return strings.ReplaceAll(path, "{session_id}", sessionID)
}

// head shortens an identifier for display. Session and agent ids are not
// credentials, but they are still account-scoped handles and there is no reason
// to put a whole one on screen.
func head(value string, n int) string {
	if len(value) <= n {
		return value
	}
	return value[:n]
}

func roleOf(prepared *minimax.PrepareResult, id string) string {
	for _, agent := range prepared.Agents {
		if agent.ID == id {
			return agent.Role
		}
	}
	return "unknown"
}

func report(label string, probe minimax.AgentProbe, err error) {
	if label != "" {
		fmt.Printf("\n  %s\n", label)
	}
	if err != nil {
		fmt.Printf("    transport error: %v\n", err)
		return
	}
	fmt.Printf("    status : %d    html: %v\n", probe.Status, looksHTML(probe.Body))
	fmt.Printf("    url    : %s\n", probe.URL)
	fmt.Printf("    body   : %s\n", oneLine(probe.Body, 240))
}

// looksHTML is the tell that separates "this path does not exist" from "this
// request was understood and rejected". The SPA catch-all answers with a page,
// and it does so with a 200 often enough that the status alone proves nothing.
func looksHTML(body string) bool {
	lower := strings.ToLower(strings.TrimSpace(body))
	return strings.HasPrefix(lower, "<!doctype") || strings.HasPrefix(lower, "<html") ||
		strings.Contains(lower, "<head>") && strings.Contains(lower, "</html>")
}

func oneLine(text string, limit int) string {
	collapsed := strings.Join(strings.Fields(text), " ")
	if len(collapsed) > limit {
		return collapsed[:limit] + "…"
	}
	if collapsed == "" {
		return "(empty)"
	}
	return collapsed
}

func orNone(value string) string {
	if value == "" {
		return "(none)"
	}
	return value
}

func egressIP(ctx context.Context, proxy string) string {
	client := &http.Client{Timeout: 20 * time.Second}
	if proxy != "" {
		if parsed, err := url.Parse(proxy); err == nil {
			client.Transport = &http.Transport{Proxy: http.ProxyURL(parsed)}
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.ipify.org", nil)
	if err != nil {
		return "unavailable"
	}
	resp, err := client.Do(req)
	if err != nil {
		return "unavailable: " + err.Error()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	return strings.TrimSpace(string(body))
}
