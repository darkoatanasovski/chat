package main

import (
	"net/http"
	"strings"
	"time"
)

const tokenTTL = 24 * time.Hour

var validRegions = map[string]bool{"eu": true, "us": true, "asia": true}

type createUserRequest struct {
	DisplayName string `json:"display_name"`
	Region      string `json:"region"`
}

type createUserResponse struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	Region      string `json:"region"`
	Tier        string `json:"tier"`
	Token       string `json:"token"`
}

// handleCreateUser mints an end-user identity within the App identified by
// the caller's API credentials (requireAppCredentials) — this is the
// endpoint a business's own backend calls on behalf of its own end-users,
// never something an end-user presents credentials to directly. There is
// still no password for the end-user themselves — see
// docs/platform/security.md for why that's acceptable for a V1 test
// platform's end-user layer specifically, distinct from the App
// credentials gating this endpoint, which *are* real, revocable secrets.
func (a *App) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	appIdentity, _ := appIdentityFromContext(r.Context())

	var req createUserRequest
	if !readJSON(w, r, &req) {
		return
	}
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	req.Region = strings.ToLower(strings.TrimSpace(req.Region))
	if req.DisplayName == "" || len(req.DisplayName) > 128 {
		writeError(w, http.StatusBadRequest, "display_name is required (max 128 chars)")
		return
	}
	if !validRegions[req.Region] {
		writeError(w, http.StatusBadRequest, "region must be one of eu, us, asia")
		return
	}

	u, err := a.usersSvc.CreateUser(r.Context(), req.DisplayName, req.Region, appIdentity.AppID)
	if err != nil {
		a.log.Error("create user", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create user")
		return
	}

	tier, err := a.appTiers.TierForApp(r.Context(), appIdentity.AppID)
	if err != nil {
		a.log.Error("resolve app tier", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to resolve tier")
		return
	}

	token, err := a.signer.IssueUserToken(u.UserID.String(), u.HomeRegion, u.AppID, tokenTTL)
	if err != nil {
		a.log.Error("issue token", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to issue token")
		return
	}

	writeJSON(w, http.StatusCreated, createUserResponse{
		UserID:      u.UserID.String(),
		DisplayName: u.DisplayName,
		Region:      u.HomeRegion,
		Tier:        tier,
		Token:       token,
	})
}
