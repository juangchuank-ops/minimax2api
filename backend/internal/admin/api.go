package admin

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"minimax2api/internal/config"
	"minimax2api/internal/gateway"
	"minimax2api/internal/minimax"
	"minimax2api/internal/pool"
	"minimax2api/internal/signin"
	"minimax2api/internal/store"
)

// API serves the admin console.
type API struct {
	store    *store.Store
	pool     *pool.Pool
	client   *minimax.Client
	signin   *signin.Service
	settings func() config.Settings
	started  time.Time
}

func New(st *store.Store, p *pool.Pool, client *minimax.Client, signinSvc *signin.Service, settings func() config.Settings) *API {
	return &API{store: st, pool: p, client: client, signin: signinSvc, settings: settings, started: time.Now()}
}

// Register installs every admin route on the mux.
func (a *API) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /admin/api/auth/login", a.login)
	mux.HandleFunc("GET /admin/api/auth/me", a.guard(a.me))
	mux.HandleFunc("POST /admin/api/auth/logout", a.guard(a.logout))
	mux.HandleFunc("POST /admin/api/auth/password", a.guard(a.changePassword))

	mux.HandleFunc("GET /admin/api/dashboard", a.guard(a.dashboard))

	mux.HandleFunc("GET /admin/api/accounts", a.guard(a.listAccounts))
	mux.HandleFunc("POST /admin/api/accounts", a.guard(a.createAccount))
	mux.HandleFunc("GET /admin/api/accounts/groups", a.guard(a.accountGroups))
	mux.HandleFunc("GET /admin/api/accounts/export", a.guard(a.exportAccounts))
	mux.HandleFunc("POST /admin/api/accounts/import", a.guard(a.importAccounts))
	mux.HandleFunc("POST /admin/api/accounts/batch", a.guard(a.batchAccounts))
	mux.HandleFunc("POST /admin/api/accounts/probe-all", a.guard(a.probeAll))
	mux.HandleFunc("POST /admin/api/accounts/quota-all", a.guard(a.quotaAll))
	mux.HandleFunc("POST /admin/api/accounts/cleanup", a.guard(a.cleanupAccounts))
	mux.HandleFunc("PATCH /admin/api/accounts/{id}", a.guard(a.updateAccount))
	mux.HandleFunc("DELETE /admin/api/accounts/{id}", a.guard(a.deleteAccount))
	mux.HandleFunc("POST /admin/api/accounts/{id}/probe", a.guard(a.probeAccount))
	mux.HandleFunc("POST /admin/api/accounts/{id}/quota", a.guard(a.refreshQuota))
	mux.HandleFunc("POST /admin/api/accounts/{id}/signin", a.guard(a.signinAccount))
	mux.HandleFunc("POST /admin/api/accounts/{id}/credit", a.guard(a.refreshCredit))

	mux.HandleFunc("GET /admin/api/signin", a.guard(a.signinOverview))
	mux.HandleFunc("POST /admin/api/signin/run", a.guard(a.signinRun))

	mux.HandleFunc("GET /admin/api/client-keys", a.guard(a.listClientKeys))
	mux.HandleFunc("POST /admin/api/client-keys", a.guard(a.createClientKey))
	mux.HandleFunc("PATCH /admin/api/client-keys/{id}", a.guard(a.updateClientKey))
	mux.HandleFunc("DELETE /admin/api/client-keys/{id}", a.guard(a.deleteClientKey))

	mux.HandleFunc("GET /admin/api/models", a.guard(a.listModels))
	mux.HandleFunc("PATCH /admin/api/models/{id}", a.guard(a.updateModel))

	mux.HandleFunc("GET /admin/api/audits", a.guard(a.listAudits))
	mux.HandleFunc("DELETE /admin/api/audits", a.guard(a.clearAudits))
	mux.HandleFunc("GET /admin/api/audits/{id}", a.guard(a.auditDetail))

	mux.HandleFunc("GET /admin/api/settings", a.guard(a.getSettings))
	mux.HandleFunc("PUT /admin/api/settings", a.guard(a.saveSettings))

	mux.HandleFunc("GET /admin/api/gallery", a.guard(a.gallery))
}

// ---------------------------------------------------------------- middleware

type ctxKey string

const sessionKey ctxKey = "session"

func (a *API) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := bearer(r)
		if token == "" || !a.store.ValidateSession(token) {
			writeError(w, http.StatusUnauthorized, "登录已过期，请重新登录")
			return
		}
		next(w, r)
	}
}

func bearer(r *http.Request) string {
	raw := strings.TrimSpace(r.Header.Get("authorization"))
	if len(raw) > 7 && strings.EqualFold(raw[:7], "bearer ") {
		return strings.TrimSpace(raw[7:])
	}
	return ""
}

