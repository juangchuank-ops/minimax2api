package minimax

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"time"
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
