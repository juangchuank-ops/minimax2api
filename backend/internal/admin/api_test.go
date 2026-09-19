package admin

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"minimax2api/internal/config"
	"minimax2api/internal/store"
)

// unsignedToken builds a JWT-shaped credential with the given claims. Nothing
// verifies the signature, so a stub one is enough to exercise the decoder.
func unsignedToken(t *testing.T, claims map[string]any) string {
	t.Helper()
	encode := func(value any) string {
		blob, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return base64.RawURLEncoding.EncodeToString(blob)
	}
	return encode(map[string]any{"alg": "HS256", "typ": "JWT"}) + "." + encode(claims) + ".signature"
}

func TestBuildAccountInfersRegionFromPhone(t *testing.T) {
	cases := []struct {
		name   string
		claims map[string]any
		want   string
	}{
		{"mainland phone", map[string]any{"user_id": "1", "phone": "+8613800138000"}, store.RegionCN},
		{"international phone", map[string]any{"user_id": "2", "phone": "+14155550123"}, store.RegionGlobal},
		{"email only", map[string]any{"user_id": "3", "email": "a@b.com"}, store.RegionGlobal},
		{"no claims", map[string]any{"user_id": "4"}, store.RegionGlobal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			account, err := buildAccount(accountPayload{Token: unsignedToken(t, tc.claims)})
			if err != nil {
				t.Fatalf("buildAccount: %v", err)
			}
			if account.Region != tc.want {
				t.Fatalf("region = %q, want %q", account.Region, tc.want)
			}
		})
	}
}

// The auto sentinel has to be distinguishable from "leave it alone", otherwise
// an operator cannot move an account between deployments through the console.
func TestBuildAccountAutoSentinelDefersToToken(t *testing.T) {
	token := unsignedToken(t, map[string]any{"user_id": "1", "phone": "+8613800138000"})

	for _, sentinel := range []string{"auto", "AUTO", " auto ", "infer", "detect", "自动识别"} {
		account, err := buildAccount(accountPayload{Token: token, Region: sentinel})
		if err != nil {
			t.Fatalf("buildAccount(%q): %v", sentinel, err)
		}
		if account.Region != store.RegionCN {
			t.Fatalf("region for sentinel %q = %q, want %q", sentinel, account.Region, store.RegionCN)
		}
	}
}

func TestBuildAccountHonoursExplicitRegion(t *testing.T) {
	// An explicit region beats the token's own claim: the operator may be
	// routing through a proxy whose tokens do not carry a usable phone.
	token := unsignedToken(t, map[string]any{"user_id": "1", "phone": "+8613800138000"})
	account, err := buildAccount(accountPayload{Token: token, Region: "global"})
	if err != nil {
		t.Fatalf("buildAccount: %v", err)
	}
	if account.Region != store.RegionGlobal {
		t.Fatalf("region = %q, want %q", account.Region, store.RegionGlobal)
	}

	if _, err := buildAccount(accountPayload{Token: token, Region: "moon"}); err == nil {
		t.Fatal("expected an unknown region to be rejected")
	}
}

func TestBuildAccountGeneratesFingerprint(t *testing.T) {
	account, err := buildAccount(accountPayload{Token: unsignedToken(t, map[string]any{"user_id": "1"})})
	if err != nil {
		t.Fatalf("buildAccount: %v", err)
	}
	// The signature covers uuid and device_id, so an account without them can
	// never be routed. Generating a self-consistent pair is what makes a bare
	// token paste work.
	//
	// The two fields have *different* shapes, and this test used to assert the
	// opposite — 32 hex characters each — because one generator was used for
	// both. That assertion is what kept the defect invisible: it described the
	// implementation instead of the upstream, so it passed while every
	// `/minimax-cloud/…` call failed with an error that named no field.
	if _, err := strconv.Atoi(account.DeviceID); err != nil {
		t.Fatalf("device id %q is not a number: %v", account.DeviceID, err)
	}
	if !strings.Contains(account.UUID, "-") {
		t.Fatalf("uuid = %q, want the site's dashed UUID shape", account.UUID)
	}
	if account.UUID == account.DeviceID {
		t.Fatal("uuid and device_id should not be identical")
	}
	if account.Kind != store.KindToken {
		t.Fatalf("kind = %q, want %q", account.Kind, store.KindToken)
	}
	if account.Name == "" {
		t.Fatal("expected a generated name")
	}
}

