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
	UserInfoPath         string `json:"userInfoPath"`
	ModelPayload         string `json:"modelPayload"`
	Language             string `json:"language"`
	ScreenWidth          int    `json:"screenWidth"`
	ScreenHeight         int    `json:"screenHeight"`
	RequestTimeoutSec    int    `json:"requestTimeoutSec"`
	StreamIdleTimeoutSec int    `json:"streamIdleTimeoutSec"`
	Proxy                string `json:"proxy"`
	UserAgent            string `json:"userAgent"`
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

// DefaultSettings returns the built-in runtime configuration.
func DefaultSettings(dataDir string) Settings {
	return Settings{
		Server: ServerSettings{
			Addr:                  "127.0.0.1:8080",
			MaxConcurrentRequests: 64,
			AdminUsername:         "admin",
		},
		Upstream: UpstreamSettings{
			BaseURL:              "https://agent.minimax.io",
			BaseURLCN:            "https://agent.minimaxi.com",
			AgentID:              "general",
			SessionPath:          "/agent/{agent_id}/session",
			MessagePath:          "/archon/api/v1/session/{session_id}/message",
			UserInfoPath:         "/v1/api/user/info",
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
		},
	}
}

const defaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"

// Normalize repairs out-of-range values coming from a stored config file.
func (s *Settings) Normalize(dataDir string) {
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
	if s.Upstream.MessagePath == "" {
		s.Upstream.MessagePath = def.Upstream.MessagePath
	}
	if s.Upstream.UserInfoPath == "" {
		s.Upstream.UserInfoPath = def.Upstream.UserInfoPath
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
	// TimezoneOffsetMin is deliberately not repaired here. A stored 0 cannot be
	// told apart from an absent field, and signinParams already maps 0 onto the
	// +8 default the capture used, so repairing it here would only add a second
	// place to get the same answer. The cost is that exactly-UTC is not
	// expressible; every other offset is, and the upstream appears to use this
	// value for reporting rather than for deciding when the day rolls over.
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
