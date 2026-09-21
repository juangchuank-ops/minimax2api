package minimax

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"minimax2api/internal/config"
)

// The wire shapes in this file are copied from a real session that produced a
// video: an artefact carries the node id the download endpoint wants, and the
// turn before it carries no `artifacts` key at all.

func driveTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	settings := config.DefaultSettings(t.TempDir())
	settings.Upstream.BaseURL = server.URL
	settings.Upstream.SummariesPath = "/minimax-cloud/api/v1/session/{session_id}/input-summaries"
	settings.Upstream.DriveFilePath = "/minimax-cloud/api/v1/drive/file/{node_id}"
	return New(func() config.Settings { return settings }), server
}

func TestSessionArtifactsReadsEveryTurnAndToleratesMissingKey(t *testing.T) {
	var gotPath string
	client, _ := driveTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"has_more":false,"summaries":[` +
			`{"user_input":{"msg_id":"444160364462157"}},` +
			`{"artifacts":[{"category":"videos","file_ext":"mp4","mime_type":"video/mp4",` +
			`"name":"444160978935873.mp4","node_id":"444166461083748","node_type":2,` +
			`"parent_id":"444166461083747","session_id":"444160364462157",` +
			`"size_bytes":801051,"created_at":1789992001949,"updated_at":1789992002005}]}]` +
			`,"base_resp":{"status_code":0,"status_msg":"ok"}}`))
	})

	artifacts, err := client.SessionArtifacts(context.Background(), Credential{Token: "t"}, "444160364462157")
	if err != nil {
		t.Fatalf("SessionArtifacts: %v", err)
	}
	if gotPath != "/minimax-cloud/api/v1/session/444160364462157/input-summaries" {
		t.Errorf("path = %q, want the session id substituted in", gotPath)
	}
	if len(artifacts) != 1 {
		t.Fatalf("artifacts = %d, want 1 (a turn without the key must not break the walk)", len(artifacts))
	}
	if artifacts[0].NodeID != "444166461083748" || artifacts[0].Kind() != "video" {
		t.Errorf("artifact = %+v, want the video node", artifacts[0])
	}
	if artifacts[0].SizeBytes != 801051 {
		t.Errorf("size = %d, want 801051", artifacts[0].SizeBytes)
	}
}

// TestSessionArtifactsSurfacesAnUpstreamRefusal: a non-zero base_resp is a
// refusal that arrived with HTTP 200, and reporting it as "no artefacts" would
// make a broken call indistinguishable from a turn that produced nothing.
func TestSessionArtifactsSurfacesAnUpstreamRefusal(t *testing.T) {
	client, _ := driveTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"summaries":[],"base_resp":{"status_code":1022100011,"status_msg":"session expired"}}`))
	})

	_, err := client.SessionArtifacts(context.Background(), Credential{Token: "t"}, "1")
	if err == nil || !strings.Contains(err.Error(), "1022100011") {
		t.Fatalf("err = %v, want the upstream code in it", err)
	}
}

func TestArtifactKindFallsBackToTheMimeType(t *testing.T) {
	cases := []struct {
		category string
		mime     string
		want     string
	}{
		{"videos", "video/mp4", "video"},
		{"Images", "", "image"},
		{"", "video/quicktime", "video"},
		{"", "image/png", "image"},
		{"", "audio/mpeg", "audio"},
		{"documents", "application/pdf", "file"},
		{"", "", "file"},
	}
	for _, c := range cases {
		got := Artifact{Category: c.category, MimeType: c.mime}.Kind()
		if got != c.want {
			t.Errorf("Kind(%q, %q) = %q, want %q", c.category, c.mime, got, c.want)
		}
	}
}

