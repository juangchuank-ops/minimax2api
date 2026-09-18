package minimax

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"minimax2api/internal/config"
)

// International check-in ("Daily check-in") endpoints.
//
// These live on the same host as the agent API but under a different prefix,
// and they are NOT interchangeable with the mainland deployment: the query
// carries `timezone_offset` rather than `timezone_id`, and the parameter set
// differs. Only the international host is implemented here; a mainland
// account is skipped rather than signed with the wrong protocol.
const (
	DefaultSigninStatusPath = "/minimax-cloud/api/v1/signin/status"
	DefaultSigninClaimPath  = "/minimax-cloud/api/v1/signin/claim"
	// DefaultCreditPath returns the account's live credit balance. The older
	// /matrix/api/v1/commerce/get_credit_details is dead weight after the
	// migration to "op" credits: it answers 200 with every field zero while
	// this one reports the real number.
	DefaultCreditPath = "/matrix/api/v1/commerce/get_membership_info"
)

// Signin day status values observed in the wild.
const (
	SigninDayUnclaimed = 1
	SigninDayClaimed   = 3
)

// Signin claim result codes. 1 is a first claim, 2 is the idempotent
// "already claimed today" answer.
const (
	SigninClaimOK        = 1
	SigninClaimDuplicate = 2
)

// statusCodeSessionExpired is the upstream code for a rejected session.
const statusCodeSessionExpired = 1022100011

// SigninDay is one slot of the seven-day cycle.
type SigninDay struct {
	DayNo   int  `json:"dayNo"`
	Points  int  `json:"points"`
	Status  int  `json:"status"`
	IsToday bool `json:"isToday"`
}

// SigninPanel is the decoded /signin/status payload.
type SigninPanel struct {
	Scene        int         `json:"scene"`
	Days         []SigninDay `json:"days"`
	ClaimedToday bool        `json:"claimedToday"`
	TodayPoints  int         `json:"todayPoints"`
	TodayDayNo   int         `json:"todayDayNo"`
}

// SigninClaim is the decoded /signin/claim payload.
type SigninClaim struct {
	ClaimID    int64 `json:"claimId"`
	Result     int   `json:"result"`
	DayNo      int   `json:"dayNo"`
	Points     int   `json:"points"`
	ExpireAtMs int64 `json:"expireAtMs"`
	// Panel is the board as it looks *after* the claim, when the upstream
	// includes it. It is the authoritative post-claim state: storing the panel
	// fetched before the claim would leave today showing as unclaimed next to a
	// "claimed" status, which is a self-contradicting console.
	Panel *SigninPanel `json:"panel"`
}

// IsDuplicate reports the idempotent "today was already claimed" answer.
//
// Derived on demand instead of cached in a field: a struct literal that sets
// Result but forgets the flag is easy to write, and the scheduler and the
// console would then disagree about whether the claim succeeded.
func (c *SigninClaim) IsDuplicate() bool {
	return c != nil && c.Result == SigninClaimDuplicate
}

// CreditInfo is the account's live balance.
type CreditInfo struct {
	Total     int    `json:"total"`
	Free      int    `json:"free"`
	Purchased int    `json:"purchased"`
	PlanName  string `json:"planName"`
	PlanType  int    `json:"planType"`
}

// ------------------------------------------------------------------- request

