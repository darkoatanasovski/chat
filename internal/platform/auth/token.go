// Package auth issues and verifies the dev-grade bearer tokens used across
// this platform. There is no password/login flow: POST /users and
// POST /organizations mint a token at creation time. This is explicitly NOT
// production-grade identity (see docs/platform/security.md) — it exists so
// every other rule in INSTRUCTIONS.md §43 ("never trust user_id/tier/region
// from the client") can be enforced without building a full IdP for a V1
// test platform.
//
// Token shape: base64url(json claims) + "." + base64url(HMAC-SHA256 signature).
//
// Claim shapes share this one signer, distinguished by Type: an end-user
// token (sub=user_id, region, app_id), an org-admin token (sub=org_id) used
// only to manage that org's Apps and their API credentials, and an app
// token (sub=app_id, app_id, api_key) that a business's backend exchanges
// its App's key+secret for (POST /apps/token) so it can authenticate
// POST /users with a short-lived Bearer token instead of sending the raw
// secret on every call — see cmd/api's requireAppJWT. Revocation still
// takes effect immediately despite being a signed token: api_key is
// re-checked live against Postgres on every requireAppJWT call
// (apps.CredentialRepo.IsActive), the same guarantee Basic auth's live
// Verify already gave. Neither user nor app tokens carry a tier — tier is
// resolved live from app_id -> organizations.tier (internal/apps.TierResolver)
// so a plan change takes effect immediately, not after a token's next
// reissue.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

type ClaimsType string

const (
	ClaimsTypeUser     ClaimsType = "user"
	ClaimsTypeOrgAdmin ClaimsType = "org_admin"
	// ClaimsTypeOrgUser is a real per-person dashboard session (sub=user_id
	// into internal/orgusers), distinct from ClaimsTypeOrgAdmin's one
	// shared, unattributed org-wide credential. Neither org_id nor role is
	// carried here — both are resolved live from user_id on every request
	// (orgusers.Repo.GetByID), same reasoning as AppID never carrying tier.
	ClaimsTypeOrgUser ClaimsType = "org_user"
	// ClaimsTypeApp is minted by POST /apps/token (requireAppCredentials,
	// HTTP Basic) and verified by requireAppJWT. See the package doc above
	// for why api_key still gets a live Postgres check on every use.
	ClaimsTypeApp ClaimsType = "app"
)

type Claims struct {
	Subject string     `json:"sub"` // user_id (Type=user), org_id (Type=org_admin), or app_id (Type=app)
	Type    ClaimsType `json:"typ"`
	Region  string     `json:"region,omitempty"` // user tokens only
	AppID   int64      `json:"app_id,omitempty"` // user and app tokens only
	// APIKey identifies which app_credentials row this app token was
	// exchanged from — app tokens only. Never the secret itself, and not
	// sufficient on its own to authenticate anything: requireAppJWT still
	// re-checks this key isn't revoked on every request (see the package
	// doc comment above).
	APIKey string `json:"api_key,omitempty"`
	Exp    int64  `json:"exp"`
}

type Signer struct {
	secret []byte
}

func NewSigner(secret string) *Signer {
	return &Signer{secret: []byte(secret)}
}

func (s *Signer) IssueUserToken(userID, region string, appID int64, ttl time.Duration) (string, error) {
	return s.issue(Claims{
		Subject: userID,
		Type:    ClaimsTypeUser,
		Region:  region,
		AppID:   appID,
		Exp:     time.Now().Add(ttl).Unix(),
	})
}

func (s *Signer) IssueOrgAdminToken(orgID int64, ttl time.Duration) (string, error) {
	return s.issue(Claims{
		Subject: strconv.FormatInt(orgID, 10),
		Type:    ClaimsTypeOrgAdmin,
		Exp:     time.Now().Add(ttl).Unix(),
	})
}

func (s *Signer) IssueOrgUserToken(userID string, ttl time.Duration) (string, error) {
	return s.issue(Claims{
		Subject: userID,
		Type:    ClaimsTypeOrgUser,
		Exp:     time.Now().Add(ttl).Unix(),
	})
}

// IssueAppToken mints the Bearer token POST /apps/token hands back after
// verifying an App's key+secret — apiKey is the credential's key (its
// public half), carried so requireAppJWT can re-check live that this
// specific credential is still active without needing the secret again.
func (s *Signer) IssueAppToken(appID int64, apiKey string, ttl time.Duration) (string, error) {
	return s.issue(Claims{
		Subject: strconv.FormatInt(appID, 10),
		Type:    ClaimsTypeApp,
		AppID:   appID,
		APIKey:  apiKey,
		Exp:     time.Now().Add(ttl).Unix(),
	})
}

func (s *Signer) issue(claims Claims) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("auth: marshal claims: %w", err)
	}
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	sig := s.sign(encodedPayload)
	return encodedPayload + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func (s *Signer) Verify(token string) (Claims, error) {
	var claims Claims

	dot := -1
	for i := len(token) - 1; i >= 0; i-- {
		if token[i] == '.' {
			dot = i
			break
		}
	}
	if dot < 0 {
		return claims, fmt.Errorf("auth: malformed token")
	}
	encodedPayload, encodedSig := token[:dot], token[dot+1:]

	sig, err := base64.RawURLEncoding.DecodeString(encodedSig)
	if err != nil {
		return claims, fmt.Errorf("auth: malformed signature")
	}
	expected := s.sign(encodedPayload)
	if subtle.ConstantTimeCompare(sig, expected) != 1 {
		return claims, fmt.Errorf("auth: invalid signature")
	}

	payload, err := base64.RawURLEncoding.DecodeString(encodedPayload)
	if err != nil {
		return claims, fmt.Errorf("auth: malformed payload")
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return claims, fmt.Errorf("auth: malformed claims")
	}
	if time.Now().Unix() > claims.Exp {
		return claims, fmt.Errorf("auth: token expired")
	}
	return claims, nil
}

func (s *Signer) sign(encodedPayload string) []byte {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(encodedPayload))
	return mac.Sum(nil)
}
