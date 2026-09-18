package minimax

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"minimax2api/internal/config"
)

// fakeToken is a structurally valid JWT whose signature segment is a
// placeholder. Real tokens are deliberately never committed: the check-in query
// string carries the token inside the URL, so a captured vector would embed a
// live credential in a public repository.
const fakeToken = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9." +
	"eyJleHAiOjQxMDI0NDQ4MDAsInVzZXIiOnsiaWQiOiI5OTk5OTk5OTk5OTk5OTk5OTk5In19." +
	"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

// vectorUnixMs pins the timestamp so the digests are reproducible.
const vectorUnixMs int64 = 1789745224000

func vectorSettings() config.Settings {
	settings := config.DefaultSettings("testdata")
	settings.Signin.Lang = "en"
	settings.Signin.OSName = "Windows"
	settings.Signin.BrowserName = "Chrome"
	settings.Signin.BrowserLanguage = "en-US"
	settings.Signin.BrowserPlatform = "Win32"
	settings.Signin.DeviceMemory = 16
	settings.Signin.CPUCoreNum = 8
	settings.Signin.TimezoneOffsetMin = 480
	return settings
}

func vectorCredential() Credential {
	return Credential{
		Region:       RegionGlobal,
		Token:        fakeToken,
		UUID:         "11111111-2222-3333-4444-555555555555",
		DeviceID:     "12345678",
		UserID:       "987654321012345678",
		ScreenWidth:  1920,
		ScreenHeight: 1080,
	}
}

// signinVectors were produced by the Python implementation in the reverse
// engineering workspace, which is itself validated against a real capture
// (12/12 byte-exact). Re-deriving them here with a synthetic token keeps the
// golden values while keeping the live token out of the repository.
var signinVectors = []struct {
	name   string
	path   string
	method string
	body   string
	signed string
	wire   string
	stamp  string
	xSig   string
	yy     string
}{
	{
		name:   "GET status",
		path:   "/minimax-cloud/api/v1/signin/status",
		method: "GET",
		body:   "",
		signed: "/minimax-cloud/api/v1/signin/status?device_platform=web&biz_id=3&app_id=3001&version_code=22201&unix=1789745224000&timezone_offset=28800&sys_language=en&lang=en&uuid=11111111-2222-3333-4444-555555555555&device_id=12345678&os_name=Windows&browser_name=Chrome&device_memory=16&cpu_core_num=8&browser_language=en-US&browser_platform=Win32&user_id=987654321012345678&op_ticket=undefined&screen_width=1920&screen_height=1080&token=" + fakeToken + "&client=web",
		wire:   "/minimax-cloud/api/v1/signin/status?device_platform=web&biz_id=3&app_id=3001&version_code=22201&unix=1789745224000&timezone_offset=28800&sys_language=en&lang=en&uuid=11111111-2222-3333-4444-555555555555&device_id=12345678&os_name=Windows&browser_name=Chrome&device_memory=16&cpu_core_num=8&browser_language=en-US&browser_platform=Win32&user_id=987654321012345678&screen_width=1920&screen_height=1080&token=" + fakeToken + "&client=web",
		stamp:  "1789745224",
		xSig:   "b7c604fd69850cef03fe09af52b9b40a",
		yy:     "7a61be96d2237a33bbdbd166e72fcf34",
	},
	{
		name:   "POST claim",
		path:   "/minimax-cloud/api/v1/signin/claim",
		method: "POST",
		body:   "{}",
		signed: "/minimax-cloud/api/v1/signin/claim?device_platform=web&biz_id=3&app_id=3001&version_code=22201&unix=1789745224000&timezone_offset=28800&sys_language=en&lang=en&uuid=11111111-2222-3333-4444-555555555555&device_id=12345678&os_name=Windows&browser_name=Chrome&device_memory=16&cpu_core_num=8&browser_language=en-US&browser_platform=Win32&user_id=987654321012345678&op_ticket=undefined&screen_width=1920&screen_height=1080&token=" + fakeToken + "&client=web",
		wire:   "/minimax-cloud/api/v1/signin/claim?device_platform=web&biz_id=3&app_id=3001&version_code=22201&unix=1789745224000&timezone_offset=28800&sys_language=en&lang=en&uuid=11111111-2222-3333-4444-555555555555&device_id=12345678&os_name=Windows&browser_name=Chrome&device_memory=16&cpu_core_num=8&browser_language=en-US&browser_platform=Win32&user_id=987654321012345678&screen_width=1920&screen_height=1080&token=" + fakeToken + "&client=web",
		stamp:  "1789745224",
		xSig:   "890d2b011f7ca4f3ad7b8007c67c17ed",
		yy:     "e38aee61ff7a508c8951308ff7893b10",
	},
	{
		name:   "POST membership info",
		path:   "/matrix/api/v1/commerce/get_membership_info",
		method: "POST",
		body:   "{}",
		signed: "/matrix/api/v1/commerce/get_membership_info?device_platform=web&biz_id=3&app_id=3001&version_code=22201&unix=1789745224000&timezone_offset=28800&sys_language=en&lang=en&uuid=11111111-2222-3333-4444-555555555555&device_id=12345678&os_name=Windows&browser_name=Chrome&device_memory=16&cpu_core_num=8&browser_language=en-US&browser_platform=Win32&user_id=987654321012345678&op_ticket=undefined&screen_width=1920&screen_height=1080&token=" + fakeToken + "&client=web",
		wire:   "/matrix/api/v1/commerce/get_membership_info?device_platform=web&biz_id=3&app_id=3001&version_code=22201&unix=1789745224000&timezone_offset=28800&sys_language=en&lang=en&uuid=11111111-2222-3333-4444-555555555555&device_id=12345678&os_name=Windows&browser_name=Chrome&device_memory=16&cpu_core_num=8&browser_language=en-US&browser_platform=Win32&user_id=987654321012345678&screen_width=1920&screen_height=1080&token=" + fakeToken + "&client=web",
		stamp:  "1789745224",
		xSig:   "890d2b011f7ca4f3ad7b8007c67c17ed",
		yy:     "acf8046f0f8e6a8547da637b209e3f12",
	},
}

