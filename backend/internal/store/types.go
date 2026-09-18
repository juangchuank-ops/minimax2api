package store

import (
	"time"

	"minimax2api/internal/config"
)

// Account status values.
const (
	StatusActive   = "active"
	StatusCooldown = "cooldown"
	StatusDisabled = "disabled"
	StatusInvalid  = "invalid"
)

// Account kinds.
const (
	KindToken = "token"
	KindGuest = "guest"
)

// Account regions. MiniMax operates two deployments with separate account
// databases, and a token issued by one is rejected by the other, so the region
// has to travel with the account.
const (
	RegionCN     = "cn"
	RegionGlobal = "global"
)

// Quota describes the last probe of an account.
type Quota struct {
	SyncedAt  time.Time `json:"syncedAt"`
	Available bool      `json:"available"`
	LatencyMs int64     `json:"latencyMs"`
	Plan      string    `json:"plan"`
	Note      string    `json:"note"`
}

// Check-in outcome values stored on an account.
const (
	SigninOK      = "ok"      // claimed (or already claimed) today
	SigninAlready = "already" // upstream said today's credits were taken
	SigninFailed  = "failed"
	SigninSkipped = "skipped" // mainland account, disabled, or no fingerprint
)

// SigninDay is one slot of the seven-day check-in cycle.
type SigninDay struct {
	DayNo   int  `json:"dayNo"`
	Points  int  `json:"points"`
	Status  int  `json:"status"`
	IsToday bool `json:"isToday"`
}

// SigninPanel is the persisted view of the upstream check-in board. It is kept
// so the console can render the seven-day strip without calling upstream on
// every page load.
type SigninPanel struct {
	Scene int         `json:"scene"`
	Days  []SigninDay `json:"days"`
}

// Credit is the last observed balance.
//
// Total is the number that matters for routing. The upstream reports it inside
// op_credit_summary.total_remaining_amount; the flat fields beside it belong to
// the pre-migration credit system and read zero on every migrated account.
type Credit struct {
	Total     int       `json:"total"`
	Free      int       `json:"free"`
	Purchased int       `json:"purchased"`
	PlanName  string    `json:"planName"`
	PlanType  int       `json:"planType"`
	SyncedAt  time.Time `json:"syncedAt"`
}

// Exhausted reports whether the balance is known to be spent.
func (c *Credit) Exhausted() bool {
	return c != nil && c.Total <= 0
}

