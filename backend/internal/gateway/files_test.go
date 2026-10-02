package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"path/filepath"
	"strings"
	"testing"
)

// These tests drive the whole file path end to end against a stub upstream:
// POST /v1/files stores bytes locally, a chat turn referencing the file makes
// the gateway run prepare + object-storage push with the sending account's
// credential, and the upstream message body carries the meta/cloud envelope
// the captured web-client upload used.

const fileTestUploadResponse = `{"base_resp":{"status_code":0,"status_msg":"ok"},` +
	`"object_key":"9484b529-a583-4f03-afe5-780ae74609c8",` +
	`"upload_instruction":{"method":"POST","url":"%s","form_fields":{"OSSAccessKeyId":"ak","key":"9484b529-a583-4f03-afe5-780ae74609c8"}}}`

// newFileHarness builds the stub upstream the file flow needs: prepare answers
// with an instruction pointing at ossURL (filled in by the test once the stub
// server exists), /upload is the object-store step, everything else is a
// normal answer.
func newFileHarness(t *testing.T) (*harness, *[]map[string]any, *string) {
	t.Helper()
	ossURL := ""
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/uploads/prepare"):
			if r.Header.Get("token") == "" || r.Header.Get("x-signature") == "" {
				t.Errorf("prepare call is unsigned")
			}
			w.Header().Set("content-type", "application/json")
			fmt.Fprintf(w, fileTestUploadResponse, ossURL)
		case r.URL.Path == "/upload":
			w.WriteHeader(http.StatusNoContent)
		default:
			// Session handshake and message stream, as the standard stub
			// answers them.
			upstreamFunc(nil, func(string) string { return upstreamText("收到") })(w, r)
		}
	})
	ossURL = h.upstream.URL + "/upload"
	bodies := h.captureMessageBodies(t)
	return h, bodies, &ossURL
}

// uploadFile posts a multipart file to /v1/files.
func uploadFile(t *testing.T, h *harness, filename, content string) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	// A browser puts the File object's mime type on the part header; quote it
	// so the stored mime type comes from the header, the way real uploads do.
	part, _ := writer.CreatePart(textproto.MIMEHeader{
		"Content-Type":        {"text/markdown"},
		"Content-Disposition": {fmt.Sprintf(`form-data; name="file"; filename=%q`, filename)},
	})
	_, _ = part.Write([]byte(content))
	_ = writer.WriteField("purpose", "user_data")
	_ = writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/files", &buf)
	req.Header.Set("content-type", writer.FormDataContentType())
	req.Header.Set("authorization", "Bearer "+h.key)
	rec := httptest.NewRecorder()
	h.gateway.FilesUpload(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload file: HTTP %d: %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("upload response not JSON: %v", err)
	}
	return out
}