// TestSigninQueryMatchesVectors pins the whole query string, not just its
// parameters. The order is part of the protocol — `yy` digests the encoded URL —
// so a reordering that still parses correctly would be a silent break.
func TestSigninQueryMatchesVectors(t *testing.T) {
	settings := vectorSettings()
	cred := vectorCredential()
	for _, vector := range signinVectors {
		t.Run(vector.name, func(t *testing.T) {
			signed := vector.path + "?" + signinParams(settings, cred, vectorUnixMs, true)
			wire := vector.path + "?" + signinParams(settings, cred, vectorUnixMs, false)

			if signed != vector.signed {
				t.Errorf("signed path mismatch\n got: %s\nwant: %s", signed, vector.signed)
			}
			if wire != vector.wire {
				t.Errorf("wire path mismatch\n got: %s\nwant: %s", wire, vector.wire)
			}
		})
	}
}

// TestSigninHeadersMatchVectors covers both digests. x-signature uses the real
// body (empty for the GET), while yy uses "{}" in both cases — the two are
// computed from different inputs on purpose.
func TestSigninHeadersMatchVectors(t *testing.T) {
	settings := vectorSettings()
	cred := vectorCredential()
	for _, vector := range signinVectors {
		t.Run(vector.name, func(t *testing.T) {
			stamp := time.UnixMilli(vectorUnixMs)
			signed := vector.path + "?" + signinParams(settings, cred, vectorUnixMs, true)

			if got := XSignature(stamp, vector.body); got != vector.xSig {
				t.Errorf("x-signature = %s, want %s", got, vector.xSig)
			}
			if got := YY(signed, "{}", stamp); got != vector.yy {
				t.Errorf("yy = %s, want %s", got, vector.yy)
			}
			if got := strconv.FormatInt(stamp.Unix(), 10); got != vector.stamp {
				t.Errorf("x-timestamp = %s, want %s", got, vector.stamp)
			}
		})
	}
}

// TestSigninWireOmitsOpTicket guards the trap that makes this endpoint hard to
// sign: op_ticket=undefined must appear in the signed string and must not
// appear on the wire. Dropping it from both, or keeping it in both, produces a
// request the upstream rejects.
func TestSigninWireOmitsOpTicket(t *testing.T) {
	settings := vectorSettings()
	cred := vectorCredential()

	signed := signinParams(settings, cred, vectorUnixMs, true)
	wire := signinParams(settings, cred, vectorUnixMs, false)

	if !strings.Contains(signed, "op_ticket=undefined") {
		t.Fatalf("signed query must carry op_ticket=undefined, got %s", signed)
	}
	if strings.Contains(wire, "op_ticket") {
		t.Fatalf("wire query must not carry op_ticket, got %s", wire)
	}
}

