package gateway

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"minimax2api/internal/minimax"
	"minimax2api/internal/store"
)

// maxUploadBytes caps one uploaded file. 100MB matches the limit the web
// client applies to its own message attachments; the upstream will have its
// own opinion, and prepare will say so.
const maxUploadBytes = 100 << 20

// fileRecord is one entry in the gateway's own file registry.
//
// Files live on local disk under the data directory and are referenced from
// chat turns by id. The registry is deliberately not part of the store: files
// are large blobs and the store is a small JSON document — coupling them would
// put megabytes of base64 into every settings save.
type fileRecord struct {
	ID        string    `json:"id"`
	Filename  string    `json:"filename"`
	Purpose   string    `json:"purpose"`
	MimeType  string    `json:"mime_type"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"created_at"`
}

// fileStore keeps uploaded files on disk with a JSON index beside them.
type fileStore struct {
	mu      sync.RWMutex
	dir     string
	records map[string]fileRecord
}

func newFileStore(dir string) *fileStore {
	return &fileStore{dir: dir, records: map[string]fileRecord{}}
}

func (s *fileStore) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.records == nil {
		s.records = map[string]fileRecord{}
	}
	raw, err := os.ReadFile(s.indexPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var records []fileRecord
	if err := json.Unmarshal(raw, &records); err != nil {
		return err
	}
	for _, record := range records {
		s.records[record.ID] = record
	}
	return nil
}

func (s *fileStore) indexPath() string { return filepath.Join(s.dir, "index.json") }

func (s *fileStore) persistLocked() error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	records := make([]fileRecord, 0, len(s.records))
	for _, record := range s.records {
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].CreatedAt.Before(records[j].CreatedAt) })
	raw, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.indexPath() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.indexPath())
}

// put stores the bytes under a fresh id and registers it.
func (s *fileStore) put(filename, purpose, mimeType string, data []byte) (fileRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fileRecord{}, err
	}
	record := fileRecord{
		ID:        "file-" + randomID(24),
		Filename:  filename,
		Purpose:   purpose,
		MimeType:  mimeType,
		Size:      int64(len(data)),
		CreatedAt: time.Now(),
	}
	if err := os.WriteFile(filepath.Join(s.dir, record.ID), data, 0o644); err != nil {
		return fileRecord{}, err
	}
	s.records[record.ID] = record
	if err := s.persistLocked(); err != nil {
		_ = os.Remove(filepath.Join(s.dir, record.ID))
		delete(s.records, record.ID)
		return fileRecord{}, err
	}
	return record, nil
}

func (s *fileStore) get(id string) (fileRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.records[id]
	return record, ok
}

func (s *fileStore) list() []fileRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	records := make([]fileRecord, 0, len(s.records))
	for _, record := range s.records {
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].CreatedAt.Before(records[j].CreatedAt) })
	return records
}

func (s *fileStore) delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.records[id]; !ok {
		return false
	}
	delete(s.records, id)
	if err := s.persistLocked(); err != nil {
		s.records[id] = fileRecord{ID: id}
		return false
	}
	_ = os.Remove(filepath.Join(s.dir, id))
	return true
}

// pending loads a stored file as a turn attachment. The bytes are read at turn
// time: the file may have been uploaded long before the chat that uses it.
func (s *fileStore) pending(record fileRecord) (minimax.PendingAttachment, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, record.ID))
	if err != nil {
		return minimax.PendingAttachment{}, err
	}
	return minimax.PendingAttachment{
		Name:     record.Filename,
		MimeType: record.MimeType,
		Size:     int64(len(data)),
		Data:     data,
	}, nil
}

// uploadsDir places the uploads store beside the generated-media directory:
// the same data volume, a sibling the media janitor does not sweep.
func uploadsDir(generatedDir string) string {
	return filepath.Join(filepath.Dir(generatedDir), "uploads")
}

