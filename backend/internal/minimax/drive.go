package minimax

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"minimax2api/internal/config"
)

// Generated media does not arrive through the conversation stream.
//
// A turn that produces a video answers with prose and tool-call frames, and the
// file itself is written into the account's drive. Nothing in the SSE payload
// names it, which is why a turn can succeed — the agent really did submit the
// job, and the file really does exist — and still come back with zero media. The
// gateway was reading the wrong document.
//
// The file is announced in two steps, both plain GETs on the API host:
//
//	GET {summariesPath}          → summaries[].artifacts[] carry node_id,
//	                               category ("videos"), mime_type, name, size
//	GET {driveFilePath}/download-url → a signed OSS URL, ~2h of validity
//
// The download URL comes back *without a scheme* and with a signature in its
// query, so it is normalised to https here and handed on as-is. It is a bearer
// token with an expiry: it must not be cached, and it must not be logged.
//
// Both paths are settings because they belong to the upstream bundle, and a
// rename should be an edit rather than a rebuild.

const (
	// DefaultSummariesPath lists a session's turns with their artefacts.
	DefaultSummariesPath = "/minimax-cloud/api/v1/session/{session_id}/input-summaries"
	// DefaultDriveFilePath addresses one drive node; the download URL is its
	// `download-url` sub-resource.
	DefaultDriveFilePath = "/minimax-cloud/api/v1/drive/file/{node_id}"
)

// Artifact is one file an agent turn produced.
//
// Only the fields the gateway acts on are modelled. `category` and `mime_type`
// are the two that decide whether it is worth resolving a download URL for;
// `created_at` is what ties an artefact to the turn that produced it.
type Artifact struct {
	NodeID    string `json:"node_id"`
	ParentID  string `json:"parent_id"`
	SessionID string `json:"session_id"`
	Name      string `json:"name"`
	Category  string `json:"category"`
	MimeType  string `json:"mime_type"`
	FileExt   string `json:"file_ext"`
	SizeBytes int64  `json:"size_bytes"`
	CreatedAt int64  `json:"created_at"`
}

// Kind maps an artefact onto the media vocabulary the rest of the gateway uses.
//
// `category` is the primary signal and `mime_type` the fallback: the category is
// the drive's own grouping and is the more stable of the two, but an artefact
// with a category the gateway has never seen should still be classed by what it
// actually is rather than dropped.
func (a Artifact) Kind() string {
	switch strings.ToLower(strings.TrimSpace(a.Category)) {
	case "videos", "video":
		return "video"
	case "images", "image", "pictures":
		return "image"
	case "audios", "audio", "voices":
		return "audio"
	}
	switch {
	case strings.HasPrefix(a.MimeType, "video/"):
		return "video"
	case strings.HasPrefix(a.MimeType, "image/"):
		return "image"
	case strings.HasPrefix(a.MimeType, "audio/"):
		return "audio"
	}
	return "file"
}

// summariesResponse is the wire shape of the summaries call.
//
// `artifacts` is absent rather than empty on turns that produced nothing, which
// is why it is a slice the decoder may leave nil.
type summariesResponse struct {
	BaseResp  baseResp    `json:"base_resp"`
	HasMore   bool        `json:"has_more"`
	Summaries []turnBlock `json:"summaries"`
}

type turnBlock struct {
	Artifacts []Artifact `json:"artifacts"`
}

type baseResp struct {
	StatusCode int    `json:"status_code"`
	StatusMsg  string `json:"status_msg"`
}

// SessionArtifacts lists every file the session has produced.
//
// The whole session is asked for rather than the last turn, because the call
// offers no "since" cursor and because the order of `summaries` is not
// documented. Filtering by time is the caller's job — see SessionMedia.
func (c *Client) SessionArtifacts(ctx context.Context, cred Credential, sessionID string) ([]Artifact, error) {
	path := strings.ReplaceAll(summariesPath(c.settings()), "{session_id}", sessionID)
	raw, err := c.getJSON(ctx, cred, path)
	if err != nil {
		return nil, err
	}
	var parsed summariesResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("minimax summaries: %w", err)
	}
	if parsed.BaseResp.StatusCode != 0 {
		return nil, fmt.Errorf("minimax summaries: %d %s", parsed.BaseResp.StatusCode, parsed.BaseResp.StatusMsg)
	}
	out := make([]Artifact, 0, 4)
	for _, turn := range parsed.Summaries {
		out = append(out, turn.Artifacts...)
	}
	return out, nil
}