type Account struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Region string `json:"region"`
	// Token is the JWT lifted from the agent site's localStorage.
	Token string `json:"token"`
	// UserID is the account's realUserID. The mainland deployment expects it
	// alongside the token, and it doubles as a stable display identifier.
	UserID string `json:"userId"`
	// Identifier is a best-effort email or phone number decoded from the token,
	// used to tell accounts apart in the console.
	Identifier string `json:"identifier"`
	// AgentID pins the account to a specific agent; empty means use the global
	// default.
	AgentID string `json:"agentID"`
	// The three fields below form the browser fingerprint. They are not
	// cosmetic: the `yy` signature is computed over the query string built from
	// them, so a token must be replayed with the fingerprint it was issued to.
	DeviceID     string `json:"deviceID"`
	UUID         string `json:"uuid"`
	ScreenWidth  int    `json:"screenWidth"`
	ScreenHeight int    `json:"screenHeight"`
	// BaseURL optionally overrides the host for this account.
	BaseURL       string    `json:"baseURL"`
	Group         string    `json:"group"`
	Remark        string    `json:"remark"`
	Enabled       bool      `json:"enabled"`
	Priority      int       `json:"priority"`
	MaxConcurrent int       `json:"maxConcurrent"`
	Status        string    `json:"status"`
	CooldownUntil time.Time `json:"cooldownUntil"`
	FailCount     int       `json:"failCount"`
	SuccessCount  int       `json:"successCount"`
	LastUsedAt    time.Time `json:"lastUsedAt"`
	LastError     string    `json:"lastError"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
	Quota         *Quota    `json:"quota,omitempty"`
	// SigninAt is the last check-in attempt, successful or not. It doubles as
	// the scheduler's "already swept today" marker, which is why it is written
	// even on failure: a day where every account errored must not be retried on
	// every tick.
	SigninAt     time.Time    `json:"signinAt"`
	SigninStatus string       `json:"signinStatus"`
	SigninStreak int          `json:"signinStreak"`
	SigninPoints int          `json:"signinPoints"`
	SigninTotal  int64        `json:"signinTotal"`
	SigninError  string       `json:"signinError"`
	SigninPanel  *SigninPanel `json:"signinPanel,omitempty"`
	// Credit is the last observed balance, refreshed by the background poller
	// and by the sweep. It is advisory: routing only acts on it while it is
	// fresh (see Settings.CreditFresh).
	Credit *Credit `json:"credit,omitempty"`
}

// AccountView is the API representation. It is an explicit projection rather
// than an embedded struct so the raw token can never be serialised by accident.
type AccountView struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Kind          string    `json:"kind"`
	Region        string    `json:"region"`
	UserID        string    `json:"userId"`
	Identifier    string    `json:"identifier"`
	AgentID       string    `json:"agentID"`
	DeviceID      string    `json:"deviceID"`
	UUID          string    `json:"uuid"`
	ScreenWidth   int       `json:"screenWidth"`
	ScreenHeight  int       `json:"screenHeight"`
	BaseURL       string    `json:"baseURL"`
	Group         string    `json:"group"`
	Remark        string    `json:"remark"`
	Enabled       bool      `json:"enabled"`
	Priority      int       `json:"priority"`
	MaxConcurrent int       `json:"maxConcurrent"`
	Status        string    `json:"status"`
	CooldownUntil time.Time `json:"cooldownUntil"`
	FailCount     int       `json:"failCount"`
	SuccessCount  int       `json:"successCount"`
	LastUsedAt    time.Time `json:"lastUsedAt"`
	LastError     string    `json:"lastError"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
	// No omitempty: the console declares quota as a required field that may be
	// null, so the key must always be present. Omitting it would make the
	// property undefined instead of null and silently break strict checks.
	Quota        *Quota       `json:"quota"`
	SigninAt     time.Time    `json:"signinAt"`
	SigninStatus string       `json:"signinStatus"`
	SigninStreak int          `json:"signinStreak"`
	SigninPoints int          `json:"signinPoints"`
	SigninTotal  int64        `json:"signinTotal"`
	SigninError  string       `json:"signinError"`
	SigninPanel  *SigninPanel `json:"signinPanel"`
	Credit       *Credit      `json:"credit"`
	TokenMasked  string       `json:"tokenMasked"`
	Inflight     int          `json:"inflight"`
}

// NewAccountView projects an account for API responses.
func NewAccountView(account *Account, inflight int) AccountView {
	return AccountView{
		ID: account.ID, Name: account.Name, Kind: account.Kind, Region: account.Region,
		UserID: account.UserID, Identifier: account.Identifier, AgentID: account.AgentID,
		DeviceID: account.DeviceID, UUID: account.UUID,
		ScreenWidth: account.ScreenWidth, ScreenHeight: account.ScreenHeight,
		BaseURL: account.BaseURL, Group: account.Group, Remark: account.Remark,
		Enabled: account.Enabled, Priority: account.Priority,
		MaxConcurrent: account.MaxConcurrent, Status: account.Status,
		CooldownUntil: account.CooldownUntil, FailCount: account.FailCount,
		SuccessCount: account.SuccessCount, LastUsedAt: account.LastUsedAt,
		LastError: account.LastError, CreatedAt: account.CreatedAt, UpdatedAt: account.UpdatedAt,
		Quota: account.Quota, TokenMasked: MaskToken(account.Token), Inflight: inflight,
		SigninAt: account.SigninAt, SigninStatus: account.SigninStatus,
		SigninStreak: account.SigninStreak, SigninPoints: account.SigninPoints,
		SigninTotal: account.SigninTotal, SigninError: account.SigninError,
		SigninPanel: account.SigninPanel, Credit: account.Credit,
	}
}