// --------------------------------------------------------------------- auth

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	var request loginRequest
	if err := decode(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if !a.store.VerifyPassword(strings.TrimSpace(request.Username), request.Password) {
		writeError(w, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	token, _ := a.store.IssueSession()
	writeJSON(w, http.StatusOK, map[string]any{
		"token":    token,
		"username": request.Username,
		"role":     "admin",
	})
}

func (a *API) me(w http.ResponseWriter, r *http.Request) {
	admin := a.store.AdminProfile()
	writeJSON(w, http.StatusOK, map[string]any{"username": admin.Username, "role": "admin"})
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	a.store.RevokeSession(bearer(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *API) changePassword(w http.ResponseWriter, r *http.Request) {
	var request struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err := decode(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	admin := a.store.AdminProfile()
	if !a.store.VerifyPassword(admin.Username, request.CurrentPassword) {
		writeError(w, http.StatusBadRequest, "当前密码不正确")
		return
	}
	if err := a.store.SetPassword(admin.Username, request.NewPassword); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.store.RevokeAllSessions()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------------------------------------------------------------- dashboard

func (a *API) dashboard(w http.ResponseWriter, r *http.Request) {
	period := r.URL.Query().Get("period")
	days := parsePeriodDays(period)
	timezone := r.URL.Query().Get("timezone")
	location := time.UTC
	if timezone != "" {
		if parsed, err := time.LoadLocation(timezone); err == nil {
			location = parsed
		}
	}

	settings := a.settings()
	now := time.Now()
	windowStart := now.AddDate(0, 0, -days)

	audits := a.store.ListAudits()
	accounts := a.store.ListAccounts()
	models := a.store.ListModels()
	keys := a.store.ListClientKeys()

	var (
		requests         int
		failures         int
		promptTokens     int
		completionTokens int
		reasoningTokens  int
		firstTokenSum    int64
		firstTokenCount  int
		latencySum       int64
		latencyCount     int
	)

	modelCounts := map[string]int{}
	modelTokens := map[string]int{}
	bucketRequests := map[string]int{}
	bucketFailures := map[string]int{}
	bucketTokens := map[string]int{}

	for _, audit := range audits {
		if audit.CreatedAt.Before(windowStart) {
			continue
		}
		requests++
		if audit.Status >= 400 {
			failures++
		}
		promptTokens += audit.PromptTokens
		completionTokens += audit.CompletionTokens
		if audit.FirstTokenMs > 0 {
			firstTokenSum += audit.FirstTokenMs
			firstTokenCount++
		}
		if audit.LatencyMs > 0 {
			latencySum += audit.LatencyMs
			latencyCount++
		}
		modelCounts[audit.Model]++
		modelTokens[audit.Model] += audit.PromptTokens + audit.CompletionTokens

		key := bucketKey(audit.CreatedAt.In(location), days)
		bucketRequests[key]++
		bucketTokens[key] += audit.PromptTokens + audit.CompletionTokens
		if audit.Status >= 400 {
			bucketFailures[key]++
		}
	}

	successRate := 0.0
	if requests > 0 {
		successRate = float64(requests-failures) / float64(requests) * 100
	}
	averageFirstToken := int64(0)
	if firstTokenCount > 0 {
		averageFirstToken = firstTokenSum / int64(firstTokenCount)
	}
	averageLatency := int64(0)
	if latencyCount > 0 {
		averageLatency = latencySum / int64(latencyCount)
	}

	type modelRow struct {
		Model    string `json:"model"`
		Requests int    `json:"requests"`
		Tokens   int    `json:"tokens"`
	}
	topModels := make([]modelRow, 0, len(modelCounts))
	for model, count := range modelCounts {
		topModels = append(topModels, modelRow{Model: model, Requests: count, Tokens: modelTokens[model]})
	}
	sort.Slice(topModels, func(i, j int) bool { return topModels[i].Requests > topModels[j].Requests })
	if len(topModels) > 8 {
		topModels = topModels[:8]
	}

	type trendRow struct {
		Bucket   string `json:"bucket"`
		Requests int    `json:"requests"`
		Failures int    `json:"failures"`
		Tokens   int    `json:"tokens"`
	}
	trend := make([]trendRow, 0, len(bucketRequests))
	for key, count := range bucketRequests {
		trend = append(trend, trendRow{Bucket: key, Requests: count, Failures: bucketFailures[key], Tokens: bucketTokens[key]})
	}
	sort.Slice(trend, func(i, j int) bool { return trend[i].Bucket < trend[j].Bucket })

	// routable counts accounts the scheduler can actually use right now. It is
	// not the same as active: an account whose cooldown has already elapsed is
	// still labelled cooldown but is perfectly schedulable, and reporting only
	// the status count made a healthy pool look dead.
	_, active, cooldown, disabled, invalid, routable := a.pool.Summary()
	enabledModels := 0
	for _, model := range models {
		if model.Enabled {
			enabledModels++
		}
	}
	activeKeys := 0
	for _, key := range keys {
		if key.Enabled {
			activeKeys++
		}
	}

	type activityRow struct {
		ID        string    `json:"id"`
		Time      time.Time `json:"time"`
		Model     string    `json:"model"`
		Status    int       `json:"status"`
		LatencyMs int64     `json:"latencyMs"`
		Account   string    `json:"account"`
	}
	activity := make([]activityRow, 0, 8)
	for _, audit := range audits {
		if len(activity) == 8 {
			break
		}
		activity = append(activity, activityRow{
			ID: audit.ID, Time: audit.CreatedAt, Model: audit.Model,
			Status: audit.Status, LatencyMs: audit.LatencyMs, Account: audit.AccountName,
		})
	}

	type distributionRow struct {
		Type  string `json:"type"`
		Count int    `json:"count"`
	}
	kindCounts := map[string]int{}
	for _, account := range accounts {
		kind := account.Kind
		if kind == "" {
			kind = store.KindToken
		}
		kindCounts[kind]++
	}
	distribution := make([]distributionRow, 0, len(kindCounts))
	for kind, count := range kindCounts {
		label := "Token"
		if kind == store.KindGuest {
			label = "游客"
		}
		distribution = append(distribution, distributionRow{Type: label, Count: count})
	}
	sort.Slice(distribution, func(i, j int) bool { return distribution[i].Count > distribution[j].Count })

	writeJSON(w, http.StatusOK, map[string]any{
		"usage": map[string]any{
			"requests":                     requests,
			"failedRequests":               failures,
			"successRate":                  successRate,
			"inputTokens":                  promptTokens,
			"outputTokens":                 completionTokens,
			"reasoningTokens":              reasoningTokens,
			"tokens":                       promptTokens + completionTokens,
			"averageFirstTokenMs":          averageFirstToken,
			"firstTokenSamples":            firstTokenCount,
			"averageLatencyMs":             averageLatency,
			"averageOutputTokensPerSecond": 0,
			"throughputSamples":            0,
			"estimatedCostUsd":             0,
		},
		"resources": map[string]any{
			"totalAccounts":    len(accounts),
			"activeAccounts":   active,
			"routableAccounts": routable,
			"cooldownAccounts": cooldown,
			"disabledAccounts": disabled,
			"invalidAccounts":  invalid,
			"totalModels":      len(models),
			"enabledModels":    enabledModels,
			"totalClientKeys":  len(keys),
			"activeClientKeys": activeKeys,
		},
		"trend":        trend,
		"topModels":    topModels,
		"distribution": distribution,
		"activity":     activity,
		"upstream": map[string]any{
			"baseURL":   settings.Upstream.BaseURL,
			"poolTotal": len(accounts),
			// Kept in sync with /health, which has always reported the routable
			// count under the same name.
			"poolAvailable":    routable,
			"averageLatencyMs": averageLatency,
		},
	})
}

func parsePeriodDays(value string) int {
	value = strings.TrimSuffix(value, "d")
	days, err := strconv.Atoi(value)
	if err != nil || days <= 0 {
		return 30
	}
	if days > 365 {
		return 365
	}
	return days
}

func bucketKey(at time.Time, days int) string {
	if days <= 2 {
		return at.Format("2006-01-02T15:00")
	}
	return at.Format("2006-01-02")
}

// ----------------------------------------------------------------- accounts

func (a *API) listAccounts(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	page := maxInt(1, config.ParseIntOrDefault(query.Get("page"), 1))
	pageSize := clampInt(config.ParseIntOrDefault(query.Get("pageSize"), 20), 1, 500)
	search := strings.ToLower(strings.TrimSpace(query.Get("search")))
	status := query.Get("status")
	kind := query.Get("kind")
	group := query.Get("group")
	sortBy := query.Get("sortBy")
	sortOrder := query.Get("sortOrder")

	inflight := a.pool.Snapshot()
	accounts := a.store.ListAccounts()

	views := make([]store.AccountView, 0, len(accounts))
	for _, account := range accounts {
		if status != "" && account.Status != status {
			continue
		}
		if kind != "" && account.Kind != kind {
			continue
		}
		if group != "" && account.Group != group {
			continue
		}
		if search != "" {
			haystack := strings.ToLower(account.Name + " " + account.Remark + " " + account.Group + " " +
				account.Identifier + " " + account.UserID + " " + account.Region)
			if !strings.Contains(haystack, search) {
				continue
			}
		}
		views = append(views, store.NewAccountView(account, inflight[account.ID]))
	}

	sortViews(views, sortBy, sortOrder)

	total := len(views)
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}

	_, active, cooldown, disabled, invalid, routable := a.pool.Summary()
	writeJSON(w, http.StatusOK, map[string]any{
		"items":    views[start:end],
		"total":    total,
		"page":     page,
		"pageSize": pageSize,
		"summary": map[string]any{
			"total": total, "active": active, "cooldown": cooldown,
			"disabled": disabled, "invalid": invalid, "routable": routable,
		},
	})
}

func sortViews(views []store.AccountView, field, order string) {
	if field == "" {
		return
	}
	less := func(i, j int) bool {
		left, right := views[i], views[j]
		switch field {
		case "name":
			return strings.ToLower(left.Name) < strings.ToLower(right.Name)
		case "status":
			return left.Status < right.Status
		case "priority":
			return left.Priority < right.Priority
		case "lastUsedAt":
			return left.LastUsedAt.Before(right.LastUsedAt)
		default:
			return left.CreatedAt.Before(right.CreatedAt)
		}
	}
	sort.SliceStable(views, func(i, j int) bool {
		if order == "asc" {
			return less(i, j)
		}
		return less(j, i)
	})
}

func (a *API) accountGroups(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"groups": a.store.AccountGroups()})
}

type accountPayload struct {
	Name          string `json:"name"`
	Token         string `json:"token"`
	Region        string `json:"region"`
	UserID        string `json:"userId"`
	Identifier    string `json:"identifier"`
	AgentID       string `json:"agentID"`
	DeviceID      string `json:"deviceID"`
	UUID          string `json:"uuid"`
	ScreenWidth   int    `json:"screenWidth"`
	ScreenHeight  int    `json:"screenHeight"`
	BaseURL       string `json:"baseURL"`
	Group         string `json:"group"`
	Remark        string `json:"remark"`
	Priority      int    `json:"priority"`
	MaxConcurrent int    `json:"maxConcurrent"`
	Enabled       *bool  `json:"enabled"`
	Kind          string `json:"kind"`
}

func (a *API) createAccount(w http.ResponseWriter, r *http.Request) {
	var payload accountPayload
	if err := decode(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}

	account, err := buildAccount(payload)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.store.AddAccount(account); err != nil {
		if err == store.ErrAlreadyExists {
			writeError(w, http.StatusConflict, "该 Token 已存在于号池中")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// The probe talks to the upstream and can take a while, so the account is
	// returned immediately and the probe continues in background.
	go a.syncQuotaDetached(account.ID)
	writeJSON(w, http.StatusOK, map[string]any{"account": a.view(account.ID)})
}

// buildAccount validates an account payload and fills in derived fields.
//
// The browser fingerprint is generated when the operator does not supply one.
// The upstream signature is computed over whatever fingerprint the request
// carries, so any self-consistent pair works; demanding a captured one would
// make adding an account needlessly painful, while an operator replaying a real
// browser session can still paste the original values.
func buildAccount(payload accountPayload) (*store.Account, error) {
	token := strings.TrimSpace(payload.Token)
	if token == "" {
		return nil, errors.New("Token 不能为空")
	}
	if strings.ContainsAny(token, " \t\r\n") {
		return nil, errors.New("Token 中不能包含空白字符，请只粘贴令牌本身")
	}
	if len(token) < 16 {
		return nil, errors.New("Token 长度异常，请确认粘贴的是完整令牌")
	}

	region := strings.TrimSpace(payload.Region)
	if isAutoRegion(region) {
		region = ""
	}
	userID := strings.TrimSpace(payload.UserID)
	identifier := strings.TrimSpace(payload.Identifier)
	// Decoding the token is best-effort: an unrecognised shape is still stored,
	// it just arrives without a friendly label.
	if info, err := minimax.ParseToken(token); err == nil {
		if userID == "" {
			userID = info.UserID
		}
		if identifier == "" {
			identifier = info.Identifier()
		}
		if region == "" {
			region = info.Region
		}
	}
	if region == "" {
		region = store.RegionGlobal
	}
	if region != store.RegionCN && region != store.RegionGlobal {
		return nil, fmt.Errorf("区域只能是 %s（国内）或 %s（国际）", store.RegionCN, store.RegionGlobal)
	}

	uuid := strings.TrimSpace(payload.UUID)
	if uuid == "" {
		uuid = newUUID()
	}
	deviceID := strings.TrimSpace(payload.DeviceID)
	if deviceID == "" {
		deviceID = newDeviceID()
	}

	enabled := true
	if payload.Enabled != nil {
		enabled = *payload.Enabled
	}
	name := strings.TrimSpace(payload.Name)
	if name == "" {
		name = defaultAccountName(identifier, region)
	}

	return &store.Account{
		Name: name, Kind: firstNonEmpty(payload.Kind, store.KindToken),
		Region: region, Token: token, UserID: userID, Identifier: identifier,
		AgentID:  strings.TrimSpace(payload.AgentID),
		DeviceID: deviceID, UUID: uuid,
		ScreenWidth: payload.ScreenWidth, ScreenHeight: payload.ScreenHeight,
		BaseURL:       strings.TrimSpace(payload.BaseURL),
		Group:         strings.TrimSpace(payload.Group),
		Remark:        strings.TrimSpace(payload.Remark),
		Enabled:       enabled,
		Priority:      clampInt(firstNonZero(payload.Priority, 50), 1, 100),
		MaxConcurrent: clampInt(firstNonZero(payload.MaxConcurrent, 2), 1, 256),
	}, nil
}

// newUUID produces the value the site keeps in `localStorage.UNIQUE_USER_ID`.
//
// The site generates a dashed UUID there, and while the upstream does not appear
// to validate its shape, matching the real one costs nothing and keeps a
// generated fingerprint indistinguishable from a captured one.
func newUUID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return fallbackDeviceID()
	}
	// Version 4 and the RFC 4122 variant, so the value parses as a UUID anywhere
	// it is fed back into a UUID parser.
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])
}

// newDeviceID produces the value the site keeps in `sessionStorage.tab_device_id`.
//
// It has to be **all digits**, and that is not cosmetic. The upstream parses it
// as a number and answers
//
//	400 {"error":"internal error","errorCode":50001,"base_resp":{"status_code":1406011050}}
//
// for anything else — including a 32-character hex string, which is what this
// used to generate for both fields. The error names nothing, so it reads as an
// upstream outage rather than a malformed field, and it only affects the
// `/minimax-cloud/…` family: `/v1/api/user/info` and the check-in endpoints
// accept a hex value, which is why check-in worked while every agent-side call
// failed.
//
// The bundle falls back to `1e7 + rand(9e7)` when sessionStorage is empty, so
// eight digits is the shape to match. Any digit string is accepted — a measured
// 4-digit, 8-digit and 12-digit value all answered 200 — but the site's own
// fallback is the least surprising choice.
func newDeviceID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return fallbackDeviceID()
	}
	// 10000000..99999999, i.e. always eight digits with no leading zero.
	span := uint64(0)
	for _, b := range buf {
		span = span<<8 | uint64(b)
	}
	return strconv.FormatUint(10_000_000+span%90_000_000, 10)
}