func TestBuildAccountRejectsMalformedToken(t *testing.T) {
	cases := map[string]string{
		"empty":      "",
		"whitespace": "abc def ghi jkl mno",
		"too short":  "short",
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := buildAccount(accountPayload{Token: token}); err == nil {
				t.Fatalf("expected %q to be rejected", token)
			}
		})
	}
}

func TestRegionFromToken(t *testing.T) {
	cn := unsignedToken(t, map[string]any{"user_id": "1", "phone": "+8613800138000"})
	if got := regionFromToken(cn); got != store.RegionCN {
		t.Fatalf("regionFromToken(cn) = %q", got)
	}
	if got := regionFromToken("not-a-jwt"); got != "" {
		t.Fatalf("regionFromToken(garbage) = %q, want empty", got)
	}
	// The mainland site prefixes the token with realUserID, joined by a plus.
	if got := regionFromToken("450234567894+" + cn); got != store.RegionCN {
		t.Fatalf("regionFromToken(realUserID+token) = %q", got)
	}
}

func TestIsAutoRegion(t *testing.T) {
	for _, value := range []string{"auto", "Auto", " infer ", "detect", "自动", "自动识别"} {
		if !isAutoRegion(value) {
			t.Fatalf("isAutoRegion(%q) = false, want true", value)
		}
	}
	for _, value := range []string{"", "cn", "global", "国内", "国际"} {
		if isAutoRegion(value) {
			t.Fatalf("isAutoRegion(%q) = true, want false", value)
		}
	}
}

func TestParseImportShapes(t *testing.T) {
	cn := unsignedToken(t, map[string]any{"user_id": "1", "phone": "+8613800138000"})
	global := unsignedToken(t, map[string]any{"user_id": "2", "email": "a@b.com"})

	entries := parseImport(
		"# comment\n\n"+cn+"\n"+
			"named----"+global+"\n"+
			"regional----"+global+"----cn\n"+
			"cn:"+cn+"\n",
		nil,
	)
	if len(entries) != 4 {
		t.Fatalf("parsed %d entries, want 4: %+v", len(entries), entries)
	}
	if entries[0].Token != cn || entries[0].Name != "" {
		t.Fatalf("bare token parsed as %+v", entries[0])
	}
	if entries[1].Name != "named" || entries[1].Token != global {
		t.Fatalf("name----token parsed as %+v", entries[1])
	}
	if entries[2].Name != "regional" || entries[2].Region != store.RegionCN {
		t.Fatalf("name----token----region parsed as %+v", entries[2])
	}
	if entries[3].Token != cn || entries[3].Region != store.RegionCN {
		t.Fatalf("cn: prefix parsed as %+v", entries[3])
	}
}

