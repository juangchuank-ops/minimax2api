package main

// driveprobe reads the documents that describe what an agent turn actually
// produced: a session's summary list, and a drive file's download URL.
//
// Both are free GETs. Neither is on the request path of the gateway, and that is
// the point — the reason a video never came back through the completion endpoint
// could be "the gateway does not know where to look" as easily as "upstream
// produced nothing", and those two look identical from a completion. The summary
// list settles it: if the turn that produced a video is in there, the field that
// points at the file is in there too.
//
// The summaries are a private conversation, so this tool prints *structure*:
// field names, types, array lengths, and identifiers. Message text is reduced to
// its length and never shown. Credentials come from the environment and are never
// printed; download URLs keep their parameter *names* and lose their values,
// because the names are the finding and the values are a bearer token.
//
//	MM_SESSION=<id> go run ./cmd/driveprobe        # map a session document
//	MM_FILE=<id>    go run ./cmd/driveprobe        # resolve a drive file
//
// Credentials: MM_TOKEN, MM_USER_ID, MM_DEVICE_ID, MM_UUID, MM_PROXY.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"minimax2api/internal/config"
	"minimax2api/internal/minimax"
)

// historyLimit is deliberately larger than any history seen so far (~163 kB).
const historyLimit = 8 << 20

func main() {
	proxy := os.Getenv("MM_PROXY")
	cred := minimax.Credential{
		Region:       minimax.RegionGlobal,
		Token:        os.Getenv("MM_TOKEN"),
		UserID:       os.Getenv("MM_USER_ID"),
		DeviceID:     os.Getenv("MM_DEVICE_ID"),
		UUID:         os.Getenv("MM_UUID"),
		ScreenWidth:  1536,
		ScreenHeight: 864,
	}
	if cred.Token == "" || cred.UserID == "" || cred.DeviceID == "" || cred.UUID == "" {
		fmt.Println("credentials missing from the environment")
		os.Exit(2)
	}

	settings := config.DefaultSettings(os.TempDir())
	settings.Upstream.Proxy = proxy
	settings.Upstream.ScreenWidth = 1536
	settings.Upstream.ScreenHeight = 864
	client := minimax.New(func() config.Settings { return settings })

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	switch {
	case os.Getenv("MM_SESSION") != "":
		mapSession(ctx, client, cred, os.Getenv("MM_SESSION"))
	case os.Getenv("MM_FILE") != "":
		resolveFile(ctx, client, cred, os.Getenv("MM_FILE"))
	case os.Getenv("MM_PATHS") != "":
		probePaths(ctx, client, cred, os.Getenv("MM_PATHS"))
	default:
		fmt.Println("set MM_SESSION=<session id>, MM_FILE=<drive file id> or MM_PATHS=<path,path>")
		os.Exit(2)
	}
}

// probePaths reports what a list of free GETs answer, verbatim and with URLs
// masked.
//
// This exists because "the account cannot do X" and "the gateway is asking the
// wrong endpoint" are the same observation until somebody compares two paths.
// A path that only exists on one of the two API surfaces answers just as
// plausibly as a path that exists everywhere and is empty.
func probePaths(ctx context.Context, client *minimax.Client, cred minimax.Credential, list string) {
	for _, path := range strings.Split(list, ",") {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		probe, err := client.FetchRaw(ctx, cred, "GET", path, nil, false, 64<<10)
		fmt.Printf("\n== %s\n", path)
		if err != nil {
			fmt.Printf("   transport error: %v\n", err)
			continue
		}
		fmt.Printf("   status %d, %d bytes, html=%v\n", probe.Status, len(probe.Body), looksLikeHTML(probe.Body))
		fmt.Printf("   body: %s\n", maskSignedURLs(firstLine(probe.Body, 8000)))
	}
}

// looksLikeHTML is the tell that separates "this path does not exist" from "this
// request was understood and answered". The SPA catch-all returns a page, and it
// does so with a 200 often enough that the status alone proves nothing.
func looksLikeHTML(body string) bool {
	lower := strings.ToLower(strings.TrimSpace(body))
	return strings.HasPrefix(lower, "<!doctype") || strings.HasPrefix(lower, "<html") ||
		strings.Contains(lower, "<head>") && strings.Contains(lower, "</html>")
}

