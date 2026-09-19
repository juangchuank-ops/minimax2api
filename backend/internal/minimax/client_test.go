package minimax

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

// --- frame parsing ---------------------------------------------------------

func TestParseFrameReadsDocumentedShape(t *testing.T) {
	frame := parseFrame(`data:{"type":6,"agent_message_chunk":{"msg_content":"你好"}}`)
	if frame.Data == nil {
		t.Fatalf("frame not parsed: %+v", frame)
	}
	if got := firstDeepString(frame.Data, contentKeys); got != "你好" {
		t.Fatalf("content = %q, want 你好", got)
	}
}

func TestParseFrameToleratesCRLFAndBlankData(t *testing.T) {
	frame := parseFrame("event:message\r\ndata:{\"type\":6}\r\n")
	if frame.Data == nil {
		t.Fatal("a CRLF-terminated frame should still parse")
	}
	if frame.Event != "message" {
		t.Fatalf("event = %q", frame.Event)
	}
}

// [DONE] is a terminator, not a payload, and must never be decoded as JSON.
func TestParseFrameLeavesDoneUnparsed(t *testing.T) {
	frame := parseFrame("data:[DONE]")
	if frame.Data != nil {
		t.Fatal("[DONE] must not be parsed into an object")
	}
	if frame.Raw != "[DONE]" {
		t.Fatalf("raw = %q", frame.Raw)
	}
}

// --- frame dispatch --------------------------------------------------------

func TestHandleFrameSplitsReasoningFromAnswer(t *testing.T) {
	client := &Client{}
	result := &Result{}
	var streamed, streamedThinking string
	opts := Options{
		OnDelta:    func(s string) { streamed += s },
		OnThinking: func(s string) { streamedThinking += s },
	}

	if err := client.handleFrame(`data:{"type":4,"thinking_chunk":{"reasoning_content":"想"}}`, opts, result); err != nil {
		t.Fatalf("thinking frame: %v", err)
	}
	if err := client.handleFrame(`data:{"type":6,"agent_message_chunk":{"msg_content":"答"}}`, opts, result); err != nil {
		t.Fatalf("content frame: %v", err)
	}

	if result.Thinking != "想" || streamedThinking != "想" {
		t.Fatalf("thinking = %q / %q, want 想", result.Thinking, streamedThinking)
	}
	if result.Text != "答" || streamed != "答" {
		t.Fatalf("text = %q / %q, want 答", result.Text, streamed)
	}
}

// The frame types are numeric and undocumented, so dispatch is driven by the
// payload keys. A renumbered event must still be understood.
func TestHandleFrameDoesNotDependOnTypeCode(t *testing.T) {
	client := &Client{}
	result := &Result{}
	if err := client.handleFrame(`data:{"type":9999,"agent_message_chunk":{"msg_content":"ok"}}`, Options{}, result); err != nil {
		t.Fatalf("frame: %v", err)
	}
	if result.Text != "ok" {
		t.Fatalf("text = %q, want ok", result.Text)
	}
}

func TestHandleFrameSurfacesUpstreamError(t *testing.T) {
	client := &Client{}
	err := client.handleFrame(`data:{"error_msg":"额度不足"}`, Options{}, &Result{})
	if err == nil || !strings.Contains(err.Error(), "额度不足") {
		t.Fatalf("err = %v, want the upstream message", err)
	}
}

// A success envelope carries status_code 0 and status_msg "success"; neither is
// an error and treating them as one would abort every healthy stream.
func TestHandleFrameIgnoresSuccessEnvelope(t *testing.T) {
	client := &Client{}
	frame := `data:{"status_msg":"success","base_resp":{"status_code":0,"status_msg":"success"}}`
	if err := client.handleFrame(frame, Options{}, &Result{}); err != nil {
		t.Fatalf("err = %v, want nil for a success frame", err)
	}
}

func TestHandleFrameRejectsNonZeroStatus(t *testing.T) {
	client := &Client{}
	err := client.handleFrame(`data:{"status_code":1004}`, Options{}, &Result{})
	if err == nil {
		t.Fatal("a non-zero status code should be reported as an error")
	}
}