// signinParams renders the check-in query string in the exact order the web
// bundle builds it. The order is load-bearing: `yy` is an MD5 over the encoded
// URL, so reordering a single pair invalidates the signature.
//
// forSignature selects between the two strings the frontend actually produces.
// The bundle puts `op_ticket: undefined` into its parameter object; URLSearchParams
// stringifies that as the literal text "undefined", but axios drops undefined
// values when it serialises the real request. So the signed URL contains
// `op_ticket=undefined` and the request that goes on the wire does not. Using
// either string for both purposes produces a signature the server rejects.
func signinParams(settings config.Settings, cred Credential, unixMs int64, forSignature bool) string {
	signin := settings.Signin
	lang := strings.TrimSpace(signin.Lang)
	if lang == "" {
		lang = "en"
	}
	osName := strings.TrimSpace(signin.OSName)
	if osName == "" {
		osName = "Windows"
	}
	browserName := strings.TrimSpace(signin.BrowserName)
	if browserName == "" {
		browserName = "Chrome"
	}
	browserLanguage := strings.TrimSpace(signin.BrowserLanguage)
	if browserLanguage == "" {
		browserLanguage = "en-US"
	}
	browserPlatform := strings.TrimSpace(signin.BrowserPlatform)
	if browserPlatform == "" {
		browserPlatform = "Win32"
	}
	width := cred.ScreenWidth
	if width <= 0 {
		width = settings.Upstream.ScreenWidth
	}
	height := cred.ScreenHeight
	if height <= 0 {
		height = settings.Upstream.ScreenHeight
	}
	offsetMinutes := signin.TimezoneOffsetMin
	if offsetMinutes == 0 {
		offsetMinutes = 480
	}
	deviceMemory := signin.DeviceMemory
	if deviceMemory <= 0 {
		deviceMemory = 16
	}
	cpuCores := signin.CPUCoreNum
	if cpuCores <= 0 {
		cpuCores = 8
	}
	userID := cred.UserID
	if strings.TrimSpace(userID) == "" {
		userID = "0"
	}

	pairs := []struct{ key, value string }{
		{"device_platform", "web"},
		{"biz_id", "3"},
		{"app_id", "3001"},
		{"version_code", "22201"},
		{"unix", strconv.FormatInt(unixMs, 10)},
		// Note the sign: the bundle computes -60 * getTimezoneOffset(), and
		// UTC+8 reports -480, so the value is +28800 rather than -480.
		{"timezone_offset", strconv.Itoa(offsetMinutes * 60)},
		{"sys_language", lang},
		{"lang", lang},
		{"uuid", cred.UUID},
		{"device_id", cred.DeviceID},
		{"os_name", osName},
		{"browser_name", browserName},
		{"device_memory", strconv.Itoa(deviceMemory)},
		{"cpu_core_num", strconv.Itoa(cpuCores)},
		{"browser_language", browserLanguage},
		{"browser_platform", browserPlatform},
		{"user_id", userID},
		{"op_ticket", "undefined"},
		{"screen_width", strconv.Itoa(width)},
		{"screen_height", strconv.Itoa(height)},
		{"token", cred.Token},
		{"client", "web"},
	}

	var builder strings.Builder
	for _, pair := range pairs {
		if pair.key == "op_ticket" && !forSignature {
			// axios drops undefined; the wire request has no such parameter.
			continue
		}
		if builder.Len() > 0 {
			builder.WriteByte('&')
		}
		builder.WriteString(pair.key)
		builder.WriteByte('=')
		builder.WriteString(formEncode(pair.value))
	}
	return builder.String()
}

// formEncode percent-encodes one query value the way URLSearchParams does:
// the same character set as encodeURIComponent, except a space becomes "+".
func formEncode(value string) string {
	return strings.ReplaceAll(encodeURIComponent(value), "%20", "+")
}

// signinRequest builds a signed check-in request.
//
// Unlike the agent endpoints, `yy` here is computed over the *relative* path
// plus query, never the absolute URL: axios keeps baseURL separate from url, so
// the signature sees only "/minimax-cloud/api/v1/signin/status?...".
func (c *Client) signinRequest(ctx context.Context, settings config.Settings, cred Credential, method, path, body string) (*http.Request, error) {
	now := time.Now()
	signedPath := path + "?" + signinParams(settings, cred, now.UnixMilli(), true)
	wirePath := path + "?" + signinParams(settings, cred, now.UnixMilli(), false)

	// yyPayload is "{}" for both the GET and the POST. The body string that
	// feeds x-signature is different: an empty body for the GET, "{}" for the
	// POST.
	yyPayload := "{}"

	var reader io.Reader
	if method == http.MethodPost {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL(settings, cred)+wirePath, reader)
	if err != nil {
		return nil, err
	}

	req.Header.Set("accept", "*/*")
	req.Header.Set("accept-language", settings.Upstream.Language)
	req.Header.Set("content-type", "application/json")
	req.Header.Set("origin", c.baseURL(settings, cred))
	req.Header.Set("referer", c.baseURL(settings, cred)+"/")
	req.Header.Set("user-agent", settings.Upstream.UserAgent)

	req.Header.Set("token", cred.Token)
	req.Header.Set("x-timestamp", strconv.FormatInt(now.Unix(), 10))
	req.Header.Set("x-signature", XSignature(now, body))
	req.Header.Set("yy", YY(signedPath, yyPayload, now))
	return req, nil
}