func mapSession(ctx context.Context, client *minimax.Client, cred minimax.Credential, sessionID string) {
	// The detail endpoint answers with a few hundred bytes of metadata, so the
	// turn history has to be somewhere else. These are the three sub-resources
	// the web client asks for, in the order it asks for them.
	for _, path := range []string{
		"/minimax-cloud/api/v1/session/" + sessionID,
		"/minimax-cloud/api/v1/session/" + sessionID + "/queue",
		"/minimax-cloud/api/v1/session/" + sessionID + "/input-summaries",
	} {
		fmt.Printf("\n================ %s ================\n", path)
		mapDocument(ctx, client, cred, path)
	}
}

func mapDocument(ctx context.Context, client *minimax.Client, cred minimax.Credential, path string) {
	probe, err := client.FetchRaw(ctx, cred, "GET", path, nil, false, historyLimit)
	if err != nil {
		fmt.Printf("transport error: %v\n", err)
		return
	}
	fmt.Printf("status %d, %d bytes\n", probe.Status, len(probe.Body))
	if probe.Status != 200 {
		fmt.Printf("body: %s\n", firstLine(probe.Body, 300))
		return
	}

	var doc any
	if err := json.Unmarshal([]byte(probe.Body), &doc); err != nil {
		fmt.Printf("not JSON: %v\n", err)
		return
	}

	shapes := map[string]int{}
	collect(doc, "", shapes)

	fmt.Println("\n== 字段形状（路径 → 出现次数）==")
	keys := make([]string, 0, len(shapes))
	for k := range shapes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("  %-60s %d\n", k, shapes[k])
	}

	fmt.Println("\n== 看起来像「产物引用」的值 ==")
	found := map[string]int{}
	artefacts(doc, "", found)
	if len(found) == 0 {
		fmt.Println("  （无：没有 id、也没有媒体路径）")
		return
	}
	keys = keys[:0]
	for k := range found {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("  %-70s ×%d\n", k, found[k])
	}

	dumpArtefacts(doc)
}

// dumpArtefacts prints the artefact arrays verbatim. They are file references,
// not conversation, so there is nothing in them to redact beyond the signed URL
// the drive would hand back.
func dumpArtefacts(doc any) {
	root, ok := doc.(map[string]any)
	if !ok {
		return
	}
	summaries, ok := root["summaries"].([]any)
	if !ok {
		return
	}
	printed := 0
	for index, item := range summaries {
		summary, ok := item.(map[string]any)
		if !ok {
			continue
		}
		raw, present := summary["artifacts"]
		if !present {
			continue
		}
		encoded, err := json.MarshalIndent(raw, "  ", "  ")
		if err != nil {
			continue
		}
		fmt.Printf("\n== summaries[%d].artifacts ==\n  %s\n", index, encoded)
		printed++
	}
	if printed == 0 {
		fmt.Println("\n（没有任何一条 summary 带 artifacts 字段）")
	}
}

func resolveFile(ctx context.Context, client *minimax.Client, cred minimax.Credential, fileID string) {
	for _, path := range []string{
		"/minimax-cloud/api/v1/drive/file/" + fileID,
		"/minimax-cloud/api/v1/drive/file/" + fileID + "/download-url",
	} {
		probe, err := client.FetchRaw(ctx, cred, "GET", path, nil, false, historyLimit)
		if err != nil {
			fmt.Printf("GET %s\n  transport error: %v\n\n", path, err)
			continue
		}
		fmt.Printf("GET %s\n  status %d, %d bytes\n", probe.URL, probe.Status, len(probe.Body))
		fmt.Printf("  body: %s\n\n", maskSignedURLs(firstLine(probe.Body, 400)))
	}
}

