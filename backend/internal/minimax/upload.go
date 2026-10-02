package minimax

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"

	"minimax2api/internal/config"
)

// DefaultUploadPreparePath issues a private upload policy: an object key plus a
// pre-signed form the caller POSTs straight to object storage.
//
// The whole attachment pipeline was captured from the web client on 2026-10-02
// (file upload through the browser): prepare → OSS POST → the message body
// references the object key under `cloud`. Nothing here is guessed.
const DefaultUploadPreparePath = "/minimax-cloud/api/v1/uploads/prepare"

// attachmentPurposeMessage is the `purpose` value the web client sends for
// files attached to a chat turn (`MessageAttachment: 1`).
const attachmentPurposeMessage = 1

// PendingAttachment is a file waiting to ride on a turn.
//
// It carries bytes rather than a reference because the upload has to happen
// with the *sending account's* credential — the object store tags the object
// with the uploader's user id, and the agent only reads objects the turn's own
// account uploaded. A gateway-level file registry therefore cannot pre-upload
// against the pool: the bytes travel with the turn and are uploaded inside
// `Completion`, right before the message POST, after the lease names an
// account.
type PendingAttachment struct {
	Name     string
	MimeType string
	Size     int64
	Data     []byte
	// ObjectKey is filled in once the file is on the object store.
	ObjectKey string
}

// formField is one key/value pair of the pre-signed OSS form, in the order the
// upstream returned it. Order of the non-file fields is not validated by OSS,
// but the file part must come last, so the pairs are kept as an ordered slice
// rather than a map.
type formField struct {
	Key   string
	Value string
}

// uploadResult is the subset of the prepare response the uploader needs.
type uploadResult struct {
	ObjectKey  string
	UploadURL  string
	FormFields []formField
}

// uploadCache remembers object keys per (account token, file content), so a
// retried turn on the same account does not push the same bytes twice. Tokens
// rotate and processes restart; a stale miss just re-uploads.
type uploadCache struct {
	mu    sync.Mutex
	byKey map[string]string
}

const uploadCacheLimit = 1024

func uploadCacheKey(token string, data []byte) string {
	sum := sha256.Sum256(data)
	tokenSum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(tokenSum[:8]) + "/" + hex.EncodeToString(sum[:])
}

func (c *uploadCache) get(key string) (string, bool) {
	if c == nil || c.byKey == nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.byKey[key]
	return v, ok
}

func (c *uploadCache) put(key, objectKey string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byKey == nil {
		c.byKey = make(map[string]string)
	}
	if len(c.byKey) >= uploadCacheLimit {
		// Crude but bounded: a full cache is evidence of a pathological caller,
		// not of value worth evicting one entry at a time.
		c.byKey = make(map[string]string)
	}
	c.byKey[key] = objectKey
}

// UploadAttachment pushes one file through the two-step private upload and
// returns its object key.
//
// Step 1 asks the cloud API (signed like every other call) for a policy; step 2
// POSTs the file to object storage with that policy and expects a bare 204.
func (c *Client) UploadAttachment(ctx context.Context, cred Credential, att *PendingAttachment) error {
	if strings.TrimSpace(cred.Token) == "" {
		return ErrInvalidCredential
	}
	if att == nil || len(att.Data) == 0 {
		return errors.New("attachment has no content")
	}
	if strings.TrimSpace(att.MimeType) == "" {
		att.MimeType = "application/octet-stream"
	}
	if strings.TrimSpace(att.Name) == "" {
		att.Name = "upload.bin"
	}

	settings := c.settings()
	if key := uploadCacheKey(cred.Token, att.Data); key != "" {
		if cached, ok := c.uploads.get(key); ok {
			att.ObjectKey = cached
			return nil
		}
	}

	result, err := c.prepareUpload(ctx, cred, settings, att)
	if err != nil {
		return err
	}
	if err := c.postToObjectStore(ctx, settings, result, att); err != nil {
		return err
	}
	c.uploads.put(uploadCacheKey(cred.Token, att.Data), result.ObjectKey)
	att.ObjectKey = result.ObjectKey
	return nil
}

