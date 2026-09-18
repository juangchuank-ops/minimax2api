package minimax

import (
	"context"
	"net/http"
	"strings"
)

// DefaultUserInfoPath reports the signed-in account's identity.
const DefaultUserInfoPath = "/v1/api/user/info"

// UserInfo is the identity behind a token.
type UserInfo struct {
	// RealUserID is the number every other endpoint wants as `user_id`. It is
	// NOT the JWT's user.id, and it cannot be derived from the token at all.
	RealUserID string
	Name       string
	// ShortID is the opaque per-deployment handle the site puts in URLs, such
	// as the `userID` field in localStorage. Not an authentication value.
	ShortID string
	Phone   string
	Email   string
}

// Label is the best human-readable name for the account.
func (u UserInfo) Label() string {
	switch {
	case u.Name != "":
		return u.Name
	case u.Email != "":
		return u.Email
	case u.Phone != "":
		return u.Phone
	default:
		return u.RealUserID
	}
}

// FetchUserInfo reads the account's identity from the upstream.
//
// This is the only way to learn realUserID. It is not in the JWT — the token
// carries a *different* id under user.id, and feeding that one back as user_id
// is rejected just like omitting it — and the site keeps the real value in
// localStorage, which a server-side client cannot read.
//
// It matters because every signed endpoint requires `user_id` in the query and
// answers a bare 401 without it. A 401 with an empty body is indistinguishable
// from a dead token, so an account that never learns its realUserID gets retired
// as invalid while being perfectly healthy. This call is what prevents that.
//
// The endpoint is reachable *without* user_id, which is exactly what makes it
// usable as the bootstrap: signinParams renders the missing value as "0", which
// is accepted here and rejected everywhere else.
func (c *Client) FetchUserInfo(ctx context.Context, cred Credential) (*UserInfo, error) {
	settings := c.settings()
	ctx, cancel := context.WithTimeout(ctx, signinTimeout(settings))
	defer cancel()

	path := strings.TrimSpace(settings.Upstream.UserInfoPath)
	if path == "" {
		path = DefaultUserInfoPath
	}
	payload, err := c.callSignin(ctx, settings, cred, http.MethodGet, path, "")
	if err != nil {
		return nil, err
	}
	core := coreOf(payload)
	// The identity sits one level deeper here than on the other endpoints.
	if inner, ok := core["userInfo"].(map[string]any); ok {
		core = inner
	}
	return &UserInfo{
		RealUserID: scalarOf(core["realUserID"]),
		Name:       stringOf(core["name"]),
		ShortID:    stringOf(core["userID"]),
		Phone:      stringOf(core["phone"]),
		Email:      stringOf(core["email"]),
	}, nil
}

// scalarOf reads a value that may arrive as a string or a number.
//
// The upstream sends realUserID as a string, and it has to: the number is past
// 2^53, so a JSON number would be silently rounded on the way into a float64.
// The numeric branch exists for other endpoints, not because this one needs it.
func scalarOf(value any) string {
	switch node := value.(type) {
	case string:
		return strings.TrimSpace(node)
	case float64:
		return strings.TrimSuffix(formatFloat(node), ".0")
	default:
		return ""
	}
}