// SessionMedia returns the media a session produced at or after `sinceUnixMs`.
//
// The cutoff is what makes this safe to call on every turn: a session is long
// lived, so the artefacts it already had are not this turn's answer. Artefacts
// whose download URL cannot be resolved are dropped rather than returned
// unplayable — a URL-less entry is not media, it is a promise.
func (c *Client) SessionMedia(ctx context.Context, cred Credential, sessionID string, sinceUnixMs int64) ([]MediaRef, error) {
	artifacts, err := c.SessionArtifacts(ctx, cred, sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]MediaRef, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.CreatedAt < sinceUnixMs {
			continue
		}
		if artifact.Kind() == "file" {
			continue
		}
		link, err := c.DownloadURL(ctx, cred, artifact.NodeID)
		if err != nil {
			return out, err
		}
		if link == "" {
			continue
		}
		out = append(out, MediaRef{Kind: artifact.Kind(), URL: link})
	}
	return dedupeMedia(out), nil
}

// DownloadURL resolves one drive node to a signed, absolute URL.
func (c *Client) DownloadURL(ctx context.Context, cred Credential, nodeID string) (string, error) {
	if strings.TrimSpace(nodeID) == "" {
		return "", nil
	}
	path := strings.ReplaceAll(driveFilePath(c.settings()), "{node_id}", nodeID) + "/download-url"
	raw, err := c.getJSON(ctx, cred, path)
	if err != nil {
		return "", err
	}
	var parsed struct {
		DownloadURL string   `json:"download_url"`
		BaseResp    baseResp `json:"base_resp"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("minimax download url: %w", err)
	}
	if parsed.BaseResp.StatusCode != 0 {
		return "", fmt.Errorf("minimax download url: %d %s", parsed.BaseResp.StatusCode, parsed.BaseResp.StatusMsg)
	}
	return absoluteURL(parsed.DownloadURL), nil
}

// absoluteURL gives the drive's scheme-less link a scheme.
//
// The endpoint answers with `host/path?signature` and no scheme at all. Passing
// that on unchanged produces a URL no client can fetch, and it looks like a
// working link right up until something tries to open it.
func absoluteURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://") {
		return trimmed
	}
	if parsed, err := url.Parse("https://" + trimmed); err == nil && parsed.Host != "" {
		return parsed.String()
	}
	return trimmed
}

// getJSON issues one signed GET and decodes the body.
//
// Every call here is a plain read: no stream, no body, API host. Anything that
// answers with a status the gateway does not recognise is reported with a
// snippet rather than an empty result, because "no media" and "the lookup
// failed" must not be the same answer.
func (c *Client) getJSON(ctx context.Context, cred Credential, path string) ([]byte, error) {
	settings := c.settings()
	if strings.TrimSpace(cred.Token) == "" {
		return nil, ErrInvalidCredential
	}
	req, err := c.newRequest(ctx, settings, cred, http.MethodGet, requestTarget{Path: path}, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req, settings)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, ErrInvalidCredential
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("minimax %s HTTP %d: %s", path, resp.StatusCode, snippet(raw))
	}
	return raw, nil
}

func summariesPath(settings config.Settings) string {
	if path := strings.TrimSpace(settings.Upstream.SummariesPath); path != "" {
		return path
	}
	return DefaultSummariesPath
}

func driveFilePath(settings config.Settings) string {
	if path := strings.TrimSpace(settings.Upstream.DriveFilePath); path != "" {
		return path
	}
	return DefaultDriveFilePath
}
