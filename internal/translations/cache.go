package translations

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Cached is one previously-translated (message_id, target_lang) pair.
type Cached struct {
	TranslatedText string
	SourceLang     string
	CreatedAt      time.Time
}

// Repo is the shard-local cache of translation results
// (migrations/shard/0013_message_translations.sql) — keyed by
// (channel_id, message_id, target_lang) the same way message_reactions is
// keyed by (channel_id, message_id, user_id, emoji), living on whichever
// physical shard the message itself lives on. Every call takes an explicit
// *pgxpool.Pool for the shard internal/routing already resolved, same
// convention as internal/reactions and internal/polls.
type Repo struct{}

func NewRepo() *Repo { return &Repo{} }

// Get returns the cached translation for (channelID, messageID, targetLang)
// if one exists, so a repeat request for the same message/language never
// has to pay for another provider call.
func (r *Repo) Get(ctx context.Context, pool *pgxpool.Pool, channelID, messageID uuid.UUID, targetLang string) (Cached, bool, error) {
	var c Cached
	err := pool.QueryRow(ctx, `
		SELECT translated_body, source_lang, created_at
		FROM message_translations
		WHERE channel_id = $1 AND message_id = $2 AND target_lang = $3
	`, channelID, messageID, targetLang).Scan(&c.TranslatedText, &c.SourceLang, &c.CreatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return Cached{}, false, nil
		}
		return Cached{}, false, fmt.Errorf("translations: get cached: %w", err)
	}
	return c, true, nil
}

// Save records a fresh translation result, keyed by (channelID, messageID,
// targetLang). ON CONFLICT DO UPDATE rather than a plain INSERT: two
// concurrent requests for the same never-before-translated message/language
// can both miss Get and both call the provider — this makes the second
// write a harmless overwrite (with an equivalent result, since the source
// text hasn't changed) instead of a constraint-violation error.
func (r *Repo) Save(ctx context.Context, pool *pgxpool.Pool, channelID, messageID uuid.UUID, sourceLang, targetLang, translatedText string) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO message_translations (channel_id, message_id, target_lang, source_lang, translated_body, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (channel_id, message_id, target_lang)
		DO UPDATE SET source_lang = EXCLUDED.source_lang, translated_body = EXCLUDED.translated_body, created_at = EXCLUDED.created_at
	`, channelID, messageID, targetLang, sourceLang, translatedText, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("translations: save: %w", err)
	}
	return nil
}