type ClientKey struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Key           string    `json:"key"`
	Enabled       bool      `json:"enabled"`
	RPMLimit      int       `json:"rpmLimit"`
	MaxConcurrent int       `json:"maxConcurrent"`
	TotalRequests int64     `json:"totalRequests"`
	CreatedAt     time.Time `json:"createdAt"`
	LastUsedAt    time.Time `json:"lastUsedAt"`
}

type Audit struct {
	ID               string    `json:"id"`
	CreatedAt        time.Time `json:"createdAt"`
	KeyName          string    `json:"keyName"`
	Model            string    `json:"model"`
	AccountName      string    `json:"accountName"`
	Status           int       `json:"status"`
	LatencyMs        int64     `json:"latencyMs"`
	FirstTokenMs     int64     `json:"firstTokenMs"`
	PromptTokens     int       `json:"promptTokens"`
	CompletionTokens int       `json:"completionTokens"`
	Stream           bool      `json:"stream"`
	Retries          int       `json:"retries"`
	IP               string    `json:"ip"`
	UserAgent        string    `json:"userAgent"`
	Error            string    `json:"error"`
	RequestBody      string    `json:"requestBody"`
	ResponseBody     string    `json:"responseBody"`
}

type MediaItem struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	URL         string    `json:"url"`
	SourceURL   string    `json:"sourceUrl"`
	Prompt      string    `json:"prompt"`
	Model       string    `json:"model"`
	AccountName string    `json:"accountName"`
	CreatedAt   time.Time `json:"createdAt"`
}

type Admin struct {
	Username     string `json:"username"`
	PasswordHash string `json:"passwordHash"`
	Salt         string `json:"salt"`
}

type ModelConfig struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Upstream    string `json:"upstream"`
	Type        string `json:"type"`
	Enabled     bool   `json:"enabled"`
	Builtin     bool   `json:"builtin"`
	Description string `json:"description"`
	Requests    int64  `json:"requests"`
	Tokens      int64  `json:"tokens"`
}

type State struct {
	Version    int             `json:"version"`
	Admin      Admin           `json:"admin"`
	Settings   config.Settings `json:"settings"`
	Accounts   []*Account      `json:"accounts"`
	ClientKeys []*ClientKey    `json:"clientKeys"`
	Audits     []*Audit        `json:"audits"`
	Media      []*MediaItem    `json:"media"`
	Models     []*ModelConfig  `json:"models"`
}

// BuiltinModels is the model catalogue exposed through /v1/models.
//
// Every entry reaches the same upstream agent: MiniMax Agent has no model
// selector in its web API, it serves whatever the account is entitled to. The
// catalogue exists so clients that insist on naming a model have something
// valid to send, and so the console can report usage per label.
func BuiltinModels() []*ModelConfig {
	return []*ModelConfig{
		{
			ID: "minimax-agent", Name: "MiniMax Agent", Upstream: "agent", Type: "chat",
			Enabled: true, Builtin: true, Description: "通用 Agent，自动规划并调用工具",
		},
		{
			ID: "minimax-m3", Name: "MiniMax M3", Upstream: "chat", Type: "chat",
			Enabled: true, Builtin: true, Description: "对话模式，响应更快",
		},
		{
			ID: "minimax-m3-thinking", Name: "MiniMax M3 Thinking", Upstream: "think", Type: "chat",
			Enabled: true, Builtin: true, Description: "深度思考模式，附带推理内容",
		},
		{
			ID: "minimax-image", Name: "MiniMax Image", Upstream: "image", Type: "image",
			Enabled: true, Builtin: true, Description: "图像生成",
		},
	}
}