func TestFilesUploadListContentDeleteRoundtrip(t *testing.T) {
	h, _, _ := newFileHarness(t)
	file := uploadFile(t, h, "notes.md", "hello file")
	id, _ := file["id"].(string)
	if !strings.HasPrefix(id, "file-") {
		t.Fatalf("id = %v, want a file- prefix", file["id"])
	}
	if file["filename"] != "notes.md" || file["bytes"] != float64(len("hello file")) ||
		file["purpose"] != "user_data" || file["object"] != "file" {
		t.Errorf("file object = %v", file)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/files", nil)
	req.Header.Set("authorization", "Bearer "+h.key)
	rec := httptest.NewRecorder()
	h.gateway.FilesList(rec, req)
	var list struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Data) != 1 {
		t.Fatalf("list = %s (err %v), want one entry", rec.Body.String(), err)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/files/"+id+"/content", nil)
	req.Header.Set("authorization", "Bearer "+h.key)
	req.SetPathValue("id", id)
	rec = httptest.NewRecorder()
	h.gateway.FilesContent(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "hello file" {
		t.Fatalf("content = HTTP %d %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("content-type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Errorf("content-type = %q, want the stored mime type", ct)
	}

	req = httptest.NewRequest(http.MethodDelete, "/v1/files/"+id, nil)
	req.Header.Set("authorization", "Bearer "+h.key)
	req.SetPathValue("id", id)
	rec = httptest.NewRecorder()
	h.gateway.FilesDelete(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = HTTP %d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodDelete, "/v1/files/"+id, nil)
	req.Header.Set("authorization", "Bearer "+h.key)
	req.SetPathValue("id", id)
	rec = httptest.NewRecorder()
	h.gateway.FilesDelete(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("second delete = HTTP %d, want 404", rec.Code)
	}
}

func TestChatWithFileIDUploadsAndReferencesObjectKey(t *testing.T) {
	h, bodies, _ := newFileHarness(t)
	h.addAccount(t, "acc-1", "token-1", 1)

	file := uploadFile(t, h, "New_API.md", "# 标题\n正文")
	id, _ := file["id"].(string)

	rec := h.chat(t, map[string]any{
		"model": "minimax-agent",
		"messages": []map[string]any{{
			"role": "user",
			"content": []map[string]any{
				{"type": "file", "file": map[string]any{"file_id": id}},
			},
		}},
	}, h.key)
	if rec.Code != http.StatusOK {
		t.Fatalf("chat with file_id: HTTP %d: %s", rec.Code, rec.Body.String())
	}
	if len(*bodies) != 1 {
		t.Fatalf("message bodies = %d, want 1", len(*bodies))
	}
	body := (*bodies)[0]
	if body["content"] != " " {
		t.Errorf("content = %v, want a single space for a file-only turn", body["content"])
	}
	attachments, _ := body["attachments"].([]any)
	if len(attachments) != 1 {
		t.Fatalf("attachments = %#v, want one envelope", body["attachments"])
	}
	entry, _ := attachments[0].(map[string]any)
	meta, _ := entry["meta"].(map[string]any)
	cloud, _ := entry["cloud"].(map[string]any)
	if meta == nil || cloud == nil {
		t.Fatalf("attachment = %#v, want meta and cloud", entry)
	}
	if meta["attachment_type"] != "file" || meta["file_name"] != "New_API.md" {
		t.Errorf("meta = %v", meta)
	}
	if cloud["object_key"] != "9484b529-a583-4f03-afe5-780ae74609c8" {
		t.Errorf("cloud = %v", cloud)
	}
}

func TestChatWithInlineFileDataUploadsBytes(t *testing.T) {
	h, bodies, _ := newFileHarness(t)
	h.addAccount(t, "acc-1", "token-1", 1)

	rec := h.chat(t, map[string]any{
		"model": "minimax-agent",
		"messages": []map[string]any{{
			"role": "user",
			"content": []map[string]any{
				{"type": "text", "text": "总结这个文件"},
				{"type": "file", "file": map[string]any{
					"filename":  "report.txt",
					"file_data": "data:text/plain;base64,5L2g5aW9",
				}},
			},
		}},
	}, h.key)
	if rec.Code != http.StatusOK {
		t.Fatalf("chat with file_data: HTTP %d: %s", rec.Code, rec.Body.String())
	}
	if len(*bodies) != 1 {
		t.Fatalf("message bodies = %d, want 1", len(*bodies))
	}
	body := (*bodies)[0]
	attachments, _ := body["attachments"].([]any)
	if len(attachments) != 1 {
		t.Fatalf("attachments = %#v, want one envelope", body["attachments"])
	}
	meta, _ := attachments[0].(map[string]any)["meta"].(map[string]any)
	if meta == nil || meta["file_name"] != "report.txt" || meta["mime_type"] != "text/plain" {
		t.Errorf("meta = %v", meta)
	}
}

func TestChatRejectsUnknownFileIDWithoutSpendingAnAccount(t *testing.T) {
	h, _, _ := newFileHarness(t)
	rec := h.chat(t, map[string]any{
		"model": "minimax-agent",
		"messages": []map[string]any{{
			"role": "user",
			"content": []map[string]any{
				{"type": "file", "file": map[string]any{"file_id": "file-nope"}},
			},
		}},
	}, h.key)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("HTTP %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "unknown file_id") {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestUploadsDirSitsBesideGeneratedMedia(t *testing.T) {
	// The uploads store is a sibling of the generated-media dir so both move
	// with the data volume and the media janitor never sweeps user files.
	if got := uploadsDir(filepath.Join("data", "generated")); got != filepath.Join("data", "uploads") {
		t.Errorf("uploadsDir = %q", got)
	}
}

func TestDecodeDataURLSplitsMimeAndPayload(t *testing.T) {
	mimeType, payload, err := decodeDataURL("data:application/pdf;base64,JVBERi0=")
	if err != nil {
		t.Fatalf("decodeDataURL: %v", err)
	}
	if mimeType != "application/pdf" || string(payload) != "%PDF-" {
		t.Errorf("mime=%q payload=%q", mimeType, payload)
	}

	if _, _, err := decodeDataURL("data:text/plain;base64,!!!"); err == nil {
		t.Error("invalid base64 must error")
	}
	if _, _, err := decodeDataURL("https://example.test/file.pdf"); err == nil {
		t.Error("non-data URL must error")
	}
}

func TestFilePartFieldsParseThroughContent(t *testing.T) {
	_, _, files := parseContent([]byte(`[
		{"type":"text","text":"看这个"},
		{"type":"file","file":{"file_id":"file-abc"}}
	]`))
	if len(files) != 1 || files[0].File.FileID != "file-abc" {
		t.Fatalf("files = %#v", files)
	}

	_, urls, files := parseContent([]byte(`[{"type":"image_url","image_url":{"url":"https://x/y.png"}}]`))
	if len(urls) != 1 || len(files) != 0 {
		t.Errorf("urls = %v, files = %v — image parsing must be untouched", urls, files)
	}
}