func TestHandleFrameCapturesIdentifiers(t *testing.T) {
	client := &Client{}
	result := &Result{}
	frame := `data:{"session_id":"sess-1","message_id":"msg-2","agent_message_chunk":{"msg_content":"x"}}`
	if err := client.handleFrame(frame, Options{}, result); err != nil {
		t.Fatalf("frame: %v", err)
	}
	if result.SessionID != "sess-1" {
		t.Fatalf("session = %q", result.SessionID)
	}
	if result.MessageID != "msg-2" {
		t.Fatalf("message = %q", result.MessageID)
	}
}

// --- media collection ------------------------------------------------------

func TestCollectMediaFindsImageAndVideoURLs(t *testing.T) {
	client := &Client{}
	result := &Result{}
	frame := `data:{"agent_message_chunk":{"msg_content":""},"attachments":[` +
		`{"image_url":"https://cdn/a.png"},{"video_url":"https://cdn/b.mp4"},` +
		`{"avatar_url":"https://cdn/avatar.png"}]}`
	if err := client.handleFrame(frame, Options{}, result); err != nil {
		t.Fatalf("frame: %v", err)
	}

	if len(result.Media) != 2 {
		t.Fatalf("media = %+v, want exactly the image and the video", result.Media)
	}
	kinds := map[string]string{}
	for _, item := range result.Media {
		kinds[item.URL] = item.Kind
	}
	if kinds["https://cdn/a.png"] != "image" {
		t.Fatalf("image kind = %q", kinds["https://cdn/a.png"])
	}
	if kinds["https://cdn/b.mp4"] != "video" {
		t.Fatalf("video kind = %q", kinds["https://cdn/b.mp4"])
	}
}

// A bare "url" field is as likely to be an avatar or a citation as generated
// media, so it is deliberately ignored.
func TestCollectMediaIgnoresAmbiguousURLField(t *testing.T) {
	client := &Client{}
	result := &Result{}
	if err := client.handleFrame(`data:{"agent_message_chunk":{"msg_content":"x"},"url":"https://cdn/x.png"}`, Options{}, result); err != nil {
		t.Fatalf("frame: %v", err)
	}
	if len(result.Media) != 0 {
		t.Fatalf("media = %+v, want none", result.Media)
	}
}

func TestDedupeMediaCollapsesRepeats(t *testing.T) {
	items := []MediaRef{
		{Kind: "image", URL: "https://cdn/a.png?sig=1"},
		{Kind: "image", URL: "https://cdn/a.png?sig=2"},
		{Kind: "video", URL: "https://cdn/b.mp4"},
	}
	got := dedupeMedia(items)
	if len(got) != 2 {
		t.Fatalf("dedupeMedia = %+v, want 2 entries", got)
	}
}

// --- session id extraction -------------------------------------------------

func TestSessionIDFromAcceptsSeveralShapes(t *testing.T) {
	cases := []struct {
		body string
		want string
	}{
		{`{"session_id":"a"}`, "a"},
		{`{"data":{"session_id":"b"}}`, "b"},
		{`{"data":{"sessionId":"c"}}`, "c"},
		{`"d"`, "d"},
		{``, ""},
		{`   `, ""},
		{`{"base_resp":{"status_code":0}}`, ""},
	}
	for _, tc := range cases {
		if got := sessionIDFrom([]byte(tc.body)); got != tc.want {
			t.Errorf("sessionIDFrom(%q) = %q, want %q", tc.body, got, tc.want)
		}
	}
}

// TestSessionIDFromRefusesAnythingButJSON pins the fix for a bug that hid
// itself well: a wrong session path is answered by the SPA's catch-all route
// with **200 and a page of HTML**, and the extractor used to return that page as
// the session id. The result was a request URL containing an entire web page,
// which the edge rejected with a 400 that read like an upstream block — so the
// mistake pointed everywhere except at the path.
//
// Refusing to guess turns that into an honest failure at the source.
func TestSessionIDFromRefusesAnythingButJSON(t *testing.T) {
	cases := []string{
		`<!DOCTYPE html><html><head><title>MiniMax</title></head></html>`,
		`not json`,
		`<html>`,
		`{"unterminated":`,
	}
	for _, body := range cases {
		if got := sessionIDFrom([]byte(body)); got != "" {
			t.Errorf("sessionIDFrom(%.40q) = %q, want an empty id", body, got)
		}
	}
}