// TestCapturedXSignatures checks x-signature against a real capture of the
// international site. Only this header can be asserted from a capture: it
// depends on the timestamp, the static salt and the body, whereas yy digests
// the URL and would therefore embed the session token.
func TestCapturedXSignatures(t *testing.T) {
	cases := []struct {
		name   string
		unixMs int64
		body   string
		want   string
	}{
		{"POST with body", 1789745224000, "{}", "890d2b011f7ca4f3ad7b8007c67c17ed"},
		{"GET without body", 1789745218000, "", "225c5941717446d50ae21ddb59e757ec"},
		{"POST without body", 1789745225000, "", "bc36d049396b00e94636cfacf62dc281"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := XSignature(time.UnixMilli(testCase.unixMs), testCase.body)
			if got != testCase.want {
				t.Errorf("x-signature = %s, want %s", got, testCase.want)
			}
		})
	}
}

// TestSigninParamsSkipEmptyFingerprintDocumentsIntent: an account missing its
// UUID or device id still renders as `key=` rather than dropping the pair, which
// matches URLSearchParams. Routing refuses such an account before it gets here,
// so this only pins the encoding rule.
func TestSigninParamsSkipEmptyFingerprintDocumentsIntent(t *testing.T) {
	settings := vectorSettings()
	cred := vectorCredential()
	cred.UUID = ""

	query := signinParams(settings, cred, vectorUnixMs, false)
	if !strings.Contains(query, "uuid=&") {
		t.Errorf("empty uuid should render as an empty pair, got %s", query)
	}
}

