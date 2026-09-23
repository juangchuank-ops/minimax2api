package main

// videorun sends one real video request and reports exactly what came back.
//
// Every cheaper check stops short of the step that decides the answer, which is
// the upstream agent deciding whether it has a tool to render with. So this one
// spends credits, and it prints the balance on both sides of the call.
//
//	go run ./cmd/videorun

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"minimax2api/internal/config"
	"minimax2api/internal/minimax"
)

// The prompt is deliberately plain: the point is not what it depicts but
// whether a file comes back.
const prompt = "A red paper lantern swinging gently in the rain at night, warm glow, close-up, cinematic."

func main() {
	proxy := os.Getenv("MM_PROXY")
	cred := minimax.Credential{
		Region:       minimax.RegionGlobal,
		Token:        os.Getenv("MM_TOKEN"),
		UserID:       os.Getenv("MM_USER_ID"),
		DeviceID:     os.Getenv("MM_DEVICE_ID"),
		UUID:         os.Getenv("MM_UUID"),
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

	host, _ := os.Hostname()
	wd, _ := os.Getwd()
	fmt.Println("== where this ran ==")
	fmt.Printf("  hostname: %s\n  workdir : %s\n  proxy   : %s\n", host, wd, orNone(proxy))
	fmt.Printf("  egress  : %s\n", egressIP(context.Background(), proxy))

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	before := balance(ctx, client, cred)
	fmt.Printf("\n  credits before: %s\n", before)
	if os.Getenv("MM_BALANCE_ONLY") != "" {
		return
	}

	// The agent id has to be resolved first, and the role matters: mavis is the
	// one the captured web client drives.
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
	fmt.Printf("  agent: %s… (role %s)\n", head(agentID, 6), roleOf(prepared, agentID))

	options, err := minimax.VideoOptions{
		Model:      "MiniMax-H3",
		Ratio:      "16:9",
		Resolution: "768P",
		Duration:   5,
	}.Normalize(minimax.VideoOptions{
		Ratio:      settings.Video.DefaultRatio,
		Resolution: settings.Video.DefaultResolution,
		Duration:   settings.Video.DefaultDuration,
	})
	if err != nil {
		fmt.Printf("  options rejected: %v\n", err)
		return
	}

	text := minimax.BuildVideoPrompt(prompt, settings.Video.PluginName, options, settings.Video.OptionsTag)
	fmt.Printf("\n== sending (model %s, %s, %s, %ds) ==\n", options.Model, options.Ratio, options.Resolution, options.Duration)
	fmt.Printf("  client_intent: %s\n", minimax.VideoClientIntent)
	fmt.Printf("  %s\n\n", oneLine(text, 400))

	// Every decoded frame, tallied by the set of keys it carried. Dispatch is
	// payload-driven, so an unrecognised frame vanishes silently — and "the
	// upstream sent something we do not read" and "the upstream sent nothing"
	// look identical from a completion. The tally is what tells them apart, and
	// it is the only part of this run that cannot be recovered afterwards.
	frames := newFrameTally()

	started := time.Now()
	result, err := client.Completion(ctx, minimax.Options{
		Credential: cred,
		Text:       text,
		// The web client sends this for a video turn; the bundle's tool-kind
		// table maps all four video tools to it.
		ClientIntent: minimax.VideoClientIntent,
		// Generous on purpose: when the agent has no tool it does not fail
		// fast, it explores. A short timeout would cut that short and turn
		// "the upstream explained what is missing" into "the gateway gave up".
		Timeout: 8 * time.Minute,
		OnFrame: frames.add,
	})
	elapsed := time.Since(started)

	fmt.Printf("== came back after %s ==\n", elapsed.Round(time.Second))
	if err != nil {
		fmt.Printf("  error: %v\n", err)
	}
	if result != nil {
		fmt.Printf("  session: %s\n", result.SessionID)
		fmt.Printf("  media  : %d\n", len(result.Media))
		for _, m := range result.Media {
			fmt.Printf("    %s\n", safeURL(m.URL))
		}
		if result.Thinking != "" {
			fmt.Printf("\n  -- thinking (first 600) --\n  %s\n", oneLine(result.Thinking, 600))
		}
		if result.Text != "" {
			fmt.Printf("\n  -- text --\n  %s\n", indent(oneLine(result.Text, 2000)))
		}
	}

	fmt.Print(frames.report())

	// The second half of the answer, and the whole point of this build: a turn
	// that produced a file leaves it in the drive, not in the stream.
	if result != nil && result.SessionID != "" {
		fmt.Println("\n== drive lookup (this is where a finished file would be) ==")
		artifacts, err := client.SessionArtifacts(ctx, cred, result.SessionID)
		if err != nil {
			fmt.Printf("  artifacts unavailable: %v\n", err)
		}
		fresh := 0
		for _, artifact := range artifacts {
			marker := "old"
			if artifact.CreatedAt >= started.UnixMilli() {
				marker = "NEW"
				fresh++
			}
			fmt.Printf("  [%s] %s  %s  %s  %d bytes  created_at=%d\n",
				marker, artifact.Kind(), artifact.Category, artifact.Name, artifact.SizeBytes, artifact.CreatedAt)
		}
		if len(artifacts) == 0 {
			fmt.Println("  (the session reports no artifacts at all)")
		}
		fmt.Printf("  -> %d produced by this turn\n", fresh)
		if fresh > 0 {
			media, err := client.SessionMedia(ctx, cred, result.SessionID, started.UnixMilli())
			if err != nil {
				fmt.Printf("  resolving links failed: %v\n", err)
			}
			for _, item := range media {
				fmt.Printf("  playable: %s %s\n", item.Kind, safeURL(item.URL))
			}
		}
	}

	fmt.Printf("\n  credits after: %s\n", balance(ctx, client, cred))
}

// frameTally counts the shapes the upstream sent, not their contents.
type frameTally struct {
	shapes map[string]int
	total  int
}

func newFrameTally() *frameTally {
	return &frameTally{shapes: map[string]int{}}
}

func (t *frameTally) add(frame map[string]any) {
	t.total++
	keys := make([]string, 0, len(frame))
	for key := range frame {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	t.shapes[strings.Join(keys, ",")]++
}

func (t *frameTally) report() string {
	if t.total == 0 {
		return "\n== frames ==\n  (nothing decoded)\n"
	}
	var out strings.Builder
	fmt.Fprintf(&out, "\n== frames (%d) ==\n", t.total)
	shapes := make([]string, 0, len(t.shapes))
	for shape := range t.shapes {
		shapes = append(shapes, shape)
	}
	sort.Strings(shapes)
	for _, shape := range shapes {
		fmt.Fprintf(&out, "  %4d × {%s}\n", t.shapes[shape], shape)
	}
	return out.String()
}

func balance(ctx context.Context, client *minimax.Client, cred minimax.Credential) string {
	probe, err := client.ProbeEndpoint(ctx, cred, http.MethodPost,
		"/matrix/api/v1/commerce/get_membership_info", []byte("{}"), false)
	if err != nil {
		return "unavailable: " + err.Error()
	}
	var parsed struct {
		Summary struct {
			Total string `json:"total_remaining_amount"`
		} `json:"op_credit_summary"`
	}
	if err := json.Unmarshal([]byte(probe.Body), &parsed); err != nil {
		return fmt.Sprintf("unparsed (HTTP %d)", probe.Status)
	}
	if parsed.Summary.Total == "" {
		return fmt.Sprintf("absent (HTTP %d)", probe.Status)
	}
	return parsed.Summary.Total
}

// safeURL keeps the host and path of a media link and drops the query. The query
// carries a time-limited signature for the user's own asset: enough to fetch it,
// and no reason to paste it anywhere.
func safeURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "<unparseable>"
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func roleOf(prepared *minimax.PrepareResult, id string) string {
	for _, agent := range prepared.Agents {
		if agent.ID == id {
			return agent.Role
		}
	}
	return "unknown"
}

func head(value string, n int) string {
	if len(value) <= n {
		return value
	}
	return value[:n]
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

func indent(text string) string {
	return strings.ReplaceAll(text, "\n", "\n  ")
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
	buf := make([]byte, 64)
	n, _ := resp.Body.Read(buf)
	return strings.TrimSpace(string(buf[:n]))
}
