package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"minimax2api/internal/minimax"
	"minimax2api/internal/store"
)

// Timeouts for the local bridge. The start call launches a browser and waits
// for it to reach the site, so it gets a slightly larger budget than the poll
// and cancel calls, which only touch state the bridge already holds.
const (
	browserLoginStartTimeout  = 15 * time.Second
	browserLoginStatusTimeout = 10 * time.Second
)

// browserLoginTargets maps the console's region selector onto the site the
// browser should open. The two deployments share nothing but the product, so
// the target has to match the region the operator picked.
var browserLoginTargets = map[string]string{
	store.RegionCN:     "https://agent.minimaxi.com",
	store.RegionGlobal: "https://agent.minimax.io",
}

type browserLoginStartRequest struct {
	Region string `json:"region"`
	Proxy  string `json:"proxy"`
}

// bridgeLoginSession is the bridge's reply to POST /login.
type bridgeLoginSession struct {
	SessionID string `json:"sessionId"`
	Target    string `json:"target"`
	ChromePID int    `json:"chromePid"`
}

// bridgeLoginStatus is the bridge's reply to GET /login/{id}. Only Status is
// always present; the empty fields are omitted so the pending, timeout and
// error states pass through to the console in their original shape.
type bridgeLoginStatus struct {
	Status string `json:"status"`
	Token  string `json:"token,omitempty"`
	Host   string `json:"host,omitempty"`
	Error  string `json:"error,omitempty"`
}

// startBrowserLogin opens a browser session on the bridge for the requested
// region and returns the session handle the console polls with.
func (a *API) startBrowserLogin(w http.ResponseWriter, r *http.Request) {
	if a.browserLoginBridge == "" {
		writeError(w, http.StatusServiceUnavailable, "浏览器登录服务未配置")
		return
	}
	var request browserLoginStartRequest
	if err := decode(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	region := normalizeRegion(request.Region)
	if region == "" {
		if strings.TrimSpace(request.Region) != "" {
			writeError(w, http.StatusBadRequest, "区域只能是 cn（国内）或 global（国际）")
			return
		}
		region = store.RegionGlobal
	}
	target := browserLoginTargets[region]

	ctx, cancel := context.WithTimeout(r.Context(), browserLoginStartTimeout)
	defer cancel()

	// proxy 优先取项目全局的出口代理；前端传来的值只作覆盖（多数情况应已一致）
	proxy := strings.TrimSpace(a.settings().Upstream.Proxy)
	if proxy == "" {
		proxy = strings.TrimSpace(request.Proxy)
	}

	// The bridge adds --proxy-server to Chrome when a proxy is supplied. An
	// unset proxy keeps the bridge's default behaviour, so only send the key
	// when a proxy is actually in effect.
	loginPayload := map[string]any{"target": target}
	if proxy != "" {
		loginPayload["proxy"] = proxy
	}

	var session bridgeLoginSession
	if err := a.bridgeJSON(ctx, http.MethodPost, "/login", loginPayload, &session); err != nil {
		writeError(w, http.StatusBadGateway, "浏览器登录服务不可用："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sessionId": session.SessionID, "target": session.Target, "region": region,
		"proxy": proxy,
	})
}

// pollBrowserLogin forwards the poll to the bridge and, once the login has
// completed, turns the captured token into a pool account so the operator does
// not have to paste it back in by hand.
func (a *API) pollBrowserLogin(w http.ResponseWriter, r *http.Request) {
	if a.browserLoginBridge == "" {
		writeError(w, http.StatusServiceUnavailable, "浏览器登录服务未配置")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "缺少会话 ID")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), browserLoginStatusTimeout)
	defer cancel()

	var status bridgeLoginStatus
	if err := a.bridgeJSON(ctx, http.MethodGet, "/login/"+url.PathEscape(id), nil, &status); err != nil {
		writeError(w, http.StatusBadGateway, "浏览器登录服务不可用："+err.Error())
		return
	}

	// pending, timeout and error are answers the console renders as-is; only a
	// finished login has anything for this side to do.
	if status.Status != "ok" {
		writeJSON(w, http.StatusOK, status)
		return
	}

	// The token came out of a browser the user was driving, but "a browser has a
	// token" is not proof the login completed: the login page can hold a partial
	// or anonymous token before the account is actually established. Verify it
	// against the upstream, and learn the identity at the same time — the region
	// below is decided from the phone number the upstream reports, not from the
	// site the window happened to open.
	probeCtx, probeCancel := context.WithTimeout(context.Background(), browserLoginStartTimeout)
	defer probeCancel()
	info, verifyErr := a.client.FetchUserInfo(probeCtx, minimax.Credential{Token: status.Token})
	if verifyErr != nil || info == nil || info.RealUserID == "" {
		// Ask the bridge to close the window: the operator has no further use for
		// a session whose token the gateway cannot use.
		_ = a.bridgeJSON(context.Background(), http.MethodDelete, "/login/"+url.PathEscape(id), nil, nil)
		writeError(w, http.StatusBadGateway, "登录令牌无效，无法通过上游校验")
		return
	}

	account, err := buildAccount(accountPayload{
		Token:  status.Token,
		Region: regionFromPhone(info.Phone),
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, "登录成功但令牌无效："+err.Error())
		return
	}
	if err := a.store.AddAccount(account); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "该 Token 已存在于号池中")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// The account is usable as soon as it is stored; the upstream probe can take
	// a while, so it runs in the background like it does for a pasted account.
	go a.syncQuotaDetached(account.ID)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "account": a.view(account.ID)})
}

