package minimax

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"minimax2api/internal/config"
)

// configWithUpstream returns the built-in settings, which already point at the
// two real hosts. Reusing them keeps the tests honest about the defaults.
func configWithUpstream() config.Settings {
	return config.DefaultSettings("/tmp/minimax2api-test")
}

// The salt is public - it ships inside the web bundle - so these vectors exist
// to pin the formula, not to protect a secret.

func TestXSignatureFollowsFormula(t *testing.T) {
	// Fixed inputs so the expected digest is reproducible by hand.
	stamp := time.Unix(1780470413, 0)
	body := `{"content":"你好"}`

	got := XSignature(stamp, body)
	want := md5Hex("1780470413" + signatureSalt + body)
	if got != want {
		t.Fatalf("XSignature = %q, want %q", got, want)
	}
	if len(got) != 32 {
		t.Fatalf("digest length = %d, want 32 hex characters", len(got))
	}
}

func TestXSignatureDependsOnTimestampAndBody(t *testing.T) {
	stamp := time.Unix(1780470413, 0)
	if XSignature(stamp, "a") != XSignature(stamp, "a") {
		t.Fatal("the signature must be deterministic")
	}
	if XSignature(stamp, "a") == XSignature(stamp, "b") {
		t.Fatal("the body must affect the signature")
	}
	if XSignature(stamp, "a") == XSignature(time.Unix(1780470414, 0), "a") {
		t.Fatal("the timestamp must affect the signature")
	}
}

func TestYYFollowsFormula(t *testing.T) {
	// The offset has to be a whole number of milliseconds: UnixMilli truncates,
	// so a 500-nanosecond offset would collapse onto the same instant.
	stamp := time.Unix(1780470413, 500*int64(time.Millisecond))
	url := "https://agent.minimax.io/archon/api/v1/session/s1/message?token=t&uuid=u"
	body := `{"content":"hi"}`

	got := YY(url, body, stamp)
	want := md5Hex(encodeURIComponent(url) + "_" + body + md5Hex("1780470413500") + "ooui")
	if got != want {
		t.Fatalf("YY = %q, want %q", got, want)
	}
}

// yy is the only header that binds the request URL, which is what makes the
// query string order significant.
func TestYYBindsURLAndMilliseconds(t *testing.T) {
	stamp := time.Unix(1780470413, 500*int64(time.Millisecond))
	url := "https://agent.minimax.io/archon/api/v1/session/s1/message?token=t"
	body := `{"content":"hi"}`
	base := YY(url, body, stamp)

	if YY(url+"&uuid=u", body, stamp) == base {
		t.Fatal("the query string must affect the signature")
	}
	if YY(url, body, time.Unix(1780470414, 500*int64(time.Millisecond))) == base {
		t.Fatal("the second must affect the signature")
	}
	if YY(url, body, time.Unix(1780470413, 600*int64(time.Millisecond))) == base {
		t.Fatal("the millisecond must affect the signature")
	}
}

// encodeURIComponent diverges from net/url.QueryEscape on exactly the
// characters below. Any difference silently changes the yy digest, so the
// behaviour is pinned rather than assumed.
func TestEncodeURIComponentMatchesJavaScript(t *testing.T) {
	cases := map[string]string{
		"abcXYZ019":           "abcXYZ019",
		"-_.!~*'()":           "-_.!~*'()",
		"a b":                 "a%20b", // QueryEscape would produce "a+b"
		"a+b":                 "a%2Bb",
		"a=b&c":               "a%3Db%26c",
		"a/b?c#d":             "a%2Fb%3Fc%23d",
		"a~b":                 "a~b", // QueryEscape leaves "~" alone too, but escapes "!*'()"
		"你好":                  "%E4%BD%A0%E5%A5%BD",
		`{}[]|\^` + "`" + `"`: "%7B%7D%5B%5D%7C%5C%5E%60%22",
	}
	for input, want := range cases {
		if got := encodeURIComponent(input); got != want {
			t.Errorf("encodeURIComponent(%q) = %q, want %q", input, got, want)
		}
	}
}