// JSON input wins over the text box, which is what makes an export/import round
// trip lossless including fingerprints.
func TestParseImportPrefersJSON(t *testing.T) {
	blob, err := json.Marshal(map[string]any{
		"accounts": []map[string]any{{"token": "from-json-token", "region": "cn", "uuid": "u"}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	entries := parseImport("from-text-token", blob)
	if len(entries) != 1 || entries[0].Token != "from-json-token" {
		t.Fatalf("entries = %+v, want the JSON payload", entries)
	}
}

func TestDefaultAccountNameCarriesRegion(t *testing.T) {
	cn := defaultAccountName("13800138000", store.RegionCN)
	global := defaultAccountName("someone@example.com", store.RegionGlobal)
	if cn == global {
		t.Fatalf("names should differ by region: %q", cn)
	}
	if cn[:3] != "cn-" || global[:7] != "global-" {
		t.Fatalf("unexpected names: %q / %q", cn, global)
	}
}

// describeToken is what keeps a token from leaking into an error message shown
// in the console.
func TestDescribeTokenMasksSecret(t *testing.T) {
	token := unsignedToken(t, map[string]any{"user_id": "1"})
	described := describeToken(token)
	if len(token) > 16 && strings.Contains(described, token[:20]) {
		t.Fatalf("describeToken leaked the token: %q", described)
	}
}

// jsonKeys returns a struct's json field names, skipping anything untagged.
func jsonKeys(t *testing.T, value any) map[string]bool {
	t.Helper()
	kind := reflect.TypeOf(value)
	keys := make(map[string]bool, kind.NumField())
	for i := range kind.NumField() {
		name := strings.Split(kind.Field(i).Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		keys[name] = true
	}
	return keys
}

// getSettings builds its response by hand rather than marshalling config.Settings.
// That is deliberate — the console must not receive every internal knob — but it
// means a new setting is invisible to the console until someone remembers to add
// it here. This test is that reminder: it would have caught `userInfoPath` going
// missing, which is exactly the kind of omission that shows up as a blank field
// in the settings page and nowhere else.
func TestGetSettingsExposesEveryUpstreamAndServerField(t *testing.T) {
	st, err := store.Open(t.TempDir(), "admin", "admin12345")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	settings := config.DefaultSettings(t.TempDir())
	api := &API{
		store:    st,
		settings: func() config.Settings { return settings },
		started:  time.Now(),
	}

	recorder := httptest.NewRecorder()
	api.getSettings(recorder, httptest.NewRequest("GET", "/admin/api/settings", nil))

	var body map[string]map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, recorder.Body.String())
	}

	// Only the two sections that are written out field by field; the rest are
	// handed to the encoder wholesale and cannot drift.
	cases := []struct {
		section string
		value   any
	}{
		{"server", settings.Server},
		{"upstream", settings.Upstream},
	}
	for _, tc := range cases {
		want := jsonKeys(t, tc.value)
		got := body[tc.section]
		for key := range want {
			if _, ok := got[key]; !ok {
				t.Errorf("%s is missing %q, so the console cannot show or edit it", tc.section, key)
			}
		}
		for key := range got {
			if !want[key] {
				t.Errorf("%s exposes %q, which is not a field of the settings struct", tc.section, key)
			}
		}
	}
}

// --- generated device fingerprints -----------------------------------------

// TestGeneratedDeviceIDIsAllDigits pins the one property the upstream checks.
//
// A device id that is not a number answers
//
//	400 {"error":"internal error","errorCode":50001,"base_resp":{"status_code":1406011050}}
//
// on every `/minimax-cloud/…` call, and nothing in that response says which
// field is wrong — it reads as an upstream outage. The check-in endpoints and
// `/v1/api/user/info` accept anything, so a wrong value here fails exactly the
// requests that were never exercised against the real server.
func TestGeneratedDeviceIDIsAllDigits(t *testing.T) {
	for i := 0; i < 200; i++ {
		got := newDeviceID()
		if _, err := strconv.Atoi(got); err != nil {
			t.Fatalf("device id %q is not a number: %v", got, err)
		}
		// The site's own fallback is `1e7 + rand(9e7)`, so eight digits and no
		// leading zero. Any digit string is accepted, but matching the site
		// keeps a generated value indistinguishable from a captured one.
		if len(got) != 8 {
			t.Fatalf("device id %q has %d digits, want 8", got, len(got))
		}
		if got[0] == '0' {
			t.Fatalf("device id %q has a leading zero, so it renders as fewer than eight digits", got)
		}
	}
}

// TestGeneratedUUIDLooksLikeAUUID: the upstream tolerates any string here, but a
// value in the site's own shape is worth keeping — a future stricter check
// should not be able to tell a generated fingerprint from a captured one.
func TestGeneratedUUIDLooksLikeAUUID(t *testing.T) {
	pattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	seen := make(map[string]bool, 200)
	for i := 0; i < 200; i++ {
		got := newUUID()
		if !pattern.MatchString(got) {
			t.Fatalf("uuid %q is not a v4 UUID", got)
		}
		if seen[got] {
			t.Fatalf("uuid %q was generated twice", got)
		}
		seen[got] = true
	}
}

// TestTheTwoFingerprintFieldsAreNotInterchangeable guards the actual defect: one
// generator was used for both fields, so the device id came out as 32 hex
// characters. Sharing a generator is the obvious-looking thing to do and is
// wrong.
func TestTheTwoFingerprintFieldsAreNotInterchangeable(t *testing.T) {
	device := newDeviceID()
	if strings.ContainsAny(device, "abcdef") {
		t.Errorf("the device id %q contains hex letters, so it is not a number", device)
	}
	uuid := newUUID()
	if _, err := strconv.Atoi(uuid); err == nil {
		t.Errorf("the uuid %q parses as a number; the two fields have different shapes", uuid)
	}
}