// cancelBrowserLogin closes an in-flight browser session.
func (a *API) cancelBrowserLogin(w http.ResponseWriter, r *http.Request) {
	if a.browserLoginBridge == "" {
		writeError(w, http.StatusServiceUnavailable, "浏览器登录服务未配置")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "缺少会话 ID")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), browserLoginStatusTimeout)
	defer cancel()

	var result struct {
		Cancelled bool `json:"cancelled"`
	}
	if err := a.bridgeJSON(ctx, http.MethodDelete, "/login/"+url.PathEscape(id), nil, &result); err != nil {
		writeError(w, http.StatusBadGateway, "浏览器登录服务不可用："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cancelled": true})
}

// regionFromPhone infers the deployment from the phone number the upstream
// reported for a verified login. The site the browser window happened to open is
// NOT a reliable signal: the two deployments share a login flow, so a mainland
// account signed in on the international page still returns a mainland token —
// and routing that token at the international host fails the geo-fence exactly
// like an expired one.
func regionFromPhone(phone string) string {
	digits := strings.TrimPrefix(strings.TrimSpace(phone), "+")
	if strings.HasPrefix(digits, "86") {
		return store.RegionCN
	}
	// No phone, or a non-mainland country code: let buildAccount fall back to the
	// token's own hint, then the global default.
	return ""
}

// bridgeJSON forwards one request to the local bridge and decodes its JSON
// reply. A non-2xx reply becomes an error carrying the bridge's own message, so
// the console shows what actually went wrong instead of a bare status code.
func (a *API) bridgeJSON(ctx context.Context, method, path string, payload any, target any) error {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, a.browserLoginBridge+path, body)
	if err != nil {
		return err
	}
	if payload != nil {
		request.Header.Set("content-type", "application/json")
	}
	response, err := a.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("桥返回 %d：%s", response.StatusCode, bridgeMessage(raw))
	}
	if target == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("桥返回了无法解析的响应：%w", err)
	}
	return nil
}

// bridgeMessage extracts the human-readable error from a bridge error body,
// falling back to the raw text when it is not the expected JSON shape.
func bridgeMessage(raw []byte) string {
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &payload); err == nil && strings.TrimSpace(payload.Error) != "" {
		return strings.TrimSpace(payload.Error)
	}
	return truncate(strings.TrimSpace(string(raw)), 200)
}