// fallbackDeviceID is the last resort when the system CSPRNG fails. It is still
// digits, because a non-numeric value is rejected outright.
func fallbackDeviceID() string {
	return strconv.FormatInt(10_000_000+time.Now().UnixNano()%90_000_000, 10)
}

// defaultAccountName labels an account by what it is registered with, which is
// what an operator actually recognises in the pool list.
func defaultAccountName(identifier, region string) string {
	prefix := "global"
	if region == store.RegionCN {
		prefix = "cn"
	}
	if identifier != "" {
		return prefix + "-" + identifier
	}
	return prefix + "-" + time.Now().Format("0102-150405")
}

func (a *API) updateAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var payload accountPayload
	if err := decode(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	tokenReplaced := false
	account, err := a.store.UpdateAccount(id, func(account *store.Account) {
		if strings.TrimSpace(payload.Name) != "" {
			account.Name = strings.TrimSpace(payload.Name)
		}
		if token := strings.TrimSpace(payload.Token); token != "" && token != account.Token {
			tokenReplaced = true
			account.Token = token
			account.Status = store.StatusActive
			account.FailCount = 0
			account.LastError = ""
			if info, err := minimax.ParseToken(token); err == nil {
				if info.UserID != "" {
					account.UserID = info.UserID
				}
				if label := info.Identifier(); label != "" {
					account.Identifier = label
				}
			}
		}
		if raw := strings.TrimSpace(payload.Region); raw != "" {
			switch {
			case isAutoRegion(raw):
				// "let the token decide" — re-infer, which is what an operator
				// wants right after pasting a token taken from the other site.
				if inferred := regionFromToken(account.Token); inferred != "" {
					account.Region = inferred
				}
			case normalizeRegion(raw) != "":
				account.Region = normalizeRegion(raw)
			default:
				account.Region = raw
			}
		} else if tokenReplaced {
			// A replacement token brings its own deployment. Keeping the region
			// that belonged to the old token would send requests to a host that
			// rejects it, which fails with an unhelpful 401.
			if inferred := regionFromToken(account.Token); inferred != "" {
				account.Region = inferred
			}
		}
		if userID := strings.TrimSpace(payload.UserID); userID != "" {
			account.UserID = userID
		}
		if agentID := strings.TrimSpace(payload.AgentID); agentID != "" {
			account.AgentID = agentID
		}
		if uuid := strings.TrimSpace(payload.UUID); uuid != "" {
			account.UUID = uuid
		}
		if deviceID := strings.TrimSpace(payload.DeviceID); deviceID != "" {
			account.DeviceID = deviceID
		}
		if payload.ScreenWidth > 0 {
			account.ScreenWidth = payload.ScreenWidth
		}
		if payload.ScreenHeight > 0 {
			account.ScreenHeight = payload.ScreenHeight
		}
		// BaseURL, group and remark are cleared when omitted, because for these
		// three "empty" is a meaningful value the operator may want to set.
		account.BaseURL = strings.TrimSpace(payload.BaseURL)
		account.Group = strings.TrimSpace(payload.Group)
		account.Remark = strings.TrimSpace(payload.Remark)
		if payload.Priority > 0 {
			account.Priority = clampInt(payload.Priority, 1, 100)
		}
		if payload.MaxConcurrent > 0 {
			account.MaxConcurrent = clampInt(payload.MaxConcurrent, 1, 256)
		}
		if payload.Enabled != nil {
			account.Enabled = *payload.Enabled
		}
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "账号不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": a.view(account.ID)})
}