// The query string is signed verbatim, so its *order* is part of the protocol:
// `yy` digests the encoded URL, and the captured web client sends these 22
// parameters in exactly this order — `unix` fifth, `client`/`region` last.
// Reordering them would sign a URL the browser never sends, and the upstream
// would reject it with a bare "invalid signature" that names no cause.
func TestClientQueryMatchesTheCapturedWebClient(t *testing.T) {
	settings := configWithUpstream()
	stamp := time.Unix(1780470413, 250*int64(time.Millisecond))
	cred := Credential{
		Token: "a+b/c", UUID: "u-1", DeviceID: "d 2", UserID: "42",
		ScreenWidth: 1920, ScreenHeight: 1080,
	}

	got := clientQuery(settings, cred, stamp)
	want := strings.Join([]string{
		"device_platform=web", "biz_id=3", "app_id=3001", "version_code=22201",
		"unix=1780470413250",
		"timezone_offset=28800", "sys_language=en", "lang=en",
		"uuid=u-1", "device_id=d%202",
		"os_name=Windows", "browser_name=Chrome", "device_memory=16", "cpu_core_num=8",
		"browser_language=zh-CN", "browser_platform=Win32",
		"user_id=42", "screen_width=1920", "screen_height=1080",
		"token=a%2Bb%2Fc",
		"client=web", "region=en",
	}, "&")
	if got != want {
		t.Fatalf("clientQuery =\n %s\nwant\n %s", got, want)
	}
}

// Empty fields are dropped rather than emitted as "key=", because the signed
// URL has to match the one actually requested. The constants survive an account
// that carries nothing but a token, because the upstream reads them every call.
func TestClientQuerySkipsEmptyFields(t *testing.T) {
	settings := configWithUpstream()
	stamp := time.Unix(1780470413, 250*int64(time.Millisecond))
	got := clientQuery(settings, Credential{Token: "t"}, stamp)

	for _, dropped := range []string{"uuid=", "device_id=", "user_id="} {
		if strings.Contains(got, dropped) {
			t.Errorf("clientQuery should omit the empty %q: %s", dropped, got)
		}
	}
	// The credential fields are the ones that vanish; the constants around them
	// keep their positions, which is what keeps the digest stable.
	if !strings.Contains(got, "&token=t&client=web&region=en") {
		t.Errorf("clientQuery should keep the trailing constants: %s", got)
	}
	for _, want := range []string{
		"device_platform=web", "timezone_offset=28800", "browser_platform=Win32",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("clientQuery is missing the constant %q: %s", want, got)
		}
	}
}

// `unix` and `yy` have to describe the same request. The web client carries the
// millisecond clock in its query and `yy` digests the encoded URL, so a second
// clock reading — even a few milliseconds later — would make the digest cover a
// URL that was never sent. The failure mode is a bare "invalid signature" that
// points at nothing, so the seam is pinned rather than trusted.
func TestUnixAndYYComeFromOneClockReading(t *testing.T) {
	settings := configWithUpstream()
	stamp := time.Unix(1780470413, 250*int64(time.Millisecond))
	client := &Client{now: func() time.Time { return stamp }}
	cred := Credential{Token: "t", UUID: "u", DeviceID: "12345678", UserID: "42"}

	body := []byte(`{}`)
	req, err := client.newRequest(context.Background(), settings, cred, http.MethodPost,
		requestTarget{Path: messagePath(settings), SessionID: "s1", Stream: true}, body)
	if err != nil {
		t.Fatalf("newRequest: %v", err)
	}

	if !strings.Contains(req.URL.RawQuery, "unix=1780470413250") {
		t.Fatalf("request URL should carry the millisecond clock: %q", req.URL.String())
	}
	if got, want := req.Header.Get("x-timestamp"), "1780470413"; got != want {
		t.Errorf("x-timestamp = %q, want %q", got, want)
	}
	// The digest must verify against the URL that is actually sent, not the one
	// that went in — Go parses and re-serialises it on the way through.
	if got, want := req.Header.Get("yy"), YY(req.URL.String(), string(body), stamp); got != want {
		t.Errorf("yy = %q, want %q (over %q)", got, want, req.URL.String())
	}
}

