package main

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/darkoatanasovski/chat/internal/orgusers"
	"github.com/darkoatanasovski/chat/internal/platform/auth"
	"github.com/darkoatanasovski/chat/internal/platform/tracing"
)

type ctxKey int

const (
	identityKey ctxKey = iota
	orgIdentityKey
	appIdentityKey
	orgUserIdentityKey
)

// Identity is an authenticated end-user, derived only from a verified
// token — INSTRUCTIONS.md §43: never trust user_id/tier/region/app_id
// asserted by the client directly. There is deliberately no Tier field:
// tier is resolved live from AppID (internal/apps.TierResolver) at the
// point a check needs it, not trusted from a possibly-stale token claim.
type Identity struct {
	UserID uuid.UUID
	Region string
	AppID  int64
}

func identityFromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey).(Identity)
	return id, ok
}

// OrgIdentity is an authenticated organization admin — the identity behind
// managing that org's Apps and their API credentials. Distinct from
// Identity (end-users) so a user token can never be replayed against
// org-management endpoints and vice versa.
type OrgIdentity struct {
	OrgID int64
}

func orgIdentityFromContext(ctx context.Context) (OrgIdentity, bool) {
	id, ok := ctx.Value(orgIdentityKey).(OrgIdentity)
	return id, ok
}

// OrgUserIdentity is an authenticated dashboard person, present only when
// the request came in on a real per-person session (ClaimsTypeOrgUser) —
// never on the org-admin token, which has no person to attribute anything
// to. Role is resolved live (see requireOrgAuth), never trusted from the
// token, matching every other mutable-authorization value in this codebase.
type OrgUserIdentity struct {
	UserID uuid.UUID
	Role   string
}

func orgUserIdentityFromContext(ctx context.Context) (OrgUserIdentity, bool) {
	id, ok := ctx.Value(orgUserIdentityKey).(OrgUserIdentity)
	return id, ok
}

// AppIdentity is an authenticated App — either verified directly via API
// key/secret (requireAppCredentials, POST /apps/token only) or via the
// Bearer JWT that credential exchange mints (requireAppJWT, POST /users).
// APIKey is set by both paths — requireAppJWT needs it to look up the live
// revocation check on every request; requireAppCredentials sets it so
// handleCreateAppToken doesn't need to re-parse the Basic auth header.
type AppIdentity struct {
	AppID  int64
	APIKey string
}

func appIdentityFromContext(ctx context.Context) (AppIdentity, bool) {
	id, ok := ctx.Value(appIdentityKey).(AppIdentity)
	return id, ok
}

// requireAuth verifies an end-user bearer token and injects Identity into
// the request context.
func (a *App) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			writeError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		claims, err := a.signer.Verify(token)
		if err != nil || claims.Type != auth.ClaimsTypeUser {
			writeError(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		userID, err := uuid.Parse(claims.Subject)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid token subject")
			return
		}
		ctx := context.WithValue(r.Context(), identityKey, Identity{
			UserID: userID, Region: claims.Region, AppID: claims.AppID,
		})
		next(w, r.WithContext(ctx))
	}
}

// requireOrgAuth accepts either an org-admin bearer token (one shared,
// unattributed credential for the whole org — programmatic/automation use,
// e.g. tools/loadtest) or a real per-person dashboard session
// (ClaimsTypeOrgUser), and injects OrgIdentity either way. This is what lets
// every existing /organizations and /apps management handler keep working
// unchanged for both the automation path and the new dashboard: they only
// ever read OrgIdentity from context, never which auth path produced it.
// A dashboard session additionally gets OrgUserIdentity injected (role,
// resolved live — never trusted from the token) for the handlers that need
// to know not just *which org* but *which person and what role*.
func (a *App) requireOrgAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			writeError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		claims, err := a.signer.Verify(token)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		switch claims.Type {
		case auth.ClaimsTypeOrgAdmin:
			orgID, err := strconv.ParseInt(claims.Subject, 10, 64)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "invalid token subject")
				return
			}
			ctx := context.WithValue(r.Context(), orgIdentityKey, OrgIdentity{OrgID: orgID})
			next(w, r.WithContext(ctx))
		case auth.ClaimsTypeOrgUser:
			userID, err := uuid.Parse(claims.Subject)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "invalid token subject")
				return
			}
			u, err := a.orgUsersRepo.GetByID(r.Context(), userID)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "invalid or expired token")
				return
			}
			ctx := context.WithValue(r.Context(), orgIdentityKey, OrgIdentity{OrgID: u.OrgID})
			ctx = context.WithValue(ctx, orgUserIdentityKey, OrgUserIdentity{UserID: u.UserID, Role: u.Role})
			next(w, r.WithContext(ctx))
		default:
			writeError(w, http.StatusUnauthorized, "invalid or expired token")
		}
	}
}

