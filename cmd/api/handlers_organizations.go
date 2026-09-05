package api

import (
	"net/http"
	"strings"

	"github.com/darkoatanasovski/chat/internal/quota"
)

// validTiers gates both org creation here and (further down the chain) is
// reused nowhere else — an App's tier is never chosen directly, it's always
// inherited live from its Organization (internal/apps.TierResolver).
var validTiers = map[string]bool{
	quota.TierFree:       true,
	quota.TierPro:        true,
	quota.TierBusiness:   true,
	quota.TierEnterprise: true,
}

type createOrgRequest struct {
	Name string `json:"name"`
	Tier string `json:"tier"`
}

type orgResponse struct {
	OrgID int64  `json:"org_id"`
	Name  string `json:"name"`
	Tier  string `json:"tier"`
	Token string `json:"token"`
}

// handleCreateOrg is the top of the B2B trust chain — public/unauthenticated
// like POST /users, same dev-grade "mint identity immediately, no password"
// model (docs/platform/security.md), one level up: this mints the
// org-admin token used to create and manage that org's Apps and their API
// credentials.
func (a *App) handleCreateOrg(w http.ResponseWriter, r *http.Request) {
	var req createOrgRequest
	if !readJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 128 {
		writeError(w, http.StatusBadRequest, "name is required (max 128 chars)")
		return
	}
	req.Tier = strings.ToUpper(strings.TrimSpace(req.Tier))
	if req.Tier == "" {
		req.Tier = quota.TierFree
	}
	if !validTiers[req.Tier] {
		writeError(w, http.StatusBadRequest, "tier must be one of FREE, PRO, BUSINESS, ENTERPRISE")
		return
	}

	org, err := a.orgsSvc.CreateOrg(r.Context(), req.Name, req.Tier)
	if err != nil {
		a.log.Error("create organization", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create organization")
		return
	}

	token, err := a.signer.IssueOrgAdminToken(org.OrgID, tokenTTL)
	if err != nil {
		a.log.Error("issue org token", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to issue token")
		return
	}

	writeJSON(w, http.StatusCreated, orgResponse{
		OrgID: org.OrgID,
		Name:  org.Name,
		Tier:  org.Tier,
		Token: token,
	})
}