// collect records the shape of every field in the document. Strings are reported
// by kind rather than by value: this is somebody's conversation.
func collect(node any, path string, out map[string]int) {
	switch value := node.(type) {
	case map[string]any:
		for key, child := range value {
			collect(child, path+"."+key, out)
		}
	case []any:
		out[path+"[]"]++
		if len(value) == 0 {
			return
		}
		collect(value[0], path+"[]", out)
	case string:
		out[path+" (string:"+stringKind(value)+")"]++
	case float64:
		out[path+" (number)"]++
	case bool:
		out[path+" (bool)"]++
	case nil:
		out[path+" (null)"]++
	default:
		out[path+" (?)"]++
	}
}

func stringKind(value string) string {
	switch {
	case value == "":
		return "empty"
	case isDigits(value):
		return fmt.Sprintf("%d digits", len(value))
	case looksLikeURL(value):
		return "url"
	case len(value) > 120:
		return fmt.Sprintf("%d chars", len(value))
	default:
		return "short"
	}
}

// artefacts is the actual question: which field points at something produced.
func artefacts(node any, path string, out map[string]int) {
	switch value := node.(type) {
	case map[string]any:
		for key, child := range value {
			artefacts(child, path+"."+key, out)
		}
	case []any:
		for _, child := range value {
			artefacts(child, path+"[]", out)
		}
	case string:
		switch {
		case isDigits(value) && len(value) >= 12:
			out[path+" = <"+fmt.Sprintf("%d 位数字", len(value))+">"]++
		case looksLikeURL(value) && (strings.Contains(value, "oss") || hasMediaSuffix(value)):
			out[path+" = <媒体 URL "+urlHostPath(value)+">"]++
		}
	}
}

func hasMediaSuffix(value string) bool {
	lower := strings.ToLower(value)
	for _, suffix := range []string{".mp4", ".mov", ".webm", ".png", ".jpg", ".jpeg"} {
		if strings.Contains(lower, suffix) {
			return true
		}
	}
	return false
}

func urlHostPath(raw string) string {
	if index := strings.IndexByte(raw, '?'); index >= 0 {
		raw = raw[:index]
	}
	raw = strings.TrimPrefix(raw, "https://")
	raw = strings.TrimPrefix(raw, "http://")
	return maskAccountIDs(raw)
}

// maskAccountIDs replaces the account's own numeric identifiers inside a URL.
// They are not secrets, but they are account handles, and a diagnostic has no
// reason to put one on screen.
func maskAccountIDs(text string) string {
	return idRun.ReplaceAllStringFunc(text, func(run string) string {
		return "<" + fmt.Sprintf("%d", len(run)) + "位数字>"
	})
}

var idRun = regexp.MustCompile(`\b[0-9]{12,}\b`)

// maskSignedURLs keeps the *shape* of every URL in the text — host, path, and
// the names of the query parameters — while blanking the values. A download URL
// is a bearer token with an expiry on it: which parameters it carries is the
// finding; what they say is not.
func maskSignedURLs(text string) string {
	var out strings.Builder
	for {
		start := strings.Index(text, "http")
		if start < 0 {
			out.WriteString(maskAccountIDs(text))
			return out.String()
		}
		out.WriteString(maskAccountIDs(text[:start]))
		text = text[start:]
		end := strings.IndexAny(text, `" '`)
		if end < 0 {
			end = len(text)
		}
		out.WriteString(maskedURL(text[:end]))
		text = text[end:]
	}
}

func maskedURL(raw string) string {
	path := raw
	query := ""
	if index := strings.IndexByte(raw, '?'); index >= 0 {
		path, query = raw[:index], raw[index+1:]
	}
	path = strings.TrimPrefix(strings.TrimPrefix(path, "https://"), "http://")
	path = maskAccountIDs(path)
	if query == "" {
		return path
	}
	names := make([]string, 0, 4)
	for _, pair := range strings.Split(query, "&") {
		name, _, _ := strings.Cut(pair, "=")
		names = append(names, name+"=…")
	}
	return path + "?" + strings.Join(names, "&")
}

func firstLine(text string, limit int) string {
	collapsed := strings.Join(strings.Fields(text), " ")
	if len(collapsed) > limit {
		return collapsed[:limit] + "…"
	}
	return collapsed
}

func isDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func looksLikeURL(value string) bool {
	return strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://")
}