// The conversation endpoint answers on its own host, and the captured web client
// posts turns to `agent-stream.<domain>`. The mapping is derived rather than
// hardcoded so that everything which is *not* that shape keeps its own address —
// a self-hosted relay and a test server would otherwise be pointed at a
// hostname they do not have.
func TestStreamBaseURLFollowsTheCapture(t *testing.T) {
	settings := configWithUpstream()

	cases := []struct {
		name string
		cred Credential
		want string
	}{
		{"international", Credential{Region: RegionGlobal}, "https://agent-stream.minimax.io"},
		{"mainland", Credential{Region: RegionCN}, "https://agent-stream.minimaxi.com"},
		{"relay", Credential{Region: RegionGlobal, BaseURL: "https://relay.local"}, "https://relay.local"},
		{"relay with port", Credential{BaseURL: "http://127.0.0.1:8080"}, "http://127.0.0.1:8080"},
	}
	client := &Client{}
	for _, tc := range cases {
		base := client.baseURL(settings, tc.cred)
		if got := streamBaseURL(settings, base); got != tc.want {
			t.Errorf("%s: streamBaseURL = %q, want %q", tc.name, got, tc.want)
		}
	}

	// An explicit setting wins, which is how a capture that disagrees gets
	// applied without a rebuild.
	pinned := settings
	pinned.Upstream.StreamBaseURL = "https://captured.example"
	cred := Credential{Region: RegionGlobal}
	if got := streamBaseURL(pinned, client.baseURL(pinned, cred)); got != "https://captured.example" {
		t.Errorf("an explicit StreamBaseURL should win, got %q", got)
	}
}

// The agent id and session id are substituted without going through net/url,
// so the query order survives byte for byte.
func TestBuildURLSubstitutesPathPlaceholders(t *testing.T) {
	settings := configWithUpstream()
	stamp := time.Unix(1780470413, 0)
	client := &Client{}
	cred := Credential{Token: "t", UUID: "u", DeviceID: "d"}

	got := client.buildURL(settings, cred, "/minimax-cloud/api/v1/session/{session_id}/message", "sess-9", stamp)
	if !strings.HasPrefix(got, "https://agent.minimax.io/minimax-cloud/api/v1/session/sess-9/message?") {
		t.Fatalf("buildURL = %q", got)
	}
	if !strings.Contains(got, "token=t") {
		t.Fatalf("buildURL should carry the credential query: %q", got)
	}
}

// A path that already carries a query must be joined with "&", not a second
// "?". The two differ by one byte and `yy` covers the encoded URL, so getting it
// wrong rejects the request for a reason that says nothing about the mistake.
func TestBuildURLAppendsToAnExistingQuery(t *testing.T) {
	settings := configWithUpstream()
	client := &Client{}

	got := client.buildURL(settings, Credential{Token: "t"},
		"/minimax-cloud/api/v1/skill?agent_name=1&include_plugin_skills=true", "", time.Unix(1780470413, 0))
	if strings.Contains(got, "?agent_name=1?") {
		t.Fatalf("buildURL joined a second \"?\": %q", got)
	}
	if !strings.Contains(got, "include_plugin_skills=true&device_platform=web") {
		t.Fatalf("buildURL should append with \"&\": %q", got)
	}
}

// Mainland accounts must be routed to the mainland host, which is the whole
// reason the region travels with the account.
func TestBuildURLSelectsHostByRegion(t *testing.T) {
	settings := configWithUpstream()
	stamp := time.Unix(1780470413, 0)
	client := &Client{}

	global := client.buildURL(settings, Credential{Region: RegionGlobal, Token: "t"}, "/x", "", stamp)
	mainland := client.buildURL(settings, Credential{Region: RegionCN, Token: "t"}, "/x", "", stamp)
	pinned := client.buildURL(settings, Credential{Region: RegionCN, Token: "t", BaseURL: "https://relay.local"}, "/x", "", stamp)

	if !strings.HasPrefix(global, "https://agent.minimax.io/") {
		t.Fatalf("global = %q", global)
	}
	if !strings.HasPrefix(mainland, "https://agent.minimaxi.com/") {
		t.Fatalf("mainland = %q", mainland)
	}
	if !strings.HasPrefix(pinned, "https://relay.local/") {
		t.Fatalf("account-level override should win: %q", pinned)
	}
}
