package minimax

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"minimax2api/internal/config"
)

// randomUUID returns a RFC 4122 v4 UUID string.
func randomUUID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		now := time.Now().UnixNano()
		return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", now>>32, (now>>16)&0xffff, (now)&0xffff, now&0xffff, now)
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(buf[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}

// parseProxy accepts the proxy URL configured in the console.
func parseProxy(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy %q: %w", raw, err)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("invalid proxy %q: missing host", raw)
	}
	return parsed, nil
}

// DownloadClient returns an HTTP client for fetching generated media.
//
// Generated images and videos are served from MiniMax's CDN, and the same
// egress fence that applies to the API applies to it: an account is only usable
// from an overseas IP, so a download attempted from the local one fails — and it
// fails as a timeout or a reset, which reads as the CDN being down rather than
// as the request having gone out the wrong door. The console's proxy is
// therefore used here too, not just for API calls.
//
// Loopback is exempt for the same reason it is exempt on the API path: a proxy
// cannot reach 127.0.0.1, and sending it there turns a local mirror into an
// empty-bodied 502.
func DownloadClient(settings config.Settings) *http.Client {
	transport := &http.Transport{
		MaxIdleConns:        16,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     90 * time.Second,
	}
	if raw := strings.TrimSpace(settings.Upstream.Proxy); raw != "" {
		if proxyURL, err := parseProxy(raw); err == nil {
			transport.Proxy = func(target *http.Request) (*url.URL, error) {
				if isLoopbackHost(target.URL.Hostname()) {
					return nil, nil
				}
				return proxyURL, nil
			}
		}
	}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 5 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
}
