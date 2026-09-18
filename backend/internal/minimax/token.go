package minimax

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// TokenInfo is what can be learned about an account from its token alone.
type TokenInfo struct {
	UserID    string
	Email     string
	Phone     string
	ExpiresAt time.Time
	// Region is the deployment the credential most likely belongs to, inferred
	// from the phone number's country code. It is a hint for the operator, not
	// a fact: mainland accounts are also created with email addresses, so the
	// console always lets the value be overridden.
	Region string
}

// Identifier returns the best available human-readable account label.
func (t TokenInfo) Identifier() string {
	switch {
	case t.Email != "":
		return t.Email
	case t.Phone != "":
		return t.Phone
	default:
		return t.UserID
	}
}

// ErrNotJWT marks input that is not a decodable JWT.
var ErrNotJWT = errors.New("not a JWT token")

// ParseToken decodes a JWT payload without verifying its signature.
//
// Verification would be pointless: the token is a bearer credential the
// operator pasted in, and only the upstream can decide whether it is still
// live. What the payload is good for is identification — an email or phone
// number lets the console show which account was just added instead of an
// opaque blob, and the phone's country code hints at which deployment the
// account lives on.
func ParseToken(token string) (TokenInfo, error) {
	token = strings.TrimSpace(token)
	// A pasted "realUserID+token" pair is common on the mainland site, where
	// the user id travels separately. Keep the id, keep the token.
	if index := strings.LastIndex(token, "+"); index > 0 {
		token = token[index+1:]
	}
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return TokenInfo{}, ErrNotJWT
	}

	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		// Some producers emit standard base64 instead of the URL-safe variant.
		payload, err = base64.StdEncoding.DecodeString(parts[1])
		if err != nil {
			return TokenInfo{}, ErrNotJWT
		}
	}

	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return TokenInfo{}, ErrNotJWT
	}

	info := TokenInfo{
		UserID: firstClaim(claims, "user_id", "userId", "uid", "sub", "realUserID", "real_user_id"),
		Email:  firstClaim(claims, "email", "mail", "email_address"),
		Phone:  firstClaim(claims, "phone", "mobile", "phone_number", "phoneNumber"),
	}
	if raw, ok := claims["exp"].(float64); ok && raw > 0 {
		info.ExpiresAt = time.Unix(int64(raw), 0)
	}
	info.Region = inferRegion(info.Phone)
	if info.Region == "" {
		info.Region = RegionGlobal
	}
	return info, nil
}

// inferRegion guesses the deployment from a phone number's country code.
func inferRegion(phone string) string {
	digits := strings.TrimPrefix(strings.TrimSpace(phone), "+")
	switch {
	case strings.HasPrefix(digits, "86"):
		return RegionCN
	case digits == "":
		return ""
	default:
		return RegionGlobal
	}
}

// firstClaim returns the first present, non-empty string claim.
func firstClaim(claims map[string]any, keys ...string) string {
	for _, key := range keys {
		switch value := claims[key].(type) {
		case string:
			if value != "" {
				return value
			}
		case float64:
			// Numeric ids are common and lose their exact form as a float64
			// once they exceed 2^53, so they are only used when no string
			// variant exists.
			return strings.TrimSuffix(strings.TrimSuffix(formatFloat(value), ".0"), ".")
		}
	}
	return ""
}

func formatFloat(value float64) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(raw)
}
