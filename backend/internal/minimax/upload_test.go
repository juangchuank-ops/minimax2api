package minimax

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"minimax2api/internal/config"
)

// The wire shapes in this file follow the captured file upload (2026-10-02):
// a signed prepare call answers with an object key and a pre-signed form, the
// form plus the file go to object storage, and the message body references the
// key under `cloud`.

// stubOSS builds the object-storage stub; its URL is what a prepare response
// would hand out.
func stubOSS(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	oss := httptest.NewServer(handler)
	t.Cleanup(oss.Close)
	return oss
}

// uploadTestClient wires a client at a stub cloud API answering with an upload
// instruction that points at ossURL.
func uploadTestClient(t *testing.T, ossURL string, prepareHandler http.HandlerFunc) *Client {
	t.Helper()
	prepare := httptest.NewServer(prepareHandler)
	t.Cleanup(prepare.Close)

	settings := config.DefaultSettings(t.TempDir())
	settings.Upstream.BaseURL = prepare.URL
	return New(func() config.Settings { return settings })
}

func sampleUpload() PendingAttachment {
	return PendingAttachment{
		Name:     "notes.md",
		MimeType: "text/markdown",
		Size:     int64(len("hello")),
		Data:     []byte("hello"),
	}
}

func TestUploadAttachmentPostsFormThenFileAndReturnsKey(t *testing.T) {
	var prepareBody map[string]any
	var ossBody []byte
	var ossContentType string

	oss := stubOSS(t, func(w http.ResponseWriter, r *http.Request) {
		ossContentType = r.Header.Get("content-type")
		ossBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	})

	client := uploadTestClient(t, oss.URL+"/",
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/minimax-cloud/api/v1/uploads/prepare" {
				t.Errorf("prepare path = %q", r.URL.Path)
			}
			if r.Header.Get("token") == "" || r.Header.Get("x-signature") == "" {
				t.Errorf("prepare call is unsigned: token=%q x-signature=%q", r.Header.Get("token"), r.Header.Get("x-signature"))
			}
			raw, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(raw, &prepareBody); err != nil {
				t.Errorf("prepare body is not JSON: %v", err)
			}
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"base_resp":{"status_code":0,"status_msg":"ok"},` +
				`"object_key":"9484b529-a583-4f03-afe5-780ae74609c8",` +
				`"upload_instruction":{"method":"POST","url":"` + oss.URL + `/",` +
				`"form_fields":{"OSSAccessKeyId":"ak","key":"9484b529-a583-4f03-afe5-780ae74609c8","policy":"pol","Signature":"sig"}}}`))
		},
	)

	att := sampleUpload()
	if err := client.UploadAttachment(context.Background(), Credential{Token: "t"}, &att); err != nil {
		t.Fatalf("UploadAttachment: %v", err)
	}
	if att.ObjectKey != "9484b529-a583-4f03-afe5-780ae74609c8" {
		t.Errorf("object key = %q, want the prepare-issued key", att.ObjectKey)
	}
	if prepareBody["purpose"] != float64(1) || prepareBody["file_name"] != "notes.md" ||
		prepareBody["mime_type"] != "text/markdown" || prepareBody["size_bytes"] != float64(len("hello")) {
		t.Errorf("prepare body = %v", prepareBody)
	}

	// The OSS request must be a multipart form carrying the pre-signed fields
	// and the file — the file last, the way the browser sends it.
	mediaType, params, err := mime.ParseMediaType(ossContentType)
	if err != nil || mediaType != "multipart/form-data" {
		t.Fatalf("OSS content-type = %q (err %v), want multipart/form-data", ossContentType, err)
	}
	reader := multipart.NewReader(strings.NewReader(string(ossBody)), params["boundary"])
	var order []string
	var filePayload []byte
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("parse OSS multipart: %v", err)
		}
		name := part.FormName()
		if name == "file" {
			filePayload, _ = io.ReadAll(part)
		} else {
			value, _ := io.ReadAll(part)
			if len(value) == 0 {
				t.Errorf("field %q came back empty", name)
			}
		}
		order = append(order, name)
	}
	wantOrder := []string{"OSSAccessKeyId", "key", "policy", "Signature", "file"}
	if strings.Join(order, ",") != strings.Join(wantOrder, ",") {
		t.Errorf("OSS field order = %v, want %v (file last)", order, wantOrder)
	}
	if string(filePayload) != "hello" {
		t.Errorf("file payload = %q", filePayload)
	}
}

func TestUploadAttachmentAcceptsNestedDataEnvelope(t *testing.T) {
	oss := stubOSS(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	client := uploadTestClient(t, oss.URL+"/",
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"base_resp":{"status_code":0},` +
				`"object_key":"aa583-4f03",` +
				`"upload_instruction":{"method":"post","url":"` + oss.URL + `/","form_fields":{"key":"aa583-4f03"}}}}`))
		},
	)

	att := sampleUpload()
	if err := client.UploadAttachment(context.Background(), Credential{Token: "t"}, &att); err != nil {
		t.Fatalf("UploadAttachment: %v", err)
	}
	if att.ObjectKey != "aa583-4f03" {
		t.Errorf("object key = %q", att.ObjectKey)
	}
}