func TestFormEncodeSpaceBecomesPlus(t *testing.T) {
	cases := map[string]string{
		"a b":     "a+b",
		"/x?y=1":  "%2Fx%3Fy%3D1",
		"中":       "%E4%B8%AD",
		"a-b_c.d": "a-b_c.d",
	}
	for input, want := range cases {
		if got := formEncode(input); got != want {
			t.Errorf("formEncode(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNumberOfReadsStringAmounts(t *testing.T) {
	// The credit endpoint reports the balance as a JSON string ("400"), so a
	// plain float64 assertion would read zero and hold a funded account out of
	// rotation.
	cases := []struct {
		value any
		want  int
	}{
		{"400", 400},
		{"0", 0},
		{400.0, 400},
		{"", 0},
		{"abc", 0},
		{nil, 0},
		{"12.9", 12},
	}
	for _, testCase := range cases {
		if got := numberOf(testCase.value); got != testCase.want {
			t.Errorf("numberOf(%#v) = %d, want %d", testCase.value, got, testCase.want)
		}
	}
}

func TestEnvelopeErrorMapsExpiredSession(t *testing.T) {
	expired := map[string]any{"base_resp": map[string]any{"status_code": float64(statusCodeSessionExpired)}}
	if err := envelopeError(expired); !errors.Is(err, ErrInvalidCredential) {
		t.Errorf("expired session should map to ErrInvalidCredential, got %v", err)
	}

	ok := map[string]any{"base_resp": map[string]any{"status_code": float64(0), "status_msg": "ok"}}
	if err := envelopeError(ok); err != nil {
		t.Errorf("a zero status_code is success, got %v", err)
	}

	// statusInfo is the other envelope shape the site uses.
	other := map[string]any{"statusInfo": map[string]any{"code": float64(100001), "message": "boom"}}
	if err := envelopeError(other); err == nil {
		t.Error("a non-zero statusInfo code should be an error")
	}
}

func TestCoreOfUnwrapsData(t *testing.T) {
	wrapped := map[string]any{"data": map[string]any{"scene": float64(2)}}
	if core := coreOf(wrapped); core["scene"] != float64(2) {
		t.Errorf("coreOf should unwrap data, got %#v", core)
	}
	// The credit endpoint answers flat, with no data wrapper.
	flat := map[string]any{"plan_type": float64(1)}
	if core := coreOf(flat); core["plan_type"] != float64(1) {
		t.Errorf("coreOf should pass a flat payload through, got %#v", core)
	}
}

// TestParsePanelReadsTheEchoedBoard covers the panel the claim endpoint sends
// back. It is the post-claim state, so the scheduler stores it instead of the
// board it fetched a moment earlier — which still shows today as unclaimed.
//
// The shape is the one observed live on 2026-09-19: the day the claim landed on
// carries status 3 and is_today, while the untouched days keep status 1.
func TestParsePanelReadsTheEchoedBoard(t *testing.T) {
	panel := parsePanel(map[string]any{
		"scene": float64(2),
		"days": []any{
			map[string]any{"day_no": float64(1), "points": float64(400),
				"status": float64(3), "is_today": false},
			map[string]any{"day_no": float64(2), "points": float64(400),
				"status": float64(3), "is_today": true},
			map[string]any{"day_no": float64(3), "points": float64(400),
				"status": float64(1), "is_today": false},
		},
	})

	if panel.Scene != 2 {
		t.Errorf("scene = %d, want 2", panel.Scene)
	}
	if len(panel.Days) != 3 {
		t.Fatalf("days = %d, want 3", len(panel.Days))
	}
	if !panel.ClaimedToday {
		t.Error("today is status 3 in the payload, so ClaimedToday should be true")
	}
	if panel.TodayDayNo != 2 || panel.TodayPoints != 400 {
		t.Errorf("today = day %d / %d points, want day 2 / 400",
			panel.TodayDayNo, panel.TodayPoints)
	}
	if panel.Days[2].Status != SigninDayUnclaimed {
		t.Errorf("day 3 status = %d, want %d (the panel must not blanket-mark days)",
			panel.Days[2].Status, SigninDayUnclaimed)
	}
}

// TestParsePanelWithoutADataWrapper guards the envelope assumption: the board
// fields sit at the top level of the claim's panel object, not inside another
// data envelope.
func TestParsePanelWithoutADataWrapper(t *testing.T) {
	panel := parsePanel(map[string]any{})
	if panel == nil {
		t.Fatal("parsePanel should never return nil for a valid envelope")
	}
	if len(panel.Days) != 0 {
		t.Errorf("an empty payload should yield no days, got %d", len(panel.Days))
	}
	if panel.ClaimedToday {
		t.Error("nothing was claimed, so ClaimedToday must stay false")
	}
}

// TestScalarOfKeepsLargeIDsExact pins the reason realUserID has to be read as a
// string.
//
// The fixture is 2^53+1 rather than a real account id: it is the smallest
// integer a float64 cannot represent, so it demonstrates the loss without
// putting anybody's account number in the repository.
func TestScalarOfKeepsLargeIDsExact(t *testing.T) {
	const id = "9007199254740993" // 2^53 + 1

	if got := scalarOf(id); got != id {
		t.Errorf("string form = %q, want it unchanged", got)
	}
	if got := scalarOf("  " + id + "  "); got != id {
		t.Errorf("whitespace should be trimmed, got %q", got)
	}
	// A JSON number of that magnitude is already lossy by the time it reaches
	// any, which is exactly why the upstream sends a string. Assert the loss so
	// that if this ever changes, the reason is visible.
	if got := scalarOf(float64(9007199254740993)); got == id {
		t.Errorf("a float64 cannot hold this id exactly; got %q — the upstream "+
			"must keep sending it as a string", got)
	}
	if got := scalarOf(float64(42)); got != "42" {
		t.Errorf("small numbers should render without a decimal point, got %q", got)
	}
	if got := scalarOf(nil); got != "" {
		t.Errorf("missing values should be empty, got %q", got)
	}
}

// TestUserInfoLabelPrefersAName keeps the console from labelling an account
// with a bare number when something friendlier is available.
func TestUserInfoLabelPrefersAName(t *testing.T) {
	cases := []struct {
		info UserInfo
		want string
	}{
		{UserInfo{Name: "林汐音", RealUserID: "557"}, "林汐音"},
		{UserInfo{Email: "a@b.c", RealUserID: "557"}, "a@b.c"},
		{UserInfo{Phone: "+8613800138000", RealUserID: "557"}, "+8613800138000"},
		{UserInfo{RealUserID: "557"}, "557"},
	}
	for _, item := range cases {
		if got := item.info.Label(); got != item.want {
			t.Errorf("Label() = %q, want %q", got, item.want)
		}
	}
}
