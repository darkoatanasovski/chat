package translations

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// UsageRepo is the control-plane ledger of translation requests
// (migrations/control/0014_translation_usage.sql) — one row per request,
// win or cache hit, so an org's translation spend can be reported without
// touching any shard. This is usage *tracking*, not live billing: this
// platform's only real money-moving integration is
// cmd/api/handlers_billing.go's subscription checkout via Dodo Payments,
// which has no per-call metering today. EstimatedCostMicros here is a
// display figure (dashboard: "you've used ~$X in translations this
// period") computed from Client.CostMicrosPerChar, not something that
// itself triggers a charge.
type UsageRepo struct {
	pool *pgxpool.Pool
}

func NewUsageRepo(pool *pgxpool.Pool) *UsageRepo {
	return &UsageRepo{pool: pool}
}

// Record logs one translate request. charCount/estimatedCostMicros are 0
// on a cache hit — CacheHit true means no provider call was actually made,
// so it cost nothing beyond the request itself.
func (r *UsageRepo) Record(ctx context.Context, appID, orgID int64, channelID, messageID uuid.UUID, sourceLang, targetLang string, charCount int, estimatedCostMicros int64, cacheHit bool) error {
	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("translations: generate usage id: %w", err)
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO translation_usage
			(usage_id, app_id, org_id, channel_id, message_id, source_lang, target_lang, char_count, estimated_cost_micros, cache_hit, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`, id, appID, orgID, channelID, messageID, sourceLang, targetLang, charCount, estimatedCostMicros, cacheHit, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("translations: record usage: %w", err)
	}
	return nil
}

// Summary is one app's translation usage totals.
type Summary struct {
	TotalRequests       int64
	CacheHits           int64
	TotalCharacters     int64
	EstimatedCostMicros int64
}

// SummaryForApp backs GET /dashboard/apps/{app_id}/translations — a plain
// control-DB aggregate, not a scatter-gather, unlike message counts
// (cmd/api's dashboardMessagesFor): translation_usage already lives
// entirely in the control plane (see this file's package doc comment).
func (r *UsageRepo) SummaryForApp(ctx context.Context, appID int64) (Summary, error) {
	var s Summary
	err := r.pool.QueryRow(ctx, `
		SELECT
			count(*),
			count(*) FILTER (WHERE cache_hit),
			COALESCE(sum(char_count), 0),
			COALESCE(sum(estimated_cost_micros), 0)
		FROM translation_usage
		WHERE app_id = $1
	`, appID).Scan(&s.TotalRequests, &s.CacheHits, &s.TotalCharacters, &s.EstimatedCostMicros)
	if err != nil {
		return Summary{}, fmt.Errorf("translations: summarize usage: %w", err)
	}
	return s, nil
}