func (c *Client) prepareUpload(ctx context.Context, cred Credential, settings config.Settings, att *PendingAttachment) (*uploadResult, error) {
	body, err := json.Marshal(map[string]any{
		"purpose":    attachmentPurposeMessage,
		"file_name":  att.Name,
		"mime_type":  att.MimeType,
		"size_bytes": len(att.Data),
	})
	if err != nil {
		return nil, err
	}

	req, err := c.newRequest(ctx, settings, cred, http.MethodPost, requestTarget{
		Path: uploadPreparePath(settings),
	}, body)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req, settings)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, ErrInvalidCredential
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("minimax upload prepare HTTP %d: %s", resp.StatusCode, snippet(raw))
	}

	// The response is either `{base_resp, object_key, upload_instruction}` at
	// the root or the same object nested under `data`; the web client accepts
	// both (`o.object_key ? o : o.data ?? o`), so both are handled here.
	var envelope struct {
		Data     json.RawMessage `json:"data"`
		BaseResp *baseResp       `json:"base_resp"`
		Code     *int            `json:"code"`
	}
	payload := json.RawMessage(raw)
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("minimax upload prepare: %w", err)
	}
	if len(envelope.Data) > 0 {
		payload = envelope.Data
	}
	var parsed struct {
		BaseResp          *baseResp          `json:"base_resp"`
		ObjectKey         string             `json:"object_key"`
		UploadInstruction *uploadInstruction `json:"upload_instruction"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return nil, fmt.Errorf("minimax upload prepare: %w", err)
	}
	if code := baseRespCode(parsed.BaseResp, envelope.Code); code != 0 {
		return nil, fmt.Errorf("minimax upload prepare failed: %s", baseRespMsg(parsed.BaseResp, raw))
	}
	if strings.TrimSpace(parsed.ObjectKey) == "" || parsed.UploadInstruction == nil {
		return nil, fmt.Errorf("minimax upload prepare: response lacks object_key or upload_instruction: %s", snippet(raw))
	}
	instruction := parsed.UploadInstruction
	if !strings.EqualFold(strings.TrimSpace(instruction.Method), http.MethodPost) {
		return nil, fmt.Errorf("minimax upload prepare: unsupported method %q", instruction.Method)
	}
	if strings.TrimSpace(instruction.URL) == "" {
		return nil, errors.New("minimax upload prepare: upload_instruction has no url")
	}
	fields, err := orderedFormFields(instruction.FormFields)
	if err != nil {
		return nil, fmt.Errorf("minimax upload prepare: %w", err)
	}
	return &uploadResult{
		ObjectKey:  strings.TrimSpace(parsed.ObjectKey),
		UploadURL:  strings.TrimSpace(instruction.URL),
		FormFields: fields,
	}, nil
}

// postToObjectStore sends the pre-signed form plus the file. The policy
// conditions pin the exact size and content type, so the body is built from the
// same bytes the prepare call described. The file part goes last.
func (c *Client) postToObjectStore(ctx context.Context, settings config.Settings, result *uploadResult, att *PendingAttachment) error {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	for _, field := range result.FormFields {
		if field.Key == "file" {
			return errors.New("minimax upload prepare: form_fields must not carry the file")
		}
		if err := writer.WriteField(field.Key, field.Value); err != nil {
			return err
		}
	}
	part, err := writer.CreateFormFile("file", att.Name)
	if err != nil {
		return err
	}
	if _, err := part.Write(att.Data); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, result.UploadURL, bytes.NewReader(buf.Bytes()))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", writer.FormDataContentType())
	resp, err := c.do(req, settings)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("minimax object upload HTTP %d: %s", resp.StatusCode, snippet(body))
	}
	return nil
}

// orderedFormFields decodes a JSON object of string fields while keeping the
// order the upstream chose. A map would lose it, and mimicking the captured
// request as closely as possible has been this codebase's habit for a reason.
func orderedFormFields(raw json.RawMessage) ([]formField, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	open, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := open.(json.Delim); !ok || delim != '{' {
		return nil, errors.New("form_fields is not an object")
	}
	var fields []formField
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, errors.New("form_fields has a non-string key")
		}
		var value string
		if err := dec.Decode(&value); err != nil {
			return nil, fmt.Errorf("field %q: %w", key, err)
		}
		fields = append(fields, formField{Key: key, Value: value})
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return fields, nil
}

func uploadPreparePath(settings config.Settings) string {
	if path := strings.TrimSpace(settings.Upstream.UploadPreparePath); path != "" {
		return path
	}
	return DefaultUploadPreparePath
}

type uploadInstruction struct {
	Method     string          `json:"method"`
	URL        string          `json:"url"`
	FormFields json.RawMessage `json:"form_fields"`
}

// baseRespCode reads whichever error envelope the response carries.
func baseRespCode(base *baseResp, legacyCode *int) int {
	if base != nil && base.StatusCode != 0 {
		return base.StatusCode
	}
	if legacyCode != nil && *legacyCode != 0 {
		return *legacyCode
	}
	return 0
}

func baseRespMsg(base *baseResp, raw []byte) string {
	if base != nil && strings.TrimSpace(base.StatusMsg) != "" {
		return base.StatusMsg
	}
	return snippet(raw)
}