// requireOrgUser is requireOrgAuth restricted to real per-person sessions —
// used by dashboard endpoints where "which person" is meaningful (team
// management, "who am I"), so the org-admin token (no person behind it) is
// rejected here instead of silently being accepted.
func (a *App) requireOrgUser(next http.HandlerFunc) http.HandlerFunc {
	return a.requireOrgAuth(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := orgUserIdentityFromContext(r.Context()); !ok {
			writeError(w, http.StatusForbidden, "requires a dashboard account, not an app-level org token")
			return
		}
		next(w, r)
	})
}

// requireOwnerRole further restricts requireOrgUser to the "owner" role —
// inviting/removing teammates and other org-level settings a plain member
// shouldn't be able to do.
func (a *App) requireOwnerRole(next http.HandlerFunc) http.HandlerFunc {
	return a.requireOrgUser(func(w http.ResponseWriter, r *http.Request) {
		orgUser, _ := orgUserIdentityFromContext(r.Context())
		if orgUser.Role != orgusers.RoleOwner {
			writeError(w, http.StatusForbidden, "only an organization owner can do this")
			return
		}
		next(w, r)
	})
}

// requireAppCredentials verifies an App's API key/secret (HTTP Basic:
// key as username, secret as password) and injects AppIdentity. Used only
// by POST /apps/token — the one place this platform still asks for the raw
// secret — to mint the short-lived Bearer JWT that POST /users (requireAppJWT)
// actually runs on. Verified live against Postgres on every call
// (apps.CredentialRepo.Verify), not a signed token, so a revoked credential
// can never even mint a fresh token.
func (a *App) requireAppCredentials(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key, secret, ok := r.BasicAuth()
		if !ok || key == "" || secret == "" {
			writeError(w, http.StatusUnauthorized, "missing app credentials")
			return
		}
		appID, err := a.appCredentials.Verify(r.Context(), key, secret)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid or revoked app credentials")
			return
		}
		ctx := context.WithValue(r.Context(), appIdentityKey, AppIdentity{AppID: appID, APIKey: key})
		next(w, r.WithContext(ctx))
	}
}

// requireAppJWT verifies the Bearer JWT POST /apps/token mints (itself
// gated by requireAppCredentials, above) and injects AppIdentity. This is
// what POST /users actually runs on: a business's backend exchanges its
// App's key+secret for this token once per appTokenTTL window instead of
// sending the raw secret on every end-user it creates. A signed token alone
// can't be invalidated before its own expiry, so — same guarantee Basic
// auth's live Verify gives — api_key is re-checked live against Postgres on
// every call (apps.CredentialRepo.IsActive): a revoked credential's
// already-issued tokens stop working immediately, not just future ones.
func (a *App) requireAppJWT(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			writeError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		claims, err := a.signer.Verify(token)
		if err != nil || claims.Type != auth.ClaimsTypeApp {
			writeError(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		active, err := a.appCredentials.IsActive(r.Context(), claims.AppID, claims.APIKey)
		if err != nil {
			a.log.Error("check app credential active", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to verify credential")
			return
		}
		if !active {
			writeError(w, http.StatusUnauthorized, "invalid or revoked app credentials")
			return
		}
		ctx := context.WithValue(r.Context(), appIdentityKey, AppIdentity{AppID: claims.AppID, APIKey: claims.APIKey})
		next(w, r.WithContext(ctx))
	}
}

// instrument wraps a handler with request-id propagation, structured logging
// and Prometheus metrics — applied to every route (INSTRUCTIONS.md §37).
func (a *App) instrument(route string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		requestID := tracing.NewRequestID()
		ctx := tracing.WithFields(r.Context(), tracing.Fields{RequestID: requestID, Region: a.cfg.Region})
		r = r.WithContext(ctx)

		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next(sw, r)
		duration := time.Since(start)

		a.metrics.HTTPRequestsTotal.WithLabelValues(route, r.Method, http.StatusText(sw.status)).Inc()
		a.metrics.HTTPRequestDuration.WithLabelValues(route, r.Method).Observe(duration.Seconds())
		a.log.Info("request",
			"request_id", requestID,
			"route", route,
			"method", r.Method,
			"status", sw.status,
			"duration_ms", duration.Milliseconds(),
		)
	}
}

// corsMiddleware allows listed browser origins (e.g. the demo app running on
// a different port) to call this API directly, and answers CORS preflight
// OPTIONS requests that the mux would otherwise 405 on.
func corsMiddleware(allowedOrigins []string, next http.Handler) http.Handler {
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		allowed[o] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && allowed[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
