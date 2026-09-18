package minimax

import (
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

// The query string is signed verbatim, so a token containing characters that
// need escaping must be encoded the same way the browser encodes it.
func TestClientQueryIsOrderedAndEncoded(t *testing.T) {
	settings := configWithUpstream()
	cred := Credential{
		Token: "a+b/c", UUID: "u-1", DeviceID: "d 2", UserID: "42",
		ScreenWidth: 1920, ScreenHeight: 1080,
	}

	got := clientQuery(settings, cred)
	want := "token=a%2Bb%2Fc&uuid=u-1&device_id=d%202&user_id=42&screen_width=1920&screen_height=1080"
	if got != want {
		t.Fatalf("clientQuery = %q, want %q", got, want)
	}
}

// Empty fields are dropped rather than emitted as "key=", because the signed
// URL has to match the one actually requested.
func TestClientQuerySkipsEmptyFields(t *testing.T) {
	settings := configWithUpstream()
	got := clientQuery(settings, Credential{Token: "t"})
	if got != "token=t&screen_width=1920&screen_height=1080" {
		t.Fatalf("clientQuery = %q", got)
	}
}

// The agent id and session id are substituted without going through net/url,
// so the query order survives byte for byte.
func TestBuildURLSubstitutesPathPlaceholders(t *testing.T) {
	settings := configWithUpstream()
	client := &Client{}
	cred := Credential{Token: "t", UUID: "u", DeviceID: "d"}

	got := client.buildURL(settings, cred, "/archon/api/v1/session/{session_id}/message", "sess-9")
	if !strings.HasPrefix(got, "https://agent.minimax.io/archon/api/v1/session/sess-9/message?") {
		t.Fatalf("buildURL = %q", got)
	}
	if !strings.Contains(got, "token=t") {
		t.Fatalf("buildURL should carry the credential query: %q", got)
	}
}

// Mainland accounts must be routed to the mainland host, which is the whole
// reason the region travels with the account.
func TestBuildURLSelectsHostByRegion(t *testing.T) {
	settings := configWithUpstream()
	client := &Client{}

	global := client.buildURL(settings, Credential{Region: RegionGlobal, Token: "t"}, "/x", "")
	mainland := client.buildURL(settings, Credential{Region: RegionCN, Token: "t"}, "/x", "")
	pinned := client.buildURL(settings, Credential{Region: RegionCN, Token: "t", BaseURL: "https://relay.local"}, "/x", "")

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
