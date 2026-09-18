package admin

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

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
	if len(account.UUID) != 32 || len(account.DeviceID) != 32 {
		t.Fatalf("fingerprint = %q / %q, want 32 hex chars each", account.UUID, account.DeviceID)
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
