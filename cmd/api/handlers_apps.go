package main

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/darkoatanasovski/chat/internal/apps"
	"github.com/darkoatanasovski/chat/internal/quota"
)

type createAppRequest struct {
	Name string `json:"name"`
}

type appResponse struct {
	AppID     int64  `json:"app_id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
	// MaxThreadDepth: 0 means no cap. See apps.App.MaxThreadDepth.
	MaxThreadDepth int `json:"max_thread_depth"`
	// MessageEditEnabled: whether this app's end-users may edit their own
	// messages (PATCH /channels/{id}/messages/{message_id}). See
	// apps.App.MessageEditEnabled.
	MessageEditEnabled bool `json:"message_edit_enabled"`
}

func appResponseFrom(app apps.App) appResponse {
	return appResponse{
		AppID:              app.AppID,
		Name:               app.Name,
		CreatedAt:          app.CreatedAt.Format(rfc3339Milli),
		MaxThreadDepth:     app.MaxThreadDepth,
		MessageEditEnabled: app.MessageEditEnabled,
	}
}

// credentialResponse's Secret/RevokedAt are only ever populated where they
// apply: Secret exists only on the single response from creation (never
// retrievable again after), RevokedAt only once a credential is revoked.
type credentialResponse struct {
	CredentialID string  `json:"credential_id"`
	Key          string  `json:"key"`
	Secret       string  `json:"secret,omitempty"`
	CreatedAt    string  `json:"created_at"`
	RevokedAt    *string `json:"revoked_at,omitempty"`
}

type createAppResponse struct {
	appResponse
	Credential credentialResponse `json:"credential"`
}

// revealCredentialResponse backs GET .../credentials/{credential_id}/reveal
// — deliberately a separate, narrower shape from credentialResponse (no
// key/created_at/revoked_at) since a reveal call already implies the
// caller has those from the list it's revealing out of.
type revealCredentialResponse struct {
	Secret string `json:"secret"`
}

// handleCreateApp backs POST /organizations/{org_id}/apps: org-admin
// authenticated, enforces max_apps, and mints the app's first credential
// immediately — the same "create + credential in one call" convenience
// POST /users already gives end-users.
func (a *App) handleCreateApp(w http.ResponseWriter, r *http.Request) {
	orgIdentity, ok := a.requireOwnedOrgPath(w, r)
	if !ok {
		return
	}

	var req createAppRequest
	if !readJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 128 {
		writeError(w, http.StatusBadRequest, "name is required (max 128 chars)")
		return
	}

	org, err := a.orgsRepo.Get(r.Context(), orgIdentity.OrgID)
	if err != nil {
		a.log.Error("load organization", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check quota")
		return
	}

	currentCount, err := a.appsRepo.CountByOrg(r.Context(), orgIdentity.OrgID)
	if err != nil {
		a.log.Error("count apps", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check quota")
		return
	}
	decision, err := a.quota.AllowResource(org.Tier, quota.CapabilityAppCreate, currentCount)
	if err != nil {
		a.log.Error("quota check", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check quota")
		return
	}
	if !decision.Allowed {
		a.metrics.QuotaRejectionsTotal.WithLabelValues(quota.CapabilityAppCreate).Inc()
		writeError(w, http.StatusTooManyRequests, decision.Reason)
		return
	}

	newApp, err := a.appsRepo.Create(r.Context(), orgIdentity.OrgID, req.Name)
	if err != nil {
		a.log.Error("create app", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create app")
		return
	}

	issued, err := a.appCredentials.Create(r.Context(), newApp.AppID)
	if err != nil {
		a.log.Error("create app credential", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create app credential")
		return
	}

	writeJSON(w, http.StatusCreated, createAppResponse{
		appResponse: appResponseFrom(newApp),
		Credential: credentialResponse{
			CredentialID: issued.CredentialID.String(),
			Key:          issued.Key,
			Secret:       issued.Secret,
			CreatedAt:    issued.CreatedAt.Format(rfc3339Milli),
		},
	})
}

// handleListApps backs GET /organizations/{org_id}/apps.
func (a *App) handleListApps(w http.ResponseWriter, r *http.Request) {
	orgIdentity, ok := a.requireOwnedOrgPath(w, r)
	if !ok {
		return
	}

	list, err := a.appsRepo.ListByOrg(r.Context(), orgIdentity.OrgID)
	if err != nil {
		a.log.Error("list apps", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list apps")
		return
	}

	out := make([]appResponse, len(list))
	for i, app := range list {
		out[i] = appResponseFrom(app)
	}
	writeJSON(w, http.StatusOK, out)
}

// updateAppRequest is PATCH /apps/{app_id}'s body — a genuine partial
// update now that there are two independently settable fields
// (max_thread_depth, message_edit_enabled): each is a *pointer (nil = "not
// being changed"), at least one of which must be present. A future
// settable field follows the same shape — its own *pointer field here,
// not a move to PUT semantics.
type updateAppRequest struct {
	MaxThreadDepth     *int  `json:"max_thread_depth"`
	MessageEditEnabled *bool `json:"message_edit_enabled"`
}

// handleUpdateApp backs PATCH /apps/{app_id}. Whichever of
// updateAppRequest's fields are present get applied together in one
// UpdateSettings call; anything omitted is left exactly as it was. Both
// settings take effect on the very next request that reads them (a reply's
// thread-depth check, an edit's enabled check) since neither is ever cached
// server-side — a "why didn't my change take effect" support question isn't
// worth the read either would save.
func (a *App) handleUpdateApp(w http.ResponseWriter, r *http.Request) {
	orgIdentity, _ := orgIdentityFromContext(r.Context())
	app, ok := a.requireOwnedApp(w, r, orgIdentity.OrgID)
	if !ok {
		return
	}

	var req updateAppRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.MaxThreadDepth == nil && req.MessageEditEnabled == nil {
		writeError(w, http.StatusBadRequest, "at least one of max_thread_depth or message_edit_enabled is required")
		return
	}
	if req.MaxThreadDepth != nil && *req.MaxThreadDepth < 0 {
		writeError(w, http.StatusBadRequest, "max_thread_depth must be >= 0 (0 means unlimited)")
		return
	}

	updated, err := a.appsRepo.UpdateSettings(r.Context(), app.AppID, req.MaxThreadDepth, req.MessageEditEnabled)
	if err != nil {
		if errors.Is(err, apps.ErrNotFound) {
			writeError(w, http.StatusNotFound, "app not found")
			return
		}
		a.log.Error("update app", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to update app")
		return
	}

	writeJSON(w, http.StatusOK, appResponseFrom(updated))
}

// requireOwnedOrgPath checks the {org_id} path segment matches the
// authenticated org-admin token — without this, an org admin could act on
// a *different* org's resources just by editing the URL, since the token
// alone doesn't constrain which path it's presented against.
func (a *App) requireOwnedOrgPath(w http.ResponseWriter, r *http.Request) (OrgIdentity, bool) {
	orgIdentity, _ := orgIdentityFromContext(r.Context())
	pathOrgID, err := strconv.ParseInt(r.PathValue("org_id"), 10, 64)
	if err != nil || pathOrgID != orgIdentity.OrgID {
		writeError(w, http.StatusForbidden, "not authorized for this organization")
		return OrgIdentity{}, false
	}
	return orgIdentity, true
}

// requireOwnedApp resolves {app_id} from the path and verifies it belongs
// to the authenticated org — shared by every /apps/{app_id}/credentials route.
func (a *App) requireOwnedApp(w http.ResponseWriter, r *http.Request, orgID int64) (apps.App, bool) {
	appID, err := strconv.ParseInt(r.PathValue("app_id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid app id")
		return apps.App{}, false
	}
	app, err := a.appsRepo.Get(r.Context(), appID)
	if err != nil {
		if errors.Is(err, apps.ErrNotFound) {
			writeError(w, http.StatusNotFound, "app not found")
			return apps.App{}, false
		}
		a.log.Error("load app", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load app")
		return apps.App{}, false
	}
	if app.OrgID != orgID {
		writeError(w, http.StatusForbidden, "not authorized for this app")
		return apps.App{}, false
	}
	return app, true
}

// handleCreateAppCredential backs POST /apps/{app_id}/credentials.
func (a *App) handleCreateAppCredential(w http.ResponseWriter, r *http.Request) {
	orgIdentity, _ := orgIdentityFromContext(r.Context())
	app, ok := a.requireOwnedApp(w, r, orgIdentity.OrgID)
	if !ok {
		return
	}

	issued, err := a.appCredentials.Create(r.Context(), app.AppID)
	if err != nil {
		a.log.Error("create app credential", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to create app credential")
		return
	}

	writeJSON(w, http.StatusCreated, credentialResponse{
		CredentialID: issued.CredentialID.String(),
		Key:          issued.Key,
		Secret:       issued.Secret,
		CreatedAt:    issued.CreatedAt.Format(rfc3339Milli),
	})
}

// handleListAppCredentials backs GET /apps/{app_id}/credentials — never
// returns a secret or its hash, only what's needed to tell credentials
// apart (key, created_at, revoked_at).
func (a *App) handleListAppCredentials(w http.ResponseWriter, r *http.Request) {
	orgIdentity, _ := orgIdentityFromContext(r.Context())
	app, ok := a.requireOwnedApp(w, r, orgIdentity.OrgID)
	if !ok {
		return
	}

	list, err := a.appCredentials.ListByApp(r.Context(), app.AppID)
	if err != nil {
		a.log.Error("list app credentials", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list credentials")
		return
	}

	out := make([]credentialResponse, len(list))
	for i, c := range list {
		cr := credentialResponse{CredentialID: c.CredentialID.String(), Key: c.Key, CreatedAt: c.CreatedAt.Format(rfc3339Milli)}
		if c.RevokedAt != nil {
			s := c.RevokedAt.Format(rfc3339Milli)
			cr.RevokedAt = &s
		}
		out[i] = cr
	}
	writeJSON(w, http.StatusOK, out)
}

// handleRevealAppCredential backs GET /apps/{app_id}/credentials/{credential_id}/reveal
// — decrypts and returns a credential's secret on demand (CredentialRepo.Reveal),
// for the case the modal shown right after creation (which already has the
// secret in hand) is long gone: closed, page refreshed, a different day
// entirely. Same org-auth + ownership scoping as every other credentials
// route, since this is exactly as sensitive as creating one.
func (a *App) handleRevealAppCredential(w http.ResponseWriter, r *http.Request) {
	orgIdentity, _ := orgIdentityFromContext(r.Context())
	app, ok := a.requireOwnedApp(w, r, orgIdentity.OrgID)
	if !ok {
		return
	}

	credentialID, err := uuid.Parse(r.PathValue("credential_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid credential id")
		return
	}

	secret, err := a.appCredentials.Reveal(r.Context(), app.AppID, credentialID)
	if err != nil {
		if errors.Is(err, apps.ErrCredentialNotFound) {
			writeError(w, http.StatusNotFound, "credential not found or has no recoverable secret")
			return
		}
		a.log.Error("reveal app credential", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to reveal credential")
		return
	}

	writeJSON(w, http.StatusOK, revealCredentialResponse{Secret: secret})
}

// handleRevokeAppCredential backs DELETE /apps/{app_id}/credentials/{credential_id}.
// Revocation is checked live on every subsequent use (apps.CredentialRepo.Verify),
// not baked into a token, so it takes effect immediately — the whole point
// of "revoke API creds."
func (a *App) handleRevokeAppCredential(w http.ResponseWriter, r *http.Request) {
	orgIdentity, _ := orgIdentityFromContext(r.Context())
	app, ok := a.requireOwnedApp(w, r, orgIdentity.OrgID)
	if !ok {
		return
	}

	credentialID, err := uuid.Parse(r.PathValue("credential_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid credential id")
		return
	}

	if err := a.appCredentials.Revoke(r.Context(), app.AppID, credentialID); err != nil {
		if errors.Is(err, apps.ErrCredentialNotFound) {
			writeError(w, http.StatusNotFound, "credential not found")
			return
		}
		a.log.Error("revoke app credential", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to revoke credential")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