func (a *API) deleteAccount(w http.ResponseWriter, r *http.Request) {
	if err := a.store.DeleteAccount(r.PathValue("id")); err != nil {
		writeError(w, http.StatusNotFound, "账号不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *API) batchAccounts(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Action        string   `json:"action"`
		IDs           []string `json:"ids"`
		MaxConcurrent int      `json:"maxConcurrent"`
	}
	if err := decode(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if len(payload.IDs) == 0 {
		writeError(w, http.StatusBadRequest, "未选择账号")
		return
	}

	switch payload.Action {
	case "delete":
		deleted, err := a.store.DeleteAccounts(payload.IDs)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted})
	case "enable", "disable":
		enabled := payload.Action == "enable"
		updated, err := a.store.UpdateAccounts(payload.IDs, func(account *store.Account) {
			account.Enabled = enabled
			if !enabled {
				account.Status = store.StatusDisabled
			} else if account.Status == store.StatusDisabled {
				account.Status = store.StatusActive
			}
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"updated": updated})
	case "clearCooldown":
		updated, err := a.store.UpdateAccounts(payload.IDs, func(account *store.Account) {
			account.Status = store.StatusActive
			account.CooldownUntil = time.Time{}
			account.FailCount = 0
			account.LastError = ""
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"updated": updated})
	case "concurrency":
		value := clampInt(payload.MaxConcurrent, 1, 256)
		updated, err := a.store.UpdateAccounts(payload.IDs, func(account *store.Account) {
			account.MaxConcurrent = value
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"updated": updated})
	case "quota":
		succeeded, failed := 0, 0
		for _, id := range payload.IDs {
			if err := a.syncQuota(r.Context(), id); err != nil {
				failed++
				continue
			}
			succeeded++
		}
		writeJSON(w, http.StatusOK, map[string]any{"succeeded": succeeded, "failed": failed})
	default:
		writeError(w, http.StatusBadRequest, "不支持的操作")
	}
}

func (a *API) cleanupAccounts(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Statuses []string `json:"statuses"`
	}
	if err := decode(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if len(payload.Statuses) == 0 {
		writeError(w, http.StatusBadRequest, "请至少选择一种状态")
		return
	}
	deleted, err := a.store.DeleteAccountsByStatus(payload.Statuses)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted})
}