// filesHandler guards the file endpoints behind the same keys as chat.
func (g *Gateway) filesHandler(w http.ResponseWriter, r *http.Request, action func(http.ResponseWriter, *http.Request, *store.ClientKey)) {
	out := &respWriter{ResponseWriter: w}
	key, err := g.authenticate(r)
	if err != nil {
		writeError(out, http.StatusUnauthorized, err.Error())
		return
	}
	action(out, r, key)
}

// FilesUpload implements POST /v1/files (multipart form: file, purpose).
func (g *Gateway) FilesUpload(w http.ResponseWriter, r *http.Request) {
	g.filesHandler(w, r, g.filesUpload)
}

func (g *Gateway) filesUpload(out http.ResponseWriter, r *http.Request, _ *store.ClientKey) {
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeError(out, http.StatusBadRequest, "invalid multipart form: "+err.Error())
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(out, http.StatusBadRequest, "form field \"file\" is required")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxUploadBytes+1))
	if err != nil {
		writeError(out, http.StatusBadRequest, "cannot read upload: "+err.Error())
		return
	}
	if len(data) == 0 {
		writeError(out, http.StatusBadRequest, "uploaded file is empty")
		return
	}
	if int64(len(data)) > maxUploadBytes {
		writeError(out, http.StatusRequestEntityTooLarge, fmt.Sprintf("file exceeds the %d MB upload limit", maxUploadBytes>>20))
		return
	}

	purpose := strings.TrimSpace(r.FormValue("purpose"))
	if purpose == "" {
		purpose = "user_data"
	}
	mimeType := strings.TrimSpace(header.Header.Get("Content-Type"))
	if mimeType == "" || mimeType == "application/octet-stream" {
		if byExt := mime.TypeByExtension(strings.ToLower(filepath.Ext(header.Filename))); byExt != "" {
			mimeType = byExt
		}
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	if i := strings.Index(mimeType, ";"); i >= 0 {
		mimeType = strings.TrimSpace(mimeType[:i])
	}

	store := g.filesStore()
	record, err := store.put(header.Filename, purpose, mimeType, data)
	if err != nil {
		writeError(out, http.StatusInternalServerError, "cannot store upload: "+err.Error())
		return
	}
	writeJSON(out, http.StatusOK, fileObject(record))
}

// FilesList implements GET /v1/files.
func (g *Gateway) FilesList(w http.ResponseWriter, r *http.Request) {
	g.filesHandler(w, r, func(out http.ResponseWriter, _ *http.Request, _ *store.ClientKey) {
		records := g.filesStore().list()
		items := make([]any, 0, len(records))
		for _, record := range records {
			items = append(items, fileObject(record))
		}
		writeJSON(out, http.StatusOK, map[string]any{
			"object":   "list",
			"data":     items,
			"has_more": false,
		})
	})
}

// FilesRetrieve implements GET /v1/files/{id}.
func (g *Gateway) FilesRetrieve(w http.ResponseWriter, r *http.Request) {
	g.filesHandler(w, r, func(out http.ResponseWriter, r *http.Request, _ *store.ClientKey) {
		record, ok := g.filesStore().get(r.PathValue("id"))
		if !ok {
			writeError(out, http.StatusNotFound, "no such file")
			return
		}
		writeJSON(out, http.StatusOK, fileObject(record))
	})
}

// FilesContent implements GET /v1/files/{id}/content.
func (g *Gateway) FilesContent(w http.ResponseWriter, r *http.Request) {
	g.filesHandler(w, r, func(out http.ResponseWriter, r *http.Request, _ *store.ClientKey) {
		store := g.filesStore()
		record, ok := store.get(r.PathValue("id"))
		if !ok {
			writeError(out, http.StatusNotFound, "no such file")
			return
		}
		f, err := os.Open(filepath.Join(store.dir, record.ID))
		if err != nil {
			writeError(out, http.StatusInternalServerError, "cannot read stored file")
			return
		}
		defer f.Close()
		out.Header().Set("content-type", record.MimeType)
		out.Header().Set("content-disposition", fmt.Sprintf("attachment; filename=%q", record.Filename))
		out.WriteHeader(http.StatusOK)
		_, _ = io.Copy(out, f)
	})
}

