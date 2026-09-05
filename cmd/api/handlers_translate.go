package api

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/darkoatanasovski/chat/internal/apps"
	"github.com/darkoatanasovski/chat/internal/quota"
	"github.com/darkoatanasovski/chat/internal/translations"
)

// langPattern is a permissive BCP-47-ish check (e.g. "es", "zh-Hans",
// "pt-BR") rather than a hardcoded allowlist — Azure Translator supports
// well over 100 languages/scripts, so this only guards against obviously
// malformed input; an unsupported-but-well-formed code is rejected by the
// provider itself and surfaced as a 502.
var langPattern = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})?$`)

type translateMessageRequest struct {
	TargetLang string `json:"target_lang"`
	// SourceLang is optional — Azure auto-detects it when omitted, and
	// Result.DetectedSourceLang reports back what it found either way.
	SourceLang string `json:"source_lang"`
}

type translateMessageResponse struct {
	TranslatedText string `json:"translated_text"`
	SourceLang     string `json:"source_lang"`
	TargetLang     string `json:"target_lang"`
	// Cached is true when this result came from
	// migrations/shard/0013_message_translations.sql instead of a fresh
	// provider call — surfaced so a client (or a curious developer) can
	// see when a request actually cost anything.
	Cached bool `json:"cached"`
}

// handleTranslateMessage backs POST
// /channels/{id}/messages/{message_id}/translate. A cache hit
// (internal/translations.Repo) short-circuits before ever calling the
// configured provider; a miss calls it, caches the result, and records the
// request in the usage ledger either way (see
// internal/translations.UsageRepo's doc comment on why usage tracking, not
// live billing). Same membership/app-scope/region-forwarding/capability/
// rate-limit shape as handleAddReaction — a translation cache write is a
// write against the channel's home-region shard, just like a reaction.
func (a *App) handleTranslateMessage(w http.ResponseWriter, r *http.Request) {
	identity, _ := identityFromContext(r.Context())

	channelID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid channel id")
		return
	}
	messageID, err := uuid.Parse(r.PathValue("message_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid message id")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	_, ok := a.checkChannelWriteAccess(w, r, channelID, identity)
	if !ok {
		return
	}

	app, ok := a.checkTranslationCapability(w, r, identity)
	if !ok {
		return
	}

	if !a.checkTranslationRateLimit(w, r, identity) {
		return
	}

	var req translateMessageRequest
	r.Body = io.NopCloser(bytes.NewReader(body))
	if !readJSON(w, r, &req) {
		return
	}
	req.TargetLang = strings.TrimSpace(req.TargetLang)
	req.SourceLang = strings.TrimSpace(req.SourceLang)
	if !langPattern.MatchString(req.TargetLang) {
		writeError(w, http.StatusBadRequest, "target_lang must be a valid language code, e.g. \"es\" or \"zh-Hans\"")
		return
	}
	if req.SourceLang != "" && !langPattern.MatchString(req.SourceLang) {
		writeError(w, http.StatusBadRequest, "source_lang must be a valid language code, e.g. \"en\"")
		return
	}

	pool, _, _, err := a.shardPoolFor(channelID.String())
	if err != nil {
		a.log.Error("resolve shard", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to resolve shard")
		return
	}

	msg, found, err := a.messagesRepo.GetByID(r.Context(), pool, channelID, messageID)
	if err != nil {
		a.log.Error("load message for translation", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load message")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "message not found")
		return
	}

	if cached, hit, err := a.translationsRepo.Get(r.Context(), pool, channelID, messageID, req.TargetLang); err != nil {
		a.log.Error("load cached translation", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to translate message")
		return
	} else if hit {
		if err := a.translationUsageRepo.Record(r.Context(), identity.AppID, app.OrgID, channelID, messageID, cached.SourceLang, req.TargetLang, 0, 0, true); err != nil {
			a.log.Error("record translation usage", "error", err)
		}
		writeJSON(w, http.StatusOK, translateMessageResponse{
			TranslatedText: cached.TranslatedText,
			SourceLang:     cached.SourceLang,
			TargetLang:     req.TargetLang,
			Cached:         true,
		})
		return
	}

	result, err := a.translationClient.Translate(r.Context(), msg.Body, req.TargetLang, req.SourceLang)
	if err != nil {
		if errors.Is(err, translations.ErrNotConfigured) {
			writeError(w, http.StatusServiceUnavailable, "translation is not configured")
			return
		}
		a.log.Error("translate message", "error", err)
		writeError(w, http.StatusBadGateway, "failed to translate message")
		return
	}

	if err := a.translationsRepo.Save(r.Context(), pool, channelID, messageID, result.DetectedSourceLang, req.TargetLang, result.TranslatedText); err != nil {
		a.log.Error("cache translation", "error", err)
	}

	charCount := len([]rune(msg.Body))
	costMicros := int64(charCount) * translations.CostMicrosPerChar
	if err := a.translationUsageRepo.Record(r.Context(), identity.AppID, app.OrgID, channelID, messageID, result.DetectedSourceLang, req.TargetLang, charCount, costMicros, false); err != nil {
		a.log.Error("record translation usage", "error", err)
	}

	writeJSON(w, http.StatusOK, translateMessageResponse{
		TranslatedText: result.TranslatedText,
		SourceLang:     result.DetectedSourceLang,
		TargetLang:     req.TargetLang,
		Cached:         false,
	})
}

// checkTranslationCapability gates the endpoint on this app's
// "translations" channel capability — read live (never cached), same
// discipline as checkReactionCapability. Returns the loaded App (not just
// a bool) since the handler also needs its OrgID for the usage ledger —
// avoids a second, redundant appsRepo.Get for the same row.
func (a *App) checkTranslationCapability(w http.ResponseWriter, r *http.Request, identity Identity) (apps.App, bool) {
	app, err := a.appsRepo.Get(r.Context(), identity.AppID)
	if err != nil {
		a.log.Error("load app for translation capability check", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check translation capability")
		return apps.App{}, false
	}
	if !app.ChannelCapabilities.Translations {
		writeError(w, http.StatusForbidden, "translations are not enabled for this app")
		return apps.App{}, false
	}
	return app, true
}

// checkTranslationRateLimit enforces translations_per_minute — a
// deliberately tight budget compared to reactions/polls since a miss here
// costs real money against the configured provider (see
// deploy/tiers.yaml's doc comment on translations_per_minute).
func (a *App) checkTranslationRateLimit(w http.ResponseWriter, r *http.Request, identity Identity) bool {
	tier, err := a.appTiers.TierForApp(r.Context(), identity.AppID)
	if err != nil {
		a.log.Error("resolve app tier", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check rate limit")
		return false
	}
	decision, err := a.quota.AllowRate(r.Context(), tier, quota.CapabilityTranslationRequest, fmt.Sprintf("rate:translation:user:%s", identity.UserID))
	if err != nil {
		a.log.Error("rate limit check", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check rate limit")
		return false
	}
	if !decision.Allowed {
		a.metrics.RateLimitRejectionsTotal.WithLabelValues(quota.CapabilityTranslationRequest).Inc()
		writeError(w, http.StatusTooManyRequests, decision.Reason)
		return false
	}
	return true
}

// ---- dashboard usage ----

type dashboardTranslationsResponse struct {
	TotalRequests    int64   `json:"total_requests"`
	CacheHits        int64   `json:"cache_hits"`
	TotalCharacters  int64   `json:"total_characters"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd"`
}

// handleDashboardAppTranslations backs GET
// /dashboard/apps/{app_id}/translations — this app's translation usage
// totals for its Dashboard tab, the same "usage, not a live invoice" figure
// internal/translations.UsageRepo's doc comment describes. A plain
// control-DB aggregate, not a scatter-gather, since translation_usage
// already lives entirely in the control plane.
func (a *App) handleDashboardAppTranslations(w http.ResponseWriter, r *http.Request) {
	orgIdentity, _ := orgIdentityFromContext(r.Context())
	app, ok := a.requireOwnedApp(w, r, orgIdentity.OrgID)
	if !ok {
		return
	}

	summary, err := a.translationUsageRepo.SummaryForApp(r.Context(), app.AppID)
	if err != nil {
		a.log.Error("load app translation usage", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load translation usage")
		return
	}

	writeJSON(w, http.StatusOK, dashboardTranslationsResponse{
		TotalRequests:    summary.TotalRequests,
		CacheHits:        summary.CacheHits,
		TotalCharacters:  summary.TotalCharacters,
		EstimatedCostUSD: float64(summary.EstimatedCostMicros) / 1_000_000,
	})
}