func (a *API) importAccounts(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Tokens string          `json:"tokens"`
		Region string          `json:"region"`
		JSON   json.RawMessage `json:"json"`
	}
	if err := decode(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}

	entries := parseImport(payload.Tokens, payload.JSON)
	if len(entries) == 0 {
		writeError(w, http.StatusBadRequest, "没有解析到可导入的账号")
		return
	}

	created, updated, failed := 0, 0, 0
	failures := make([]string, 0, 4)
	record := func(token string, err error) {
		failed++
		if len(failures) < 4 {
			failures = append(failures, describeToken(token)+": "+err.Error())
		}
	}

	for _, entry := range entries {
		account, err := buildAccount(accountPayload{
			Name: entry.Name, Token: entry.Token,
			Region: firstNonEmpty(entry.Region, payload.Region),
			UserID: entry.UserID, Identifier: entry.Identifier, AgentID: entry.AgentID,
			DeviceID: entry.DeviceID, UUID: entry.UUID,
			ScreenWidth: entry.ScreenWidth, ScreenHeight: entry.ScreenHeight,
			BaseURL: entry.BaseURL, Group: entry.Group, Remark: entry.Remark,
			Priority: entry.Priority, MaxConcurrent: entry.MaxConcurrent,
		})
		if err != nil {
			record(entry.Token, err)
			continue
		}

		if existing := a.findByToken(account.Token); existing != nil {
			// Re-importing is the documented way to refresh an expired token,
			// so an existing entry is updated rather than rejected.
			_, err := a.store.UpdateAccount(existing.ID, func(target *store.Account) {
				target.Token = account.Token
				target.Region = account.Region
				if entry.Name != "" {
					target.Name = entry.Name
				}
				if account.UserID != "" {
					target.UserID = account.UserID
				}
				if account.Identifier != "" {
					target.Identifier = account.Identifier
				}
				target.Status = store.StatusActive
				target.FailCount = 0
				target.LastError = ""
			})
			if err != nil {
				record(entry.Token, err)
				continue
			}
			updated++
			continue
		}

		if err := a.store.AddAccount(account); err != nil {
			record(entry.Token, err)
			continue
		}
		created++
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"created": created, "updated": updated, "failed": failed, "errors": failures,
	})
}

// describeToken renders enough of a token to identify it in an error message
// without echoing the credential back into the console.
func describeToken(token string) string {
	token = strings.TrimSpace(token)
	if len(token) <= 12 {
		return "该账号"
	}
	return token[:6] + "…" + token[len(token)-4:]
}

type importEntry struct {
	Name          string `json:"name"`
	Token         string `json:"token"`
	Region        string `json:"region"`
	UserID        string `json:"userId"`
	Identifier    string `json:"identifier"`
	AgentID       string `json:"agentID"`
	DeviceID      string `json:"deviceID"`
	UUID          string `json:"uuid"`
	ScreenWidth   int    `json:"screenWidth"`
	ScreenHeight  int    `json:"screenHeight"`
	BaseURL       string `json:"baseURL"`
	Group         string `json:"group"`
	Remark        string `json:"remark"`
	Priority      int    `json:"priority"`
	MaxConcurrent int    `json:"maxConcurrent"`
}

// parseImport accepts the shapes an operator is likely to paste.
//
// Recognised line formats, in the order they are tried:
//
//	<token>
//	<name>----<token>
//	<name>----<token>----<region>
//	cn:<token> | global:<token>
//
// A JSON array (or {"accounts": [...]}) takes precedence when supplied, which
// is what the export endpoint emits and what makes a round trip lossless.
func parseImport(rawTokens string, rawJSON json.RawMessage) []importEntry {
	if len(rawJSON) > 0 {
		var list []importEntry
		if err := json.Unmarshal(rawJSON, &list); err == nil && len(list) > 0 {
			return list
		}
		var wrapped struct {
			Accounts []importEntry `json:"accounts"`
		}
		if err := json.Unmarshal(rawJSON, &wrapped); err == nil && len(wrapped.Accounts) > 0 {
			return wrapped.Accounts
		}
	}

	entries := make([]importEntry, 0, 16)
	for _, line := range strings.Split(rawTokens, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if index := strings.Index(line, "----"); index > 0 {
			entry := importEntry{Name: strings.TrimSpace(line[:index])}
			rest := strings.TrimSpace(line[index+4:])
			if second := strings.Index(rest, "----"); second > 0 {
				entry.Token = strings.TrimSpace(rest[:second])
				entry.Region = normalizeRegion(strings.TrimSpace(rest[second+4:]))
			} else {
				entry.Token = rest
			}
			entries = append(entries, entry)
			continue
		}

		if region, token, ok := splitRegionPrefix(line); ok {
			entries = append(entries, importEntry{Token: token, Region: region})
			continue
		}

		entries = append(entries, importEntry{Token: line})
	}
	return entries
}

// splitRegionPrefix handles the "cn:<token>" shorthand.
func splitRegionPrefix(line string) (string, string, bool) {
	index := strings.Index(line, ":")
	if index <= 0 || index > 7 {
		return "", "", false
	}
	region := normalizeRegion(line[:index])
	if region == "" {
		return "", "", false
	}
	token := strings.TrimSpace(line[index+1:])
	if token == "" {
		return "", "", false
	}
	return region, token, true
}

func normalizeRegion(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "cn", "china", "mainland", "国内", "国服":
		return store.RegionCN
	case "global", "intl", "international", "overseas", "国际", "海外":
		return store.RegionGlobal
	}
	return ""
}

// isAutoRegion reports whether the caller asked for region inference instead of
// naming a region. The console sends this sentinel because "auto" and "not
// supplied" are different intents: the first must overwrite an existing region,
// the second must leave it alone.
func isAutoRegion(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "auto", "infer", "detect", "自动", "自动识别":
		return true
	}
	return false
}

// regionFromToken infers the deployment a token belongs to from its phone
// country code, returning "" when the token carries no usable hint.
func regionFromToken(token string) string {
	info, err := minimax.ParseToken(strings.TrimSpace(token))
	if err != nil {
		return ""
	}
	return info.Region
}

