package config

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Config holds process-level bootstrap options. Runtime-tunable gateway
// parameters live in the persistent store instead.
type Config struct {
	Addr          string
	DataDir       string
	StaticDir     string
	AdminUser     string
	AdminPassword string
}

func Load() Config {
	var cfg Config
	flag.StringVar(&cfg.Addr, "addr", env("MINIMAX2API_ADDR", "127.0.0.1:8080"), "HTTP listen address")
	flag.StringVar(&cfg.DataDir, "data", env("MINIMAX2API_DATA", "data"), "persistent data directory")
	flag.StringVar(&cfg.StaticDir, "static", env("MINIMAX2API_STATIC", "frontend/dist"), "built frontend directory")
	flag.StringVar(&cfg.AdminUser, "admin-user", env("MINIMAX2API_ADMIN_USER", "admin"), "initial admin username")
	flag.StringVar(&cfg.AdminPassword, "admin-password", env("MINIMAX2API_ADMIN_PASSWORD", ""), "initial admin password (random when empty)")
	flag.Parse()

	if cfg.AdminPassword == "" {
		cfg.AdminPassword = env("MINIMAX2API_ADMIN_PASSWORD", "")
	}
	return cfg
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// Settings mirrors the runtime-tunable section exposed by the admin console.
type Settings struct {
	Server   ServerSettings   `json:"server"`
	Upstream UpstreamSettings `json:"upstream"`
	Routing  RoutingSettings  `json:"routing"`
	Audit    AuditSettings    `json:"audit"`
	Media    MediaSettings    `json:"media"`
	Signin   SigninSettings   `json:"signin"`
	Video    VideoSettings    `json:"video"`
}

// VideoSettings drives the video-generation route.
//
// Video is not a chat mode. MiniMax-H3 has no model id in the conversation
// API: the agent reaches it through the `video-creater` plugin, and the plugin
// is selected by *mentioning* it in the message text — the composer's reference
// chip serialises to `@video-creater` and that string is the whole mechanism.
// The generation parameters then travel in a trailing
// `<video-generation-options>` block appended to the same text, not in a
// request field.
//
// Both of those belong to the upstream bundle rather than to this gateway, so
// both are settings: a renamed plugin or a renamed tag should be a console edit,
// not a rebuild.
type VideoSettings struct {
	// PluginName is the plugin reference the agent routes on. It is written
	// into the message text as `@<pluginName>`.
	PluginName string `json:"pluginName"`
	// OptionsTag is the tag the generation parameters travel in.
	OptionsTag string `json:"optionsTag"`
	// Default* fill in whatever the caller left out.
	//
	// They matter more than an ordinary default would. The plugin's own skill
	// asks the user to confirm every unspecified choice before generating, and
	// a headless API call has nobody to answer — so an omitted parameter does
	// not quietly fall back to an upstream default, it stalls the turn. Always
	// sending a complete set is what keeps a request unattended.
	DefaultRatio      string `json:"defaultRatio"`
	DefaultResolution string `json:"defaultResolution"`
	DefaultDuration   int    `json:"defaultDuration"`
	// TimeoutSec bounds one video turn, separately from the chat timeout: the
	// fast variant alone spends around twenty seconds generating, and the slow
	// one is documented at 15–30 minutes.
	TimeoutSec int `json:"timeoutSec"`
}

// SigninSettings drives the daily check-in sweep and the credit guard.
//
// Only the international deployment is implemented. MiniMax runs the check-in
// under a different parameter set on the mainland host (it keys off
// timezone_id rather than timezone_offset), and signing a mainland account with
// the international protocol just earns a rejection, so those accounts are
// skipped rather than guessed at.
type SigninSettings struct {
	Enabled bool `json:"enabled"`
	// Hour and Minute are local wall-clock time for the daily sweep. The
	// upstream resets the day at some unverified boundary, so a morning run
	// keeps a healthy margin on either interpretation.
	Hour   int `json:"hour"`
	Minute int `json:"minute"`
	// GapSeconds spaces out the per-account requests. Check-in is a
	// risk-control sensitive endpoint; a burst of parallel calls from one IP is
	// exactly the pattern that gets accounts flagged.
	GapSeconds int `json:"gapSeconds"`
	// TimeoutSec bounds a single check-in call.
	TimeoutSec int `json:"timeoutSec"`
	// SkipZeroCredit removes accounts whose last known balance is zero from
	// scheduling. See CreditFreshMin for how stale a reading may be.
	SkipZeroCredit bool `json:"skipZeroCredit"`
	// CreditFreshMin is how long a balance reading stays actionable. Past it
	// the account is scheduled again: refusing to use an account because of a
	// day-old zero would strand capacity that a single request would refill.
	CreditFreshMin int `json:"creditFreshMin"`
	// CreditRefreshMin is how often balances are polled in the background. 0
	// disables the poller, leaving manual refresh only.
	CreditRefreshMin int `json:"creditRefreshMin"`

	// Browser fingerprint reported to the check-in endpoints. The captured
	// values are reproduced by default; they only have to stay self-consistent,
	// since the signature covers whatever is actually sent.
	Lang              string `json:"lang"`
	OSName            string `json:"osName"`
	BrowserName       string `json:"browserName"`
	BrowserLanguage   string `json:"browserLanguage"`
	BrowserPlatform   string `json:"browserPlatform"`
	DeviceMemory      int    `json:"deviceMemory"`
	CPUCoreNum        int    `json:"cpuCoreNum"`
	TimezoneOffsetMin int    `json:"timezoneOffsetMin"`

	// Paths are settings rather than constants for the same reason the agent
	// paths are: a rename upstream should not need a rebuild.
	StatusPath string `json:"statusPath"`
	ClaimPath  string `json:"claimPath"`
	CreditPath string `json:"creditPath"`
	// CreditDetailsPath lists each credit grant separately. It is what makes a
	// check-in auditable: the claim endpoint reports success whether or not the
	// points were issued, so the grant is the only evidence they arrived.
	CreditDetailsPath string `json:"creditDetailsPath"`
}

type ServerSettings struct {
	Addr                  string `json:"addr"`
	MaxConcurrentRequests int    `json:"maxConcurrentRequests"`
	AdminUsername         string `json:"adminUsername"`
}

// UpstreamSettings describes how to reach MiniMax Agent.
//
// Two hosts are kept side by side because MiniMax runs separate mainland and
// international deployments with separate account databases: an account
// registered with a +86 phone number authenticates only against the mainland
// host, and one registered with an email or an overseas number only against
// the international host. Routing each account to the right host is what makes
// both kinds of accounts work in a single pool.
type UpstreamSettings struct {
	BaseURL     string `json:"baseURL"`
	BaseURLCN   string `json:"baseURLCN"`
	AgentID     string `json:"agentID"`
	SessionPath string `json:"sessionPath"`
	MessagePath string `json:"messagePath"`
	// UserInfoPath is where an account's realUserID can be read. Every signed
	// endpoint demands that value in its query and answers a bare 401 without
	// it, and it appears nowhere in the token — so this call is the only way an
	// account added as a bare JWT can ever become usable.
	UserInfoPath string `json:"userInfoPath"`
	// AgentListPath is where an account's agents are listed. It is what turns
	// the `general` *role* into the numeric agent *id* every URL needs; without
	// it a session cannot be opened at all.
	AgentListPath string `json:"agentListPath"`
	// ConfigPath is the agent-side initialisation call. A freshly registered
	// account refuses to answer messages until it has run once, and says so
	// with a message about environment variables that mentions none of this.
	//
	// It also has to run before a check-in, not after: a claim made before the
	// account's agent-side record exists is registered and never paid out, and
	// running the sequence afterwards does not recover it.
	ConfigPath string `json:"configPath"`
	// ConnectionsPath completes the opening sequence the web client runs when
	// it opens the agent page.
	ConnectionsPath      string `json:"connectionsPath"`
	ModelPayload         string `json:"modelPayload"`
	Language             string `json:"language"`
	ScreenWidth          int    `json:"screenWidth"`
	ScreenHeight         int    `json:"screenHeight"`
	RequestTimeoutSec    int    `json:"requestTimeoutSec"`
	StreamIdleTimeoutSec int    `json:"streamIdleTimeoutSec"`
	Proxy                string `json:"proxy"`
	// StreamBaseURL overrides the host the conversation endpoint answers on.
	//
	// It is separate from BaseURL because the captured web client posts
	// messages to `agent-stream.<domain>` while every other call stays on the
	// API host — and the two hosts are not interchangeable: the same path on
	// the API host reaches a different entry point, which is exactly what the
	// gateway did before this field existed.
	//
	// Empty means "derive it from BaseURL", which is the better default: it
	// follows an install or an account that has been pointed somewhere else
	// (a self-hosted relay, a test server) instead of overriding it with a
	// hostname those deployments do not have.
	StreamBaseURL string `json:"streamBaseURL"`
	UserAgent     string `json:"userAgent"`
}

type RoutingSettings struct {
	Strategy        string `json:"strategy"`
	CooldownBaseSec int    `json:"cooldownBaseSec"`
	CooldownMaxSec  int    `json:"cooldownMaxSec"`
	MaxAttempts     int    `json:"maxAttempts"`
	CapacityWaitSec int    `json:"capacityWaitSec"`
	StickyTTLSec    int    `json:"stickyTTLSec"`
	PreferIdle      bool   `json:"preferIdle"`
}

type AuditSettings struct {
	RetentionDays  int  `json:"retentionDays"`
	MaxRecords     int  `json:"maxRecords"`
	RecordBody     bool `json:"recordBody"`
	BodyLimitBytes int  `json:"bodyLimitBytes"`
}

type MediaSettings struct {
	GeneratedDir   string `json:"generatedDir"`
	PublicBaseURL  string `json:"publicBaseURL"`
	MaxTotalSizeMB int    `json:"maxTotalSizeMB"`
	AutoDownload   bool   `json:"autoDownload"`
}

// Settings an earlier build shipped as its own defaults and that turned out to
// be wrong. They are listed so Normalize can recognise and repair them.
//
// Repairing is necessary because Normalize otherwise only fills in *empty*
// settings, and a fresh install freezes a copy of every default into the
// settings file. A default that is later found to be wrong therefore stays wrong
// on every existing install, with no symptom — which is how both of these
// survived a fix to the defaults themselves.
//
// Neither value can work, so overwriting one is never a loss of intent:
//
//   - the session path resolves to the site's SPA fallback, which answers 200
//     with a full page of HTML rather than a session;
//   - `general` is an agent *role*, which the upstream answers with a 200 that
//     opens no session at all.
const (
	legacyDefaultSessionPath = "/agent/{agent_id}/session"
	legacyDefaultAgentID     = "general"
	// legacyDefaultMessagePath is a different kind of entry: it was not broken,
	// it was merely the wrong *door*. It answers, and the agent replies, but the
	// entry point it names hands the agent no rendering tool — so every video
	// request came back as a friendly description of a video that was never
	// made. The captured web client uses the `/minimax-cloud` path below, on the
	// streaming host; the two go together, which is why both are migrated.
	legacyDefaultMessagePath = "/archon/api/v1/session/{session_id}/message"
)

// DefaultSettings returns the built-in runtime configuration.
func DefaultSettings(dataDir string) Settings {
	return Settings{
		Server: ServerSettings{
			Addr:                  "127.0.0.1:8080",
			MaxConcurrentRequests: 64,
			AdminUsername:         "admin",
		},
		Upstream: UpstreamSettings{
			BaseURL:   "https://agent.minimax.io",
			BaseURLCN: "https://agent.minimaxi.com",
			// Empty on purpose: `general` is an agent *role*, not an id, and the
			// upstream accepts it with a 200 that opens no session. The real id
			// is a per-account number, discovered when the account is prepared.
			AgentID: "",
			// Must agree with minimax.DefaultSessionPath — this package cannot
			// import it (the dependency runs the other way), so the agreement is
			// pinned by a test in the minimax package instead.
			SessionPath:          "/minimax-cloud/api/v1/agent/{agent_id}/session",
			MessagePath:          "/minimax-cloud/api/v1/session/{session_id}/message",
			UserInfoPath:         "/v1/api/user/info",
			AgentListPath:        "/minimax-cloud/api/v1/agent",
			ConfigPath:           "/minimax-cloud/api/v1/config",
			ConnectionsPath:      "/minimax-cloud/api/v1/channel/connections",
			ModelPayload:         "",
			Language:             "zh-CN,zh;q=0.9,en;q=0.8",
			ScreenWidth:          1920,
			ScreenHeight:         1080,
			RequestTimeoutSec:    300,
			StreamIdleTimeoutSec: 120,
			Proxy:                "",
			UserAgent:            defaultUserAgent,
		},
		Routing: RoutingSettings{
			Strategy:        "least_inflight",
			CooldownBaseSec: 60,
			CooldownMaxSec:  900,
			MaxAttempts:     3,
			CapacityWaitSec: 20,
			StickyTTLSec:    300,
			PreferIdle:      true,
		},
		Audit: AuditSettings{
			RetentionDays:  7,
			MaxRecords:     5000,
			RecordBody:     true,
			BodyLimitBytes: 8192,
		},
		Media: MediaSettings{
			GeneratedDir:   filepath.Join(dataDir, "generated"),
			PublicBaseURL:  "",
			MaxTotalSizeMB: 2048,
			AutoDownload:   true,
		},
		Signin: SigninSettings{
			// Off by default: it spends a request against every account in the
			// pool and is only useful once the operator has accounts they are
			// willing to automate.
			Enabled:           false,
			Hour:              9,
			Minute:            5,
			GapSeconds:        2,
			TimeoutSec:        30,
			SkipZeroCredit:    false,
			CreditFreshMin:    360,
			CreditRefreshMin:  30,
			Lang:              "en",
			OSName:            "Windows",
			BrowserName:       "Chrome",
			BrowserLanguage:   "en-US",
			BrowserPlatform:   "Win32",
			DeviceMemory:      16,
			CPUCoreNum:        8,
			TimezoneOffsetMin: 480,
			StatusPath:        "/minimax-cloud/api/v1/signin/status",
			ClaimPath:         "/minimax-cloud/api/v1/signin/claim",
			CreditPath:        "/matrix/api/v1/commerce/get_membership_info",
			CreditDetailsPath: "/minimax-cloud/api/v1/credit/details",
		},
		Video: VideoSettings{
			PluginName:        "video-creater",
			OptionsTag:        "video-generation-options",
			DefaultRatio:      "16:9",
			DefaultResolution: "768P",
			DefaultDuration:   5,
			// Comfortably above the fast variant's ~20s and long enough that a
			// slow H3 turn returns whatever progress the agent has reached
			// rather than a bare timeout. It cannot cover a full H3 render:
			// 15–30 minutes is beyond any synchronous HTTP surface, which is
			// why the slow model is documented as submit-and-follow-up.
			TimeoutSec: 600,
		},
	}
}

const defaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"

// Normalize repairs out-of-range values coming from a stored config file.
// Normalize fills in absent settings and repairs the known-broken legacy
// defaults, and reports whether it changed anything.
//
// The report matters because a repair that is not written back leaves the
// settings file describing a configuration the process is not using — and a file
// that disagrees with the running process is how a broken value survives a fix
// in the first place.
func (s *Settings) Normalize(dataDir string) bool {
	before := *s
	def := DefaultSettings(dataDir)
	if s.Server.MaxConcurrentRequests <= 0 {
		s.Server.MaxConcurrentRequests = def.Server.MaxConcurrentRequests
	}
	if s.Server.AdminUsername == "" {
		s.Server.AdminUsername = def.Server.AdminUsername
	}
	if s.Upstream.BaseURL == "" {
		s.Upstream.BaseURL = def.Upstream.BaseURL
	}
	if s.Upstream.BaseURLCN == "" {
		s.Upstream.BaseURLCN = def.Upstream.BaseURLCN
	}
	if s.Upstream.AgentID == "" {
		s.Upstream.AgentID = def.Upstream.AgentID
	}
	if s.Upstream.SessionPath == "" {
		s.Upstream.SessionPath = def.Upstream.SessionPath
	}
	// Repair the two known-broken legacy defaults. Kept next to the empty-value
	// repairs above rather than in a version-keyed migration table, because there
	// are two of them and a reader should be able to see at a glance what is
	// being changed and why. See the constants for what each one costs.
	if s.Upstream.SessionPath == legacyDefaultSessionPath {
		s.Upstream.SessionPath = def.Upstream.SessionPath
	}
	if s.Upstream.AgentID == legacyDefaultAgentID {
		s.Upstream.AgentID = def.Upstream.AgentID
	}
	if s.Upstream.MessagePath == "" {
		s.Upstream.MessagePath = def.Upstream.MessagePath
	}
	// The message path and the streaming host go together: the path was captured
	// on that host, and pointing one at the other is a combination that was never
	// observed. Migrating both keeps an upgraded instance on the pair the web
	// client actually uses.
	if s.Upstream.MessagePath == legacyDefaultMessagePath {
		s.Upstream.MessagePath = def.Upstream.MessagePath
	}
	if s.Upstream.UserInfoPath == "" {
		s.Upstream.UserInfoPath = def.Upstream.UserInfoPath
	}
	if s.Upstream.AgentListPath == "" {
		s.Upstream.AgentListPath = def.Upstream.AgentListPath
	}
	if s.Upstream.ConfigPath == "" {
		s.Upstream.ConfigPath = def.Upstream.ConfigPath
	}
	if s.Upstream.ConnectionsPath == "" {
		s.Upstream.ConnectionsPath = def.Upstream.ConnectionsPath
	}
	if s.Upstream.ScreenWidth <= 0 {
		s.Upstream.ScreenWidth = def.Upstream.ScreenWidth
	}
	if s.Upstream.ScreenHeight <= 0 {
		s.Upstream.ScreenHeight = def.Upstream.ScreenHeight
	}
	if s.Upstream.Language == "" {
		s.Upstream.Language = def.Upstream.Language
	}
	if s.Upstream.RequestTimeoutSec < 5 {
		s.Upstream.RequestTimeoutSec = def.Upstream.RequestTimeoutSec
	}
	if s.Upstream.StreamIdleTimeoutSec < 5 {
		s.Upstream.StreamIdleTimeoutSec = def.Upstream.StreamIdleTimeoutSec
	}
	if s.Upstream.UserAgent == "" {
		s.Upstream.UserAgent = def.Upstream.UserAgent
	}
	switch s.Routing.Strategy {
	case "least_inflight", "round_robin", "priority", "random":
	default:
		s.Routing.Strategy = def.Routing.Strategy
	}
	if s.Routing.CooldownBaseSec <= 0 {
		s.Routing.CooldownBaseSec = def.Routing.CooldownBaseSec
	}
	if s.Routing.CooldownMaxSec < s.Routing.CooldownBaseSec {
		s.Routing.CooldownMaxSec = def.Routing.CooldownMaxSec
	}
	if s.Routing.MaxAttempts < 1 || s.Routing.MaxAttempts > 20 {
		s.Routing.MaxAttempts = def.Routing.MaxAttempts
	}
	if s.Routing.CapacityWaitSec < 0 {
		s.Routing.CapacityWaitSec = def.Routing.CapacityWaitSec
	}
	if s.Routing.StickyTTLSec < 0 {
		s.Routing.StickyTTLSec = def.Routing.StickyTTLSec
	}
	if s.Audit.MaxRecords < 100 {
		s.Audit.MaxRecords = def.Audit.MaxRecords
	}
	if s.Audit.BodyLimitBytes < 256 {
		s.Audit.BodyLimitBytes = def.Audit.BodyLimitBytes
	}
	if s.Media.GeneratedDir == "" {
		s.Media.GeneratedDir = def.Media.GeneratedDir
	}
	if s.Media.MaxTotalSizeMB < 64 {
		s.Media.MaxTotalSizeMB = def.Media.MaxTotalSizeMB
	}

	if s.Signin.Hour < 0 || s.Signin.Hour > 23 {
		s.Signin.Hour = def.Signin.Hour
	}
	if s.Signin.Minute < 0 || s.Signin.Minute > 59 {
		s.Signin.Minute = def.Signin.Minute
	}
	if s.Signin.GapSeconds < 0 {
		s.Signin.GapSeconds = def.Signin.GapSeconds
	}
	if s.Signin.TimeoutSec < 5 {
		s.Signin.TimeoutSec = def.Signin.TimeoutSec
	}
	if s.Signin.CreditFreshMin <= 0 {
		// Zero is repaired rather than honoured: an unbounded freshness window
		// would let one stale zero hold an account out of rotation forever,
		// which is the failure this guard exists to avoid, not to create.
		s.Signin.CreditFreshMin = def.Signin.CreditFreshMin
	}
	if s.Signin.CreditRefreshMin < 0 {
		s.Signin.CreditRefreshMin = def.Signin.CreditRefreshMin
	}
	if s.Signin.DeviceMemory <= 0 {
		s.Signin.DeviceMemory = def.Signin.DeviceMemory
	}
	if s.Signin.CPUCoreNum <= 0 {
		s.Signin.CPUCoreNum = def.Signin.CPUCoreNum
	}
	if s.Signin.Lang == "" {
		s.Signin.Lang = def.Signin.Lang
	}
	if s.Signin.OSName == "" {
		s.Signin.OSName = def.Signin.OSName
	}
	if s.Signin.BrowserName == "" {
		s.Signin.BrowserName = def.Signin.BrowserName
	}
	if s.Signin.BrowserLanguage == "" {
		s.Signin.BrowserLanguage = def.Signin.BrowserLanguage
	}
	if s.Signin.BrowserPlatform == "" {
		s.Signin.BrowserPlatform = def.Signin.BrowserPlatform
	}
	if s.Signin.StatusPath == "" {
		s.Signin.StatusPath = def.Signin.StatusPath
	}
	if s.Signin.ClaimPath == "" {
		s.Signin.ClaimPath = def.Signin.ClaimPath
	}
	if s.Signin.CreditPath == "" {
		s.Signin.CreditPath = def.Signin.CreditPath
	}
	if s.Signin.CreditDetailsPath == "" {
		s.Signin.CreditDetailsPath = def.Signin.CreditDetailsPath
	}
	if s.Video.PluginName == "" {
		s.Video.PluginName = def.Video.PluginName
	}
	if s.Video.OptionsTag == "" {
		s.Video.OptionsTag = def.Video.OptionsTag
	}
	if s.Video.DefaultRatio == "" {
		s.Video.DefaultRatio = def.Video.DefaultRatio
	}
	if s.Video.DefaultResolution == "" {
		s.Video.DefaultResolution = def.Video.DefaultResolution
	}
	if s.Video.DefaultDuration <= 0 {
		s.Video.DefaultDuration = def.Video.DefaultDuration
	}
	if s.Video.TimeoutSec <= 0 {
		s.Video.TimeoutSec = def.Video.TimeoutSec
	}
	// TimezoneOffsetMin is deliberately not repaired here. A stored 0 cannot be
	// told apart from an absent field, and signinParams already maps 0 onto the
	// +8 default the capture used, so repairing it here would only add a second
	// place to get the same answer. The cost is that exactly-UTC is not
	// expressible; every other offset is, and the upstream appears to use this
	// value for reporting rather than for deciding when the day rolls over.
	return *s != before
}

func (s Settings) Clone() Settings {
	raw, err := json.Marshal(s)
	if err != nil {
		return s
	}
	var clone Settings
	if err := json.Unmarshal(raw, &clone); err != nil {
		return s
	}
	return clone
}

func (s Settings) RequestTimeout() time.Duration {
	return time.Duration(s.Upstream.RequestTimeoutSec) * time.Second
}

func (s Settings) StreamIdleTimeout() time.Duration {
	return time.Duration(s.Upstream.StreamIdleTimeoutSec) * time.Second
}

// VideoTimeout bounds one video-generation turn.
//
// It is a separate budget from RequestTimeout because the two have nothing in
// common: a chat turn that takes a minute is broken, and a video turn that
// takes a minute has not started yet.
func (s Settings) VideoTimeout() time.Duration {
	return time.Duration(s.Video.TimeoutSec) * time.Second
}

func (s Settings) CooldownBase() time.Duration {
	return time.Duration(s.Routing.CooldownBaseSec) * time.Second
}

func (s Settings) CooldownMax() time.Duration {
	return time.Duration(s.Routing.CooldownMaxSec) * time.Second
}

func (s Settings) CapacityWait() time.Duration {
	return time.Duration(s.Routing.CapacityWaitSec) * time.Second
}

func (s Settings) StickyTTL() time.Duration {
	return time.Duration(s.Routing.StickyTTLSec) * time.Second
}

func (s Settings) Retention() time.Duration {
	return time.Duration(s.Audit.RetentionDays) * 24 * time.Hour
}

func (s Settings) MediaLimitBytes() int64 {
	return int64(s.Media.MaxTotalSizeMB) * 1024 * 1024
}

// SigninClock is the configured local wall-clock time for the daily sweep,
// expressed as an offset from midnight.
func (s Settings) SigninClock() time.Duration {
	return time.Duration(s.Signin.Hour)*time.Hour + time.Duration(s.Signin.Minute)*time.Minute
}

// SigninGap is the pause between two accounts during a sweep.
func (s Settings) SigninGap() time.Duration {
	return time.Duration(s.Signin.GapSeconds) * time.Second
}

// SigninTimeout bounds a single check-in call.
func (s Settings) SigninTimeout() time.Duration {
	seconds := s.Signin.TimeoutSec
	if seconds <= 0 {
		seconds = 30
	}
	return time.Duration(seconds) * time.Second
}

// CreditFresh is how long a balance reading stays actionable for routing.
func (s Settings) CreditFresh() time.Duration {
	return time.Duration(s.Signin.CreditFreshMin) * time.Minute
}

// CreditRefresh is the background balance polling interval.
func (s Settings) CreditRefresh() time.Duration {
	return time.Duration(s.Signin.CreditRefreshMin) * time.Minute
}

// ParseIntOrDefault is a small helper for query parameters.
func ParseIntOrDefault(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}