// FilesDelete implements DELETE /v1/files/{id}.
func (g *Gateway) FilesDelete(w http.ResponseWriter, r *http.Request) {
	g.filesHandler(w, r, func(out http.ResponseWriter, r *http.Request, _ *store.ClientKey) {
		id := r.PathValue("id")
		if !g.filesStore().delete(id) {
			writeError(out, http.StatusNotFound, "no such file")
			return
		}
		writeJSON(out, http.StatusOK, map[string]any{"id": id, "object": "file", "deleted": true})
	})
}

// resolveFilePart turns an OpenAI file content part into a turn attachment.
//
// Both OpenAI spellings are accepted: a `file_id` referencing an earlier
// /v1/files upload, or inline `file_data` as a data URL (the PDF-input shape).
func (g *Gateway) resolveFilePart(part contentPart) (minimax.PendingAttachment, error) {
	store := g.filesStore()
	if id := strings.TrimSpace(part.File.FileID); id != "" {
		record, ok := store.get(id)
		if !ok {
			return minimax.PendingAttachment{}, fmt.Errorf("unknown file_id %q", id)
		}
		return store.pending(record)
	}
	data := strings.TrimSpace(part.File.FileData)
	if data == "" {
		return minimax.PendingAttachment{}, errors.New("file content part needs file_id or file_data")
	}
	mimeType, payload, err := decodeDataURL(data)
	if err != nil {
		return minimax.PendingAttachment{}, err
	}
	name := strings.TrimSpace(part.File.Filename)
	if name == "" {
		name = "upload" + extensionFor(mimeType)
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return minimax.PendingAttachment{Name: name, MimeType: mimeType, Size: int64(len(payload)), Data: payload}, nil
}

// decodeDataURL splits `data:<mime>;base64,<payload>`.
func decodeDataURL(raw string) (mimeType string, payload []byte, err error) {
	if !strings.HasPrefix(raw, "data:") {
		return "", nil, errors.New("file_data must be a data URL")
	}
	comma := strings.Index(raw, ",")
	if comma < 0 {
		return "", nil, errors.New("invalid data URL")
	}
	header := raw[len("data:"):comma]
	payload, err = base64.StdEncoding.DecodeString(strings.TrimSpace(raw[comma+1:]))
	if err != nil {
		return "", nil, fmt.Errorf("invalid base64 file payload: %w", err)
	}
	if len(payload) == 0 {
		return "", nil, errors.New("file payload is empty")
	}
	mimeType = header
	for _, piece := range strings.Split(header, ";") {
		piece = strings.TrimSpace(piece)
		if piece != "" && !strings.EqualFold(piece, "base64") {
			mimeType = piece
			break
		}
	}
	return mimeType, payload, nil
}

func extensionFor(mimeType string) string {
	if exts, _ := mime.ExtensionsByType(mimeType); len(exts) > 0 {
		return exts[0]
	}
	return ".bin"
}

func fileObject(record fileRecord) map[string]any {
	return map[string]any{
		"id":         record.ID,
		"object":     "file",
		"bytes":      record.Size,
		"created_at": record.CreatedAt.Unix(),
		"filename":   record.Filename,
		"purpose":    record.Purpose,
		"status":     "processed",
	}
}

// filesStore lazily builds the on-disk registry from the media settings. The
// directory follows the generated-media dir's parent, so it moves when the
// deployment moves.
func (g *Gateway) filesStore() *fileStore {
	g.filesOnce.Do(func() {
		dir := uploadsDir(g.settings().Media.GeneratedDir)
		g.files = newFileStore(dir)
		if err := g.files.load(); err != nil {
			// A broken index still serves chat turns; individual files are
			// recoverable from disk. The error is visible in the log.
			log.Printf("uploads index unreadable (%v); starting empty", err)
		}
	})
	return g.files
}