func (a *API) exportAccounts(w http.ResponseWriter, r *http.Request) {
	limit := clampInt(config.ParseIntOrDefault(r.URL.Query().Get("limit"), 10000), 1, 10000)
	accounts := a.store.ListAccounts()
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].CreatedAt.After(accounts[j].CreatedAt) })
	if len(accounts) > limit {
		accounts = accounts[:limit]
	}
	out := make([]map[string]any, 0, len(accounts))
	for _, account := range accounts {
		// The export is the import format: everything needed to rebuild the
		// account, including the fingerprint, so a round trip through a file
		// preserves the exact identity the token was issued to.
		out = append(out, map[string]any{
			"name": account.Name, "token": account.Token, "region": account.Region,
			"userId": account.UserID, "identifier": account.Identifier,
			"agentID": account.AgentID, "deviceID": account.DeviceID, "uuid": account.UUID,
			"screenWidth": account.ScreenWidth, "screenHeight": account.ScreenHeight,
			"baseURL": account.BaseURL, "group": account.Group, "remark": account.Remark,
			"priority": account.Priority, "maxConcurrent": account.MaxConcurrent,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": out, "count": len(out)})
}

func (a *API) probeAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := a.store.AccountByID(id); !ok {
		writeError(w, http.StatusNotFound, "账号不存在")
		return
	}
	probeCtx, cancel := context.WithTimeout(r.Context(), probeTimeout(a.settings()))
	defer cancel()
	// The probe is the console's remedy for a half-configured account, so it has
	// to prepare one first — otherwise the button that should fix an account is
	// the one that reports it broken.
	a.prepareAccount(probeCtx, id)
	account, ok := a.store.AccountByID(id)
	if !ok {
		writeError(w, http.StatusNotFound, "账号不存在")
		return
	}
	started := time.Now()
	_, err := a.client.Probe(probeCtx, gateway.CredentialOf(account))
	latency := time.Since(started).Milliseconds()
	message := "OK"
	if err != nil {
		message = err.Error()
	}
	a.store.SaveAccountState(id, func(account *store.Account) {
		account.Quota = &store.Quota{
			SyncedAt: time.Now(), Available: err == nil, LatencyMs: latency,
			Plan: quotaPlan(account), Note: message,
		}
		if err != nil {
			if err == minimax.ErrInvalidCredential {
				account.Status = store.StatusInvalid
			} else if account.Status != store.StatusDisabled {
				account.Status = store.StatusCooldown
				account.CooldownUntil = time.Now().Add(a.settings().CooldownBase())
			}
			account.LastError = truncate(message, 200)
			return
		}
		account.Status = store.StatusActive
		account.CooldownUntil = time.Time{}
		account.LastError = ""
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": err == nil, "latencyMs": latency, "message": message, "account": a.view(id),
	})
}

func (a *API) probeAll(w http.ResponseWriter, r *http.Request) {
	accounts := a.store.ListAccounts()
	healthy, unhealthy := 0, 0
	for _, account := range accounts {
		if !account.Enabled {
			continue
		}
		ctx, cancel := context.WithTimeout(r.Context(), probeTimeout(a.settings()))
		a.prepareAccount(ctx, account.ID)
		// Re-read: preparation may have just supplied the user id and the agent
		// id, and this loop's copy predates that.
		current, ok := a.store.AccountByID(account.ID)
		if !ok {
			cancel()
			continue
		}
		_, err := a.client.Probe(ctx, gateway.CredentialOf(current))
		cancel()
		if err != nil {
			unhealthy++
			a.store.SaveAccountState(account.ID, func(target *store.Account) {
				target.LastError = truncate(err.Error(), 200)
				if err == minimax.ErrInvalidCredential {
					target.Status = store.StatusInvalid
				} else if target.Status != store.StatusDisabled {
					target.Status = store.StatusCooldown
					target.CooldownUntil = time.Now().Add(a.settings().CooldownBase())
				}
			})
			continue
		}
		healthy++
		a.store.SaveAccountState(account.ID, func(target *store.Account) {
			target.Status = store.StatusActive
			target.CooldownUntil = time.Time{}
			target.LastError = ""
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"healthy": healthy, "unhealthy": unhealthy})
}

func (a *API) refreshQuota(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// Prepare first, for the same reason the probe does: refreshing an account
	// that was never prepared would just report it as broken.
	a.prepareAccount(r.Context(), id)
	if err := a.syncQuota(r.Context(), id); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": a.view(id)})
}

func (a *API) quotaAll(w http.ResponseWriter, r *http.Request) {
	accounts := a.store.ListAccounts()
	succeeded, failed := 0, 0
	for _, account := range accounts {
		if !account.Enabled {
			continue
		}
		a.prepareAccount(r.Context(), account.ID)
		if err := a.syncQuota(r.Context(), account.ID); err != nil {
			failed++
			continue
		}
		succeeded++
	}
	writeJSON(w, http.StatusOK, map[string]any{"succeeded": succeeded, "failed": failed})
}

// syncQuotaDetached runs a quota probe outside the request lifecycle with a
// bounded timeout, so a slow upstream never blocks the console.
func (a *API) syncQuotaDetached(id string) {
	timeout := a.settings().RequestTimeout()
	if timeout > 90*time.Second {
		timeout = 90 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	// Preparation first. Every signed call needs the realUserID in its query,
	// the probe below is such a call, and it cannot open a session until the
	// agent id is known — running it first would spend requests on calls that
	// are guaranteed to fail.
	a.prepareAccount(ctx, id)
	_ = a.syncQuota(ctx, id)
}

// prepareAccount fills in everything an account needs before it can hold a
// conversation, and runs the agent-side opening sequence.
//
// Three things happen here, in this order:
//
//  1. realUserID. Every signed call needs it in its query and answers a bare
//     401 without it — indistinguishable from an expired token, so an account
//     would be retired as invalid while being perfectly healthy. It is not in
//     the token and cannot be derived from it: the JWT carries a *different* id
//     under user.id, and sending that one is rejected exactly like sending
//     nothing.
//  2. `/config`, the agent-side initialisation. A freshly registered account
//     answers messages with "Environment Variables not configured" until the
//     web client has opened the agent page once, and that message mentions
//     none of this.
//  3. The agent id. The upstream's agent *roles* are names (`general`) but the
//     id in every URL is a number, and the role name is accepted with a 200
//     that opens no session — so guessing it yields a failure shaped like a
//     success.
//
// Best effort by design: an operator who pasted a prepared account already has
// these values, and a failure here (no proxy, unreachable upstream) should
// surface as a probe error rather than block the account from being created.
func (a *API) prepareAccount(ctx context.Context, id string) {
	account, ok := a.store.AccountByID(id)
	if !ok {
		return
	}
	cred := gateway.CredentialOf(account)

	if strings.TrimSpace(account.UserID) == "" || account.UserID == "0" {
		info, err := a.client.FetchUserInfo(ctx, cred)
		if err != nil || info == nil || info.RealUserID == "" {
			// Nothing below can succeed without the id: the query would carry
			// user_id=0 and every call would come back 401. Stop here rather
			// than spend requests to collect failures.
			return
		}
		label := strings.TrimSpace(account.Identifier)
		if label == "" {
			label = info.Label()
		}
		if _, err := a.store.UpdateAccounts([]string{id}, func(target *store.Account) {
			target.UserID = info.RealUserID
			target.Identifier = label
		}); err != nil {
			return
		}
		// Re-read: the calls below sign a query built from the account, and the
		// copy in hand still carries the empty user id.
		if refreshed, ok := a.store.AccountByID(id); ok {
			account = refreshed
			cred = gateway.CredentialOf(account)
		}
	}

	// The agent-side opening sequence. Idempotent and cheap, and it is both what
	// lets a brand new account answer a message at all and what has to have
	// happened before any check-in can pay out.
	prepared, _ := a.client.Prepare(ctx, cred)

	if strings.TrimSpace(account.AgentID) == "" {
		if agentID := prepared.AgentID(); agentID != "" {
			_, _ = a.store.UpdateAccounts([]string{id}, func(target *store.Account) {
				target.AgentID = agentID
			})
		}
	}
}

// syncQuota runs a minimal upstream call and stores the observed health.
func (a *API) syncQuota(ctx context.Context, id string) error {
	account, ok := a.store.AccountByID(id)
	if !ok {
		return fmt.Errorf("账号不存在")
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.settings().RequestTimeout())
		defer cancel()
	}

	started := time.Now()
	result, err := a.client.Completion(ctx, minimax.Options{
		Credential: gateway.CredentialOf(account),
		Text:       "ping",
		Timeout:    a.settings().RequestTimeout(),
	})
	latency := time.Since(started).Milliseconds()

	note := "上游连通"
	available := err == nil
	if err != nil {
		note = truncate(err.Error(), 200)
	} else if result.Text == "" {
		note = "上游连通，未返回内容"
	}

	a.store.SaveAccountState(id, func(target *store.Account) {
		target.Quota = &store.Quota{
			SyncedAt: time.Now(), Available: available, LatencyMs: latency,
			Plan: quotaPlan(target), Note: note,
		}
		if err != nil {
			if err == minimax.ErrInvalidCredential {
				target.Status = store.StatusInvalid
			} else if target.Status != store.StatusDisabled {
				target.Status = store.StatusCooldown
				target.CooldownUntil = time.Now().Add(a.settings().CooldownBase())
			}
			target.LastError = truncate(note, 200)
			return
		}
		target.Status = store.StatusActive
		target.CooldownUntil = time.Time{}
		target.LastError = ""
	})
	return err
}

// probeTimeout bounds a connectivity probe so the console stays responsive.
func probeTimeout(settings config.Settings) time.Duration {
	timeout := settings.RequestTimeout()
	if timeout > 90*time.Second {
		timeout = 90 * time.Second
	}
	return timeout
}

func quotaPlan(account *store.Account) string {
	if account.Quota != nil && account.Quota.Plan != "" {
		return account.Quota.Plan
	}
	if account.Region == store.RegionCN {
		return "MiniMax Agent（国内）"
	}
	return "MiniMax Agent（国际）"
}

// ------------------------------------------------------------------- signin

// signinOverview reports the scheduler's state and the pool's check-in tally.
//
// Per-account detail is not repeated here: it already travels in the account
// list, and a second copy would be one more thing to keep in step.
func (a *API) signinOverview(w http.ResponseWriter, r *http.Request) {
	settings := a.settings()
	accounts := a.store.ListAccounts()
	now := time.Now()

	done, failed, skipped, exhausted := 0, 0, 0, 0
	var totalPoints int64
	for _, account := range accounts {
		totalPoints += account.SigninTotal
		switch account.SigninStatus {
		case store.SigninOK, store.SigninAlready:
			done++
		case store.SigninFailed:
			failed++
		case store.SigninSkipped:
			skipped++
		}
		if settings.Signin.SkipZeroCredit && account.Credit.Exhausted() &&
			now.Sub(account.Credit.SyncedAt) < settings.CreditFresh() {
			exhausted++
		}
	}

	// A typed nil would serialise as null either way, but declaring it as
	// *signin.Report keeps the "no run yet" case explicit at the call site.
	var last *signin.Report = a.signin.LastReport()

	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":    settings.Signin.Enabled,
		"running":    a.signin.Running(),
		"nextRunAt":  a.signin.NextRun(now),
		"lastRunAt":  a.signin.LastRunAt(),
		"lastReport": last,
		// The console needs this to know whether a zero balance is actually
		// holding the account out of rotation, or merely being displayed.
		"skipZeroCredit": settings.Signin.SkipZeroCredit,
		"summary": map[string]any{
			"total": len(accounts), "done": done, "failed": failed,
			"skipped": skipped, "exhausted": exhausted, "totalPoints": totalPoints,
		},
	})
}