func TestUploadAttachmentSurfacesPrepareAndStorageFailures(t *testing.T) {
	t.Run("prepare reports base_resp error", func(t *testing.T) {
		oss := stubOSS(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
		client := uploadTestClient(t, oss.URL+"/",
			func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("content-type", "application/json")
				_, _ = w.Write([]byte(`{"base_resp":{"status_code":1402,"status_msg":"file too large"}}`))
			},
		)
		att := sampleUpload()
		err := client.UploadAttachment(context.Background(), Credential{Token: "t"}, &att)
		if err == nil || !strings.Contains(err.Error(), "file too large") {
			t.Errorf("err = %v, want the upstream status message", err)
		}
	})

	t.Run("object store must answer 204", func(t *testing.T) {
		oss := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "denied", http.StatusForbidden)
		}))
		defer oss.Close()
		settings := config.DefaultSettings(t.TempDir())
		prepare := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"object_key":"k","upload_instruction":{"method":"POST","url":"` + oss.URL + `/","form_fields":{"key":"k"}}}`))
		}))
		defer prepare.Close()
		settings.Upstream.BaseURL = prepare.URL
		client := New(func() config.Settings { return settings })

		att := sampleUpload()
		err := client.UploadAttachment(context.Background(), Credential{Token: "t"}, &att)
		if err == nil || !strings.Contains(err.Error(), "HTTP 403") {
			t.Errorf("err = %v, want HTTP 403 from object storage", err)
		}
	})
}

func TestUploadAttachmentCachesPerCredentialAndContent(t *testing.T) {
	oss := stubOSS(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	var prepareCalls int
	client := uploadTestClient(t, oss.URL+"/",
		func(w http.ResponseWriter, r *http.Request) {
			prepareCalls++
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"object_key":"cached-key","upload_instruction":{"method":"POST","url":"` + oss.URL + `/","form_fields":{"key":"k"}}}`))
		},
	)

	first := sampleUpload()
	if err := client.UploadAttachment(context.Background(), Credential{Token: "t"}, &first); err != nil {
		t.Fatalf("first upload: %v", err)
	}
	second := sampleUpload()
	if err := client.UploadAttachment(context.Background(), Credential{Token: "t"}, &second); err != nil {
		t.Fatalf("second upload: %v", err)
	}
	if prepareCalls != 1 {
		t.Errorf("prepare calls = %d, want 1 (same account and content must reuse the key)", prepareCalls)
	}

	// A different credential is a different upload: objects are tagged with
	// the uploader's user id, so another account's key is worthless.
	third := sampleUpload()
	if err := client.UploadAttachment(context.Background(), Credential{Token: "other"}, &third); err != nil {
		t.Fatalf("third upload: %v", err)
	}
	if prepareCalls != 2 {
		t.Errorf("prepare calls after new credential = %d, want 2", prepareCalls)
	}
}

func TestBuildMessageBodyCarriesFileEnvelope(t *testing.T) {
	settings := config.DefaultSettings(t.TempDir())
	body := buildMessageBody(settings, Options{
		Text: " ",
		Files: []PendingAttachment{{
			Name: "New_API.md", MimeType: "text/markdown", Size: 12365, ObjectKey: "9484b529-a583",
		}},
	}, "turn-1")

	attachments, ok := body["attachments"].([]any)
	if !ok || len(attachments) != 1 {
		t.Fatalf("attachments = %#v, want exactly one envelope", body["attachments"])
	}
	entry, _ := attachments[0].(map[string]any)
	meta, _ := entry["meta"].(map[string]any)
	cloud, _ := entry["cloud"].(map[string]any)
	if meta == nil || cloud == nil {
		t.Fatalf("attachment = %#v, want meta and cloud", entry)
	}
	if meta["attachment_type"] != "file" || meta["file_name"] != "New_API.md" ||
		meta["mime_type"] != "text/markdown" || meta["size_bytes"] != int64(12365) {
		t.Errorf("meta = %v", meta)
	}
	if cloud["object_key"] != "9484b529-a583" {
		t.Errorf("cloud = %v", cloud)
	}
}

func TestBuildMessageBodySendsSpaceForFileOnlyTurn(t *testing.T) {
	// The captured file-only turn carried content " " — an empty string is
	// what a bare turn looks like, and the two must stay distinguishable.
	settings := config.DefaultSettings(t.TempDir())
	body := buildMessageBody(settings, Options{
		Files: []PendingAttachment{{Name: "a.md", MimeType: "text/markdown", ObjectKey: "k"}},
	}, "turn-1")
	if body["content"] != " " {
		t.Errorf("content = %q, want a single space", body["content"])
	}

	imageOnly := buildMessageBody(settings, Options{
		Images: []UploadedImage{{URL: "https://example.test/a.png", Name: "a.png"}},
	}, "turn-1")
	if imageOnly["content"] != "" {
		t.Errorf("image-only content = %q, want the empty string preserved", imageOnly["content"])
	}
}