// call issues one signed check-in request and returns the decoded envelope.
func (c *Client) callSignin(ctx context.Context, settings config.Settings, cred Credential, method, path, body string) (map[string]any, error) {
	if strings.TrimSpace(cred.Token) == "" {
		return nil, ErrInvalidCredential
	}
	req, err := c.signinRequest(ctx, settings, cred, method, path, body)
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
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("minimax signin HTTP %d: %s", resp.StatusCode, snippet(raw))
	}

	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("minimax signin 返回非 JSON: %s", snippet(raw))
	}
	if err := envelopeError(payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// envelopeError converts the upstream's base_resp/statusInfo envelope into an
// error, mapping a rejected session onto ErrInvalidCredential so the caller can
// retire the account instead of merely cooling it down.
func envelopeError(payload map[string]any) error {
	for _, key := range []string{"base_resp", "statusInfo"} {
		node, ok := payload[key].(map[string]any)
		if !ok {
			continue
		}
		code := intOf(node["status_code"])
		if code == 0 {
			code = intOf(node["code"])
		}
		if code == 0 {
			continue
		}
		if code == statusCodeSessionExpired {
			return ErrInvalidCredential
		}
		message := stringOf(node["status_msg"])
		if message == "" {
			message = stringOf(node["message"])
		}
		return fmt.Errorf("上游返回 %d %s", code, message)
	}
	return nil
}

// ------------------------------------------------------------------ sign-in

// SigninStatus fetches the seven-day check-in panel.
func (c *Client) SigninStatus(ctx context.Context, cred Credential) (*SigninPanel, error) {
	settings := c.settings()
	ctx, cancel := context.WithTimeout(ctx, signinTimeout(settings))
	defer cancel()

	path := strings.TrimSpace(settings.Signin.StatusPath)
	if path == "" {
		path = DefaultSigninStatusPath
	}
	payload, err := c.callSignin(ctx, settings, cred, http.MethodGet, path, "")
	if err != nil {
		return nil, err
	}
	return parsePanel(coreOf(payload)), nil
}

// parsePanel decodes the seven-day board out of an envelope body.
//
// Shared by /signin/status and the panel the claim endpoint echoes back, so the
// two cannot drift apart in how they read a day.
func parsePanel(core map[string]any) *SigninPanel {
	panel := &SigninPanel{Scene: intOf(core["scene"])}
	days, _ := core["days"].([]any)
	for _, entry := range days {
		item, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		day := SigninDay{
			DayNo:   intOf(item["day_no"]),
			Points:  intOf(item["points"]),
			Status:  intOf(item["status"]),
			IsToday: boolOf(item["is_today"]),
		}
		panel.Days = append(panel.Days, day)
		if day.IsToday {
			panel.TodayDayNo = day.DayNo
			panel.TodayPoints = day.Points
			panel.ClaimedToday = day.Status == SigninDayClaimed
		}
	}
	return panel
}

// SigninClaim claims today's credits.
//
// The endpoint is idempotent: a second call on the same day answers
// claim_result=2 instead of an error, so a retry after an ambiguous failure is
// safe and does not double-credit.
func (c *Client) SigninClaim(ctx context.Context, cred Credential) (*SigninClaim, error) {
	settings := c.settings()
	ctx, cancel := context.WithTimeout(ctx, signinTimeout(settings))
	defer cancel()

	path := strings.TrimSpace(settings.Signin.ClaimPath)
	if path == "" {
		path = DefaultSigninClaimPath
	}
	payload, err := c.callSignin(ctx, settings, cred, http.MethodPost, path, "{}")
	if err != nil {
		return nil, err
	}
	core := coreOf(payload)
	claim := &SigninClaim{
		ClaimID:    int64Of(core["claim_id"]),
		Result:     intOf(core["claim_result"]),
		DayNo:      intOf(core["day_no"]),
		Points:     intOf(core["points"]),
		ExpireAtMs: int64Of(core["expire_at_ms"]),
	}
	if node, ok := core["panel"].(map[string]any); ok {
		if parsed := parsePanel(node); len(parsed.Days) > 0 {
			claim.Panel = parsed
		}
	}
	return claim, nil
}

// ------------------------------------------------------------------- credit

// Credit reads the account's live credit balance.
//
// The balance lives in op_credit_summary.total_remaining_amount, which the
// upstream sends as a *string*. The two flat fields beside it
// (opcredit_balance, total_remains_credit) are the pre-migration figures and
// stay at zero for every account that has been migrated, so they are only used
// as a fallback.
func (c *Client) Credit(ctx context.Context, cred Credential) (*CreditInfo, error) {
	settings := c.settings()
	ctx, cancel := context.WithTimeout(ctx, signinTimeout(settings))
	defer cancel()

	path := strings.TrimSpace(settings.Signin.CreditPath)
	if path == "" {
		path = DefaultCreditPath
	}
	payload, err := c.callSignin(ctx, settings, cred, http.MethodPost, path, "{}")
	if err != nil {
		return nil, err
	}
	core := coreOf(payload)

	info := &CreditInfo{
		PlanName: stringOf(core["plan_name"]),
		PlanType: intOf(core["plan_type"]),
	}
	if summary, ok := core["op_credit_summary"].(map[string]any); ok {
		info.Total = numberOf(summary["total_remaining_amount"])
		info.Free = numberOf(summary["free_remaining_amount"])
		info.Purchased = numberOf(summary["purchased_remaining_amount"])
		return info, nil
	}
	// No summary section: fall back to the flat fields, newest first.
	info.Total = numberOf(core["opcredit_balance"])
	if info.Total == 0 {
		info.Total = numberOf(core["total_remains_credit"])
	}
	return info, nil
}

// ------------------------------------------------------------------ helpers

// signinTimeout bounds a check-in call. These endpoints answer immediately, so
// the generous agent timeout would only make a stuck account hang the batch.
func signinTimeout(settings config.Settings) time.Duration {
	seconds := settings.Signin.TimeoutSec
	if seconds <= 0 {
		seconds = 30
	}
	return time.Duration(seconds) * time.Second
}

// coreOf unwraps the {data: ...} envelope, falling back to the root object for
// endpoints that answer flat (the credit endpoint does).
func coreOf(payload map[string]any) map[string]any {
	if core, ok := payload["data"].(map[string]any); ok {
		return core
	}
	return payload
}

func intOf(value any) int {
	switch node := value.(type) {
	case float64:
		return int(node)
	case int:
		return node
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(node))
		if err != nil {
			return 0
		}
		return parsed
	}
	return 0
}

func int64Of(value any) int64 {
	switch node := value.(type) {
	case float64:
		return int64(node)
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(node), 10, 64)
		if err != nil {
			return 0
		}
		return parsed
	}
	return 0
}

// numberOf reads a number that the upstream may send as a string, a float or
// an int. The credit fields arrive as strings ("400"), so a plain float64
// assertion silently yields zero.
func numberOf(value any) int {
	switch node := value.(type) {
	case string:
		trimmed := strings.TrimSpace(node)
		if trimmed == "" {
			return 0
		}
		parsed, err := strconv.ParseFloat(trimmed, 64)
		if err != nil {
			return 0
		}
		return int(parsed)
	default:
		return intOf(value)
	}
}

func stringOf(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func boolOf(value any) bool {
	if flag, ok := value.(bool); ok {
		return flag
	}
	return false
}