// signinRun triggers a sweep outside the schedule.
func (a *API) signinRun(w http.ResponseWriter, r *http.Request) {
	report := a.signin.Sweep(r.Context())
	if report == nil {
		writeError(w, http.StatusConflict, "已有签到任务在执行，请稍候")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"report":    report,
		"nextRunAt": a.signin.NextRun(time.Now()),
	})
}

// signinAccount checks in a single account.
//
// A skip is not an error: "this is a mainland account" is a complete and
// correct answer, and returning 4xx for it would push the console into showing
// a failure where the honest report is "nothing to do".
func (a *API) signinAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	result, err := a.signin.CheckOne(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": result, "account": a.view(id)})
}

// refreshCredit re-reads one account's balance.
func (a *API) refreshCredit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	credit, err := a.signin.RefreshCredit(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"credit": credit, "account": a.view(id)})
}

func (a *API) view(id string) *store.AccountView {
	inflight := a.pool.Snapshot()
	account, ok := a.store.AccountByID(id)
	if !ok {
		return nil
	}
	view := store.NewAccountView(account, inflight[id])
	return &view
}

func (a *API) findByToken(token string) *store.Account {
	for _, account := range a.store.ListAccounts() {
		if account.Token == token {
			return account
		}
	}
	return nil
}

// -------------------------------------------------------------- client keys

func (a *API) listClientKeys(w http.ResponseWriter, r *http.Request) {
	keys := a.store.ListClientKeys()
	items := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		items = append(items, map[string]any{
			"id": key.ID, "name": key.Name, "key": key.Key, "maskedKey": maskKey(key.Key),
			"enabled": key.Enabled, "rpmLimit": key.RPMLimit, "maxConcurrent": key.MaxConcurrent,
			"totalRequests": key.TotalRequests, "createdAt": key.CreatedAt, "lastUsedAt": key.LastUsedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

func maskKey(key string) string {
	if len(key) <= 12 {
		return key
	}
	return key[:11] + "…" + key[len(key)-4:]
}

func (a *API) createClientKey(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Name          string `json:"name"`
		RPMLimit      int    `json:"rpmLimit"`
		MaxConcurrent int    `json:"maxConcurrent"`
	}
	if err := decode(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if strings.TrimSpace(payload.Name) == "" {
		writeError(w, http.StatusBadRequest, "密钥名称不能为空")
		return
	}
	key, err := a.store.CreateClientKey(strings.TrimSpace(payload.Name), maxInt(0, payload.RPMLimit), clampInt(payload.MaxConcurrent, 1, 256))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": map[string]any{
		"id": key.ID, "name": key.Name, "key": key.Key, "maskedKey": maskKey(key.Key),
		"enabled": key.Enabled, "rpmLimit": key.RPMLimit, "maxConcurrent": key.MaxConcurrent,
		"totalRequests": key.TotalRequests, "createdAt": key.CreatedAt, "lastUsedAt": key.LastUsedAt,
	}})
}