func TestSessionMediaKeepsOnlyThisTurnAndOnlyMedia(t *testing.T) {
	client, _ := driveTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/input-summaries") {
			_, _ = w.Write([]byte(`{"summaries":[{"artifacts":[` +
				// An older turn's video: same session, wrong turn.
				`{"category":"videos","mime_type":"video/mp4","node_id":"100","created_at":1000},` +
				// This turn's video.
				`{"category":"videos","mime_type":"video/mp4","node_id":"200","created_at":5000},` +
				// This turn, but not media.
				`{"category":"documents","mime_type":"application/pdf","node_id":"300","created_at":5000}` +
				`]}],"base_resp":{"status_code":0,"status_msg":"ok"}}`))
			return
		}
		// Only node 200 is ever asked about.
		if !strings.Contains(r.URL.Path, "/drive/file/200/download-url") {
			t.Errorf("unexpected download lookup: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"download_url":"matrix-internal.oss.example/Mavis/1/files/2/3.mp4",` +
			`"base_resp":{"status_code":0,"status_msg":"ok"}}`))
	})

	media, err := client.SessionMedia(context.Background(), Credential{Token: "t"}, "7", 4000)
	if err != nil {
		t.Fatalf("SessionMedia: %v", err)
	}
	if len(media) != 1 {
		t.Fatalf("media = %+v, want exactly this turn's video", media)
	}
	if media[0].Kind != "video" {
		t.Errorf("kind = %q, want video", media[0].Kind)
	}
	// The drive answers without a scheme, and a scheme-less URL cannot be
	// fetched — it looks like a working link until something opens it.
	if media[0].URL != "https://matrix-internal.oss.example/Mavis/1/files/2/3.mp4" {
		t.Errorf("url = %q, want an https link", media[0].URL)
	}
}

// TestSessionMediaDropsArtifactsItCannotResolve: an entry with no playable URL
// is not media, it is a promise. Returning it would make the caller report a
// success it cannot deliver.
func TestSessionMediaDropsArtifactsItCannotResolve(t *testing.T) {
	client, _ := driveTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/input-summaries") {
			_, _ = w.Write([]byte(`{"summaries":[{"artifacts":[` +
				`{"category":"videos","mime_type":"video/mp4","node_id":"1","created_at":9},` +
				`{"category":"videos","mime_type":"video/mp4","node_id":"2","created_at":9}` +
				`]}],"base_resp":{"status_code":0,"status_msg":"ok"}}`))
			return
		}
		if strings.Contains(r.URL.Path, "/drive/file/1/") {
			_, _ = w.Write([]byte(`{"download_url":"","base_resp":{"status_code":0,"status_msg":"ok"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"download_url":"cdn.example/ok.mp4","base_resp":{"status_code":0,"status_msg":"ok"}}`))
	})

	media, err := client.SessionMedia(context.Background(), Credential{Token: "t"}, "7", 0)
	if err != nil {
		t.Fatalf("SessionMedia: %v", err)
	}
	if len(media) != 1 || media[0].URL != "https://cdn.example/ok.mp4" {
		t.Fatalf("media = %+v, want only the resolvable one", media)
	}
}

func TestDownloadURLReportsAnUpstreamRefusal(t *testing.T) {
	client, _ := driveTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"download_url":"","base_resp":{"status_code":1406011050,"status_msg":"internal error"}}`))
	})

	_, err := client.DownloadURL(context.Background(), Credential{Token: "t"}, "1")
	if err == nil || !strings.Contains(err.Error(), "1406011050") {
		t.Fatalf("err = %v, want the upstream code in it", err)
	}
}

func TestDownloadURLWithNoNodeIsNotAnError(t *testing.T) {
	client, _ := driveTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("an empty node id must not spend a request")
		w.WriteHeader(http.StatusInternalServerError)
	})

	got, err := client.DownloadURL(context.Background(), Credential{Token: "t"}, "  ")
	if err != nil || got != "" {
		t.Fatalf("got (%q, %v), want an empty result and no error", got, err)
	}
}

// TestAbsoluteURLLeavesARealURLAlone guards the other direction: the endpoint
// currently answers without a scheme, and a future version that starts sending
// one must not end up with two.
func TestAbsoluteURLLeavesARealURLAlone(t *testing.T) {
	cases := map[string]string{
		"cdn.example/a.mp4":         "https://cdn.example/a.mp4",
		"https://cdn.example/a.mp4": "https://cdn.example/a.mp4",
		"http://cdn.example/a.mp4":  "http://cdn.example/a.mp4",
		"  cdn.example/a.mp4  ":     "https://cdn.example/a.mp4",
		"":                          "",
		"not a url at all":          "not a url at all",
	}
	for input, want := range cases {
		if got := absoluteURL(input); got != want {
			t.Errorf("absoluteURL(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestSessionArtifactsRejectsAnHTTPError keeps "the lookup failed" from looking
// like "the turn produced nothing".
func TestSessionArtifactsRejectsAnHTTPError(t *testing.T) {
	client, _ := driveTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("Not Found"))
	})

	_, err := client.SessionArtifacts(context.Background(), Credential{Token: "t"}, "7")
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v, want a 404 in it", err)
	}
}
