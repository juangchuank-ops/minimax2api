package minimax

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// signatureSalt is the static key baked into the MiniMax Agent web bundle.
// It ships with the frontend, so it is public by construction: any browser can
// read it out of the page chunk. Treat it as a protocol constant, not a secret.
const signatureSalt = "I*7Cf%WZ#S&%1RlZJ&C2"

// signatureSuffix is appended to the yy digest input.
const signatureSuffix = "ooui"

// md5Hex returns the lowercase 32-character hex MD5 of value.
func md5Hex(value string) string {
	sum := md5.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}

// XSignature reproduces the upstream `x-signature` header:
//
//	MD5( unix_seconds + salt + raw_request_body )
//
// The digest covers the timestamp, the static salt and the exact body bytes
// that will be sent. It is independent of the URL.
func XSignature(timestamp time.Time, body string) string {
	return md5Hex(strconv.FormatInt(timestamp.Unix(), 10) + signatureSalt + body)
}

// YY reproduces the upstream `yy` header:
//
//	MD5( encodeURIComponent(full_url) + "_" + body + MD5(str(ms)) + "ooui" )
//
// Unlike x-signature this one binds the full request URL (including the client
// metadata query string) and a millisecond timestamp, so it must be computed
// against the same URL that is actually requested.
func YY(fullURL string, body string, timestamp time.Time) string {
	millis := strconv.FormatInt(timestamp.UnixMilli(), 10)
	return md5Hex(encodeURIComponent(fullURL) + "_" + body + md5Hex(millis) + signatureSuffix)
}

// encodeURIComponent mirrors the JavaScript built-in of the same name.
//
// net/url.QueryEscape is not a substitute: it encodes spaces as "+" and leaves
// "~" alone while escaping "!*'()", which is the opposite of what
// encodeURIComponent does for those characters. Any mismatch changes the digest
// and the upstream rejects the request.
func encodeURIComponent(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	for i := 0; i < len(value); i++ {
		char := value[i]
		switch {
		case char >= 'A' && char <= 'Z',
			char >= 'a' && char <= 'z',
			char >= '0' && char <= '9',
			char == '-', char == '_', char == '.', char == '!',
			char == '~', char == '*', char == '\'', char == '(', char == ')':
			builder.WriteByte(char)
		default:
			// Non-ASCII bytes are emitted one at a time, which is exactly how
			// the JS built-in percent-encodes UTF-8 input.
			fmt.Fprintf(&builder, "%%%02X", char)
		}
	}
	return builder.String()
}