func (a *API) updateClientKey(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Name          string `json:"name"`
		Enabled       *bool  `json:"enabled"`
		RPMLimit      *int   `json:"rpmLimit"`
		MaxConcurrent *int   `json:"maxConcurrent"`
	}
	if err := decode(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	key, err := a.store.UpdateClientKey(r.PathValue("id"), func(key *store.ClientKey) {
		if strings.TrimSpace(payload.Name) != "" {
			key.Name = strings.TrimSpace(payload.Name)
		}
		if payload.Enabled != nil {
			key.Enabled = *payload.Enabled
		}
		if payload.RPMLimit != nil {
			key.RPMLimit = maxInt(0, *payload.RPMLimit)
		}
		if payload.MaxConcurrent != nil {
			key.MaxConcurrent = clampInt(*payload.MaxConcurrent, 1, 256)
		}
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "密钥不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": map[string]any{
		"id": key.ID, "name": key.Name, "key": key.Key, "maskedKey": maskKey(key.Key),
		"enabled": key.Enabled, "rpmLimit": key.RPMLimit, "maxConcurrent": key.MaxConcurrent,
		"totalRequests": key.TotalRequests, "createdAt": key.CreatedAt, "lastUsedAt": key.LastUsedAt,
	}})
}

func (a *API) deleteClientKey(w http.ResponseWriter, r *http.Request) {
	if err := a.store.DeleteClientKey(r.PathValue("id")); err != nil {
		writeError(w, http.StatusNotFound, "密钥不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ------------------------------------------------------------------- models

func (a *API) listModels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"items": a.store.ListModels()})
}

func (a *API) updateModel(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Enabled     *bool  `json:"enabled"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := decode(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	model, err := a.store.UpdateModel(r.PathValue("id"), func(model *store.ModelConfig) {
		if payload.Enabled != nil {
			model.Enabled = *payload.Enabled
		}
		if strings.TrimSpace(payload.Name) != "" {
			model.Name = strings.TrimSpace(payload.Name)
		}
		if strings.TrimSpace(payload.Description) != "" {
			model.Description = strings.TrimSpace(payload.Description)
		}
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "模型不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"model": model})
}

// ------------------------------------------------------------------- audits

func (a *API) listAudits(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	page := maxInt(1, config.ParseIntOrDefault(query.Get("page"), 1))
	pageSize := clampInt(config.ParseIntOrDefault(query.Get("pageSize"), 20), 1, 500)
	search := strings.ToLower(strings.TrimSpace(query.Get("search")))
	status := query.Get("status")

	audits := a.store.ListAudits()
	filtered := make([]*store.Audit, 0, len(audits))
	for _, audit := range audits {
		if status == "success" && audit.Status >= 400 {
			continue
		}
		if status == "failed" && audit.Status < 400 {
			continue
		}
		if search != "" {
			haystack := strings.ToLower(audit.ID + " " + audit.Model + " " + audit.KeyName + " " + audit.AccountName)
			if !strings.Contains(haystack, search) {
				continue
			}
		}
		filtered = append(filtered, audit)
	}

	total := len(filtered)
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := minInt(start+pageSize, total)

	writeJSON(w, http.StatusOK, map[string]any{
		"items": filtered[start:end], "total": total, "page": page, "pageSize": pageSize,
	})
}

func (a *API) auditDetail(w http.ResponseWriter, r *http.Request) {
	audit, ok := a.store.AuditByID(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "记录不存在")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"audit": audit})
}

func (a *API) clearAudits(w http.ResponseWriter, r *http.Request) {
	if err := a.store.ClearAudits(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ----------------------------------------------------------------- settings

func (a *API) getSettings(w http.ResponseWriter, r *http.Request) {
	settings := a.settings()
	writeJSON(w, http.StatusOK, map[string]any{
		"server": map[string]any{
			"addr": settings.Server.Addr, "maxConcurrentRequests": settings.Server.MaxConcurrentRequests,
			"adminUsername": a.store.AdminProfile().Username,
		},
		"upstream": map[string]any{
			"baseURL": settings.Upstream.BaseURL, "baseURLCN": settings.Upstream.BaseURLCN,
			"agentID":     settings.Upstream.AgentID,
			"sessionPath": settings.Upstream.SessionPath, "messagePath": settings.Upstream.MessagePath,
			"userInfoPath":    settings.Upstream.UserInfoPath,
			"agentListPath":   settings.Upstream.AgentListPath,
			"configPath":      settings.Upstream.ConfigPath,
			"connectionsPath": settings.Upstream.ConnectionsPath,
			"modelPayload":    settings.Upstream.ModelPayload,
			"language":        settings.Upstream.Language,
			"screenWidth":     settings.Upstream.ScreenWidth, "screenHeight": settings.Upstream.ScreenHeight,
			"requestTimeoutSec":    settings.Upstream.RequestTimeoutSec,
			"streamIdleTimeoutSec": settings.Upstream.StreamIdleTimeoutSec,
			"proxy":                settings.Upstream.Proxy, "userAgent": settings.Upstream.UserAgent,
		},
		"routing": settings.Routing,
		"audit":   settings.Audit,
		"media":   settings.Media,
		"signin":  settings.Signin,
		"video":   settings.Video,
		"about": map[string]any{
			"version": gateway.Version, "buildTime": a.started.Format(time.RFC3339),
			"dataDir": a.store.DataDir(), "upstreamURL": settings.Upstream.BaseURL,
		},
	})
}

func (a *API) saveSettings(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Server        *config.ServerSettings   `json:"server"`
		Upstream      *config.UpstreamSettings `json:"upstream"`
		Routing       *config.RoutingSettings  `json:"routing"`
		Audit         *config.AuditSettings    `json:"audit"`
		Media         *config.MediaSettings    `json:"media"`
		Signin        *config.SigninSettings   `json:"signin"`
		Video         *config.VideoSettings    `json:"video"`
		AdminPassword string                   `json:"adminPassword"`
	}
	if err := decode(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}

	if err := a.store.UpdateSettings(func(settings *config.Settings) {
		if payload.Server != nil {
			settings.Server.MaxConcurrentRequests = payload.Server.MaxConcurrentRequests
			if strings.TrimSpace(payload.Server.AdminUsername) != "" {
				settings.Server.AdminUsername = strings.TrimSpace(payload.Server.AdminUsername)
			}
		}
		if payload.Upstream != nil {
			settings.Upstream = *payload.Upstream
		}
		if payload.Routing != nil {
			settings.Routing = *payload.Routing
		}
		if payload.Audit != nil {
			settings.Audit = *payload.Audit
		}
		if payload.Media != nil {
			settings.Media = *payload.Media
		}
		if payload.Signin != nil {
			settings.Signin = *payload.Signin
		}
		if payload.Video != nil {
			settings.Video = *payload.Video
		}
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if payload.AdminPassword != "" {
		admin := a.store.AdminProfile()
		username := admin.Username
		if payload.Server != nil && strings.TrimSpace(payload.Server.AdminUsername) != "" {
			username = strings.TrimSpace(payload.Server.AdminUsername)
		}
		if err := a.store.SetPassword(username, payload.AdminPassword); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	a.getSettings(w, r)
}

// ------------------------------------------------------------------ gallery

func (a *API) gallery(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"items": a.store.ListMedia()})
}

// ------------------------------------------------------------------ helpers

func decode(r *http.Request, target any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return nil
	}
	return json.Unmarshal(body, target)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, "encode failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}

func clampInt(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstNonZero(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}