// --- token decoding --------------------------------------------------------

func jwtWithPayload(t *testing.T, payload string) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	body := base64.RawURLEncoding.EncodeToString([]byte(payload))
	return header + "." + body + ".signature"
}

func TestParseTokenExtractsIdentity(t *testing.T) {
	token := jwtWithPayload(t, `{"user_id":"42","email":"user@example.com","exp":1780470413}`)
	info, err := ParseToken(token)
	if err != nil {
		t.Fatalf("ParseToken: %v", err)
	}
	if info.UserID != "42" {
		t.Fatalf("user id = %q", info.UserID)
	}
	if info.Identifier() != "user@example.com" {
		t.Fatalf("identifier = %q", info.Identifier())
	}
	if info.Region != RegionGlobal {
		t.Fatalf("region = %q, want global for an email account", info.Region)
	}
	if !info.ExpiresAt.Equal(time.Unix(1780470413, 0)) {
		t.Fatalf("expiry = %v", info.ExpiresAt)
	}
}

// A +86 number means the account lives on the mainland deployment, which is the
// only signal available without asking the upstream.
func TestParseTokenInfersMainlandFromPhone(t *testing.T) {
	token := jwtWithPayload(t, `{"user_id":"7","phone":"+8613800138000"}`)
	info, err := ParseToken(token)
	if err != nil {
		t.Fatalf("ParseToken: %v", err)
	}
	if info.Region != RegionCN {
		t.Fatalf("region = %q, want cn", info.Region)
	}
	if info.Identifier() != "+8613800138000" {
		t.Fatalf("identifier = %q", info.Identifier())
	}
}

// The mainland site hands out "realUserID+token" pairs, and operators paste
// them whole.
func TestParseTokenAcceptsUserIDPrefix(t *testing.T) {
	inner := jwtWithPayload(t, `{"user_id":"9","email":"a@b.c"}`)
	info, err := ParseToken("450234567894+" + inner)
	if err != nil {
		t.Fatalf("ParseToken: %v", err)
	}
	if info.Identifier() != "a@b.c" {
		t.Fatalf("identifier = %q", info.Identifier())
	}
}

func TestParseTokenRejectsGarbage(t *testing.T) {
	for _, raw := range []string{"", "not-a-token", "only.two"} {
		if _, err := ParseToken(raw); !errors.Is(err, ErrNotJWT) {
			t.Errorf("ParseToken(%q) err = %v, want ErrNotJWT", raw, err)
		}
	}
}

// A token with no identity claims still parses; it simply has nothing to label
// the account with, and the console falls back to the region prefix.
func TestParseTokenWithoutClaims(t *testing.T) {
	token := jwtWithPayload(t, `{"iat":1}`)
	info, err := ParseToken(token)
	if err != nil {
		t.Fatalf("ParseToken: %v", err)
	}
	if info.Identifier() != "" {
		t.Fatalf("identifier = %q, want empty", info.Identifier())
	}
	if info.Region != RegionGlobal {
		t.Fatalf("region = %q, want the global default", info.Region)
	}
}

// TestLoopbackHostsAreRecognised pins the decision that keeps a configured proxy
// from swallowing a request to a local upstream.
//
// A proxy is set to reach the internet, and an upstream on 127.0.0.1 is not on
// the internet. Routing it anyway gets `502` with an empty body back from the
// proxy, which reads as the upstream being down — the failure gives no hint that
// the request never left the machine.
func TestLoopbackHostsAreRecognised(t *testing.T) {
	loopback := []string{"127.0.0.1", "127.1.2.3", "localhost", "LOCALHOST", "::1"}
	for _, host := range loopback {
		if !isLoopbackHost(host) {
			t.Errorf("isLoopbackHost(%q) = false, want true", host)
		}
	}
	// The real upstreams, plus the shapes that merely look local.
	remote := []string{
		"agent.minimax.io", "agent.minimaxi.com",
		"127.0.0.1.example.com", // resolves elsewhere despite the prefix
		"10.0.0.1",              // private, but a proxy is the only way to reach it
		"", "0.0.0.0",
	}
	for _, host := range remote {
		if isLoopbackHost(host) {
			t.Errorf("isLoopbackHost(%q) = true, want false", host)
		}
	}
}
