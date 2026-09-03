// Package apps owns the App (isolated chat instance, numeric id) and its
// API credentials — the tenant-isolation boundary every end-user, channel,
// and message now belongs to exactly one of. An App belongs to exactly one
// Organization (internal/organizations), which is where tier lives; see
// TierResolver for how a request resolves "which tier governs this app"
// without storing tier redundantly on the app or its end-users.
package apps

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = fmt.Errorf("apps: app not found")

// ChannelCapabilities is the "Channel Capabilities" panel on the
// dashboard's app settings screen — 20 independent, owner-toggleable
// feature switches, stored as one JSONB column (apps.channel_capabilities,
// migrations/control/0012_channel_capabilities.sql) rather than 20 more
// boolean columns following max_thread_depth/message_edit_enabled's
// one-column-per-setting precedent, purely because 20 more positional
// *bool parameters on UpdateSettings would stop being readable. Every
// field here is read live wherever it gates behavior — never cached, same
// discipline as every other per-app setting in this package.
type ChannelCapabilities struct {
	// TypingEvents gates the WebSocket typing.start/typing.stop relay
	// (internal/realtime/websocket.go's relayTyping) — fully wired.
	TypingEvents bool `json:"typing_events"`
	// ReadEvents gates POST /channels/{id}/read (handleMarkRead) — fully
	// wired; GET .../read-state is never gated (see that handler's doc
	// comment).
	ReadEvents bool `json:"read_events"`
	// ConnectionEvents gates the connection.updated broadcast on WebSocket
	// connect/disconnect (internal/realtime/websocket.go's
	// relayConnectionEvent) — fully wired.
	ConnectionEvents bool `json:"connection_events"`
	// CustomEvents gates POST /channels/{id}/events
	// (handleSendCustomEvent) — fully wired.
	CustomEvents bool `json:"custom_events"`
	// Reactions gates POST/DELETE .../reactions — fully wired.
	Reactions bool `json:"reactions"`
	// Search gates GET /channels/{id}/messages/search — fully wired
	// (simple ILIKE, not full-text; see internal/messages.Repo.Search's
	// doc comment on that scope choice).
	Search bool `json:"search"`
	// ThreadsAndReplies gates setting parent_id on send (replies) — fully
	// wired. Independent of MaxThreadDepth, which caps nesting once
	// threading itself is on.
	ThreadsAndReplies bool `json:"threads_and_replies"`
	// Quotes gates setting quoted_message_id on send — fully wired.
	Quotes bool `json:"quotes"`
	// Mutes gates POST/DELETE/GET .../mutes — fully wired, but NOT
	// enforced in realtime delivery filtering the way blocks are; see
	// internal/mutes' package doc comment for the exact scope.
	Mutes bool `json:"mutes"`
	// Uploads gates setting attachments on send — fully wired.
	// Client-supplied URLs only; this API never hosts files itself.
	Uploads bool `json:"uploads"`
	// URLEnrichment gates the best-effort, fire-and-forget link-preview
	// fetch after a send (cmd/api's enrichLinkPreview) — fully wired.
	URLEnrichment bool `json:"url_enrichment"`
	// MessageCount is stored and dashboard-visible but does not currently
	// gate anything — reply_count is always tracked and always exposed on
	// every message response regardless of this toggle. Same honest-scope
	// treatment as DynamicPartitioning/StrictLastMessageTime below.
	MessageCount bool `json:"message_count"`
	// MessageReminders gates POST/DELETE .../reminders — fully wired
	// (cmd/worker's poll loop delivers them; see internal/reminders).
	MessageReminders bool `json:"message_reminders"`
	// UnreadReminders gates cmd/worker's unread-channel nudge sweep — fully
	// wired (see UnreadSweeper in cmd/worker/reminders.go).
	UnreadReminders bool `json:"unread_reminders"`
	// PendingMessages gates the Pending flag on send plus the
	// approve/reject moderation endpoints — fully wired.
	PendingMessages bool `json:"pending_messages"`
	// Polls gates setting poll_id on send — fully wired.
	Polls bool `json:"polls"`
	// StrictLastMessageTime is stored and dashboard-visible but currently
	// inert: this codebase has no "system message" concept yet for it to
	// distinguish from a real user message, which is the only thing this
	// toggle would change the ordering behavior of.
	StrictLastMessageTime bool `json:"strict_last_message_time"`
	// LocationSharing gates setting location on send — fully wired.
	LocationSharing bool `json:"location_sharing"`
	// DeliveryEvents is stored and dashboard-visible but not currently
	// wired into cmd/gateway's Fanout — doing so would mean an extra
	// per-message control-plane capability lookup on every single delivery
	// platform-wide (fanout only knows channel_id, not app_id, until it
	// resolves one), which isn't a cost worth paying on the hot path for a
	// capability most apps won't enable. Left as an honest, documented gap
	// rather than a silently-slow implementation.
	DeliveryEvents bool `json:"delivery_events"`
	// Translations gates POST
	// /channels/{id}/messages/{message_id}/translate (internal/translations,
	// cmd/api's handleTranslateMessage) — the 20th toggle on this panel,
	// added the same JSONB-key way as the other 19 (no migration needed;
	// missing/omitted on old rows just decodes to the zero value, off).
	// Unlike every other toggle here, flipping this one on has a real
	// per-request cost against the configured provider on a cache miss —
	// see internal/translations' package doc comment.
	Translations bool `json:"translations"`
}

type App struct {
	AppID     int64
	OrgID     int64
	Name      string
	CreatedAt time.Time
	// MaxThreadDepth caps how many levels deep a reply chain
	// (messages.parent_id) is allowed to nest for this app — 0 means no
	// cap. The first per-app, owner-configurable setting on this table
	// (see migrations/control's app_thread_settings migration); defaults
	// to 3, changed via UpdateSettings (PATCH /apps/{app_id}), and
	// always read live at send time — never cached.
	MaxThreadDepth int
	// MessageEditEnabled gates PATCH /channels/{id}/messages/{message_id}
	// (internal/messages.Repo.Edit) for this app's end-users — the second
	// per-app, owner-configurable setting on this table (see
	// migrations/control/0010_app_message_edit_setting.sql). Defaults to
	// true, changed via UpdateSettings (PATCH /apps/{app_id}), and always
	// read live at edit time — never cached, same discipline as
	// MaxThreadDepth.
	MessageEditEnabled bool
	// ChannelCapabilities is the toggle set described on the type itself.
	ChannelCapabilities ChannelCapabilities
	// MaxMessageLength replaces cmd/api's previously hardcoded
	// maxMessageBodyLen constant with a per-app, owner-lowerable limit.
	// Defaults to 4000.
	MaxMessageLength int
	// EnabledCommands: slash-command names this app's composer surfaces
	// to end-users (see migrations/control/0012_channel_capabilities.sql
	// for which of them cmd/api's message-send path itself interprets).
	EnabledCommands []string
	// DynamicPartitioning: stored and dashboard-visible; does not change
	// routing behavior today — see the migration's doc comment for why.
	DynamicPartitioning bool
}

// appColumns/scanApp are shared by every query below that returns a full
// App row, so the "5 plain columns + 1 jsonb column that needs unmarshaling
// afterward" shape only has to be written once.
const appColumns = `app_id, org_id, name, created_at, max_thread_depth, message_edit_enabled,
		channel_capabilities, max_message_length, enabled_commands, dynamic_partitioning`

func scanApp(row pgx.Row) (App, error) {
	var a App
	var capsRaw []byte
	if err := row.Scan(&a.AppID, &a.OrgID, &a.Name, &a.CreatedAt, &a.MaxThreadDepth, &a.MessageEditEnabled,
		&capsRaw, &a.MaxMessageLength, &a.EnabledCommands, &a.DynamicPartitioning); err != nil {
		return App{}, err
	}
	if err := json.Unmarshal(capsRaw, &a.ChannelCapabilities); err != nil {
		return App{}, fmt.Errorf("apps: unmarshal channel_capabilities: %w", err)
	}
	return a, nil
}

type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

func (r *Repo) Create(ctx context.Context, orgID int64, name string) (App, error) {
	a, err := scanApp(r.pool.QueryRow(ctx, `
		INSERT INTO apps (org_id, name) VALUES ($1, $2)
		RETURNING `+appColumns+`
	`, orgID, name))
	if err != nil {
		return App{}, fmt.Errorf("apps: create: %w", err)
	}
	return a, nil
}

func (r *Repo) Get(ctx context.Context, appID int64) (App, error) {
	a, err := scanApp(r.pool.QueryRow(ctx, `
		SELECT `+appColumns+` FROM apps WHERE app_id = $1
	`, appID))
	if err != nil {
		if err == pgx.ErrNoRows {
			return App{}, ErrNotFound
		}
		return App{}, fmt.Errorf("apps: get: %w", err)
	}
	return a, nil
}

// ListByOrg backs GET /organizations/{org_id}/apps.
func (r *Repo) ListByOrg(ctx context.Context, orgID int64) ([]App, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+appColumns+` FROM apps WHERE org_id = $1 ORDER BY created_at
	`, orgID)
	if err != nil {
		return nil, fmt.Errorf("apps: list by org: %w", err)
	}
	defer rows.Close()

	var out []App
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, fmt.Errorf("apps: scan: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// UpdateSettings backs PATCH /apps/{app_id} — the org owning appID has
// already been verified by the caller (cmd/api's requireOwnedApp), same as
// every other /apps/{app_id}/... mutation. Every parameter is a pointer so
// a PATCH that only touches some settings leaves the rest exactly as they
// were (COALESCE falls back to the existing column value on a nil
// argument) — the caller (cmd/api's handleUpdateApp) is responsible for
// rejecting a request where every parameter is nil, since that's not a
// valid partial update.
//
// capabilities is whole-value, not per-field: cmd/api's handler is the one
// that merges an individual toggle flip into the app's current
// ChannelCapabilities and passes the fully resolved struct here — keeping
// this layer's SQL a plain column overwrite instead of 19 more COALESCEs
// reaching into JSONB keys.
func (r *Repo) UpdateSettings(
	ctx context.Context,
	appID int64,
	maxThreadDepth *int,
	messageEditEnabled *bool,
	capabilities *ChannelCapabilities,
	maxMessageLength *int,
	enabledCommands *[]string,
	dynamicPartitioning *bool,
) (App, error) {
	var capsJSON []byte
	if capabilities != nil {
		var err error
		capsJSON, err = json.Marshal(capabilities)
		if err != nil {
			return App{}, fmt.Errorf("apps: marshal channel_capabilities: %w", err)
		}
	}

	a, err := scanApp(r.pool.QueryRow(ctx, `
		UPDATE apps SET
			max_thread_depth = COALESCE($1, max_thread_depth),
			message_edit_enabled = COALESCE($2, message_edit_enabled),
			channel_capabilities = COALESCE($3::jsonb, channel_capabilities),
			max_message_length = COALESCE($4, max_message_length),
			enabled_commands = COALESCE($5, enabled_commands),
			dynamic_partitioning = COALESCE($6, dynamic_partitioning)
		WHERE app_id = $7
		RETURNING `+appColumns+`
	`, maxThreadDepth, messageEditEnabled, nullableJSON(capsJSON), maxMessageLength, enabledCommands, dynamicPartitioning, appID))
	if err != nil {
		if err == pgx.ErrNoRows {
			return App{}, ErrNotFound
		}
		return App{}, fmt.Errorf("apps: update settings: %w", err)
	}
	return a, nil
}

// nullableJSON turns a zero-length/nil marshal result into a real SQL NULL
// (rather than pgx sending an empty-but-non-nil []byte, which the jsonb
// cast would reject as invalid JSON) so COALESCE($3::jsonb, ...) falls
// through to the existing column value exactly when capabilities was nil.
func nullableJSON(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

// ChannelCapabilities is a narrow accessor on top of Get — the whole shape
// internal/realtime's CapabilitiesResolver interface needs, so that package
// can depend on this one for just the ChannelCapabilities type and this one
// method rather than the full App/Repo surface (the same "inject a narrow
// interface" discipline as internal/realtime.PresenceToucher). Read live,
// same as every call to Get — never cached.
func (r *Repo) ChannelCapabilities(ctx context.Context, appID int64) (ChannelCapabilities, error) {
	a, err := r.Get(ctx, appID)
	if err != nil {
		return ChannelCapabilities{}, err
	}
	return a.ChannelCapabilities, nil
}

// CountByOrg backs the max_apps resource quota (INSTRUCTIONS.md §22/§25):
// always read from authoritative Postgres state, the same role
// channels.Repo.CountByCreator plays for max_channels.
func (r *Repo) CountByOrg(ctx context.Context, orgID int64) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `SELECT count(*) FROM apps WHERE org_id = $1`, orgID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("apps: count by org: %w", err)
	}
	return count, nil
}

// TierSource adapts Repo as TierResolver's Postgres fallback: the live join
// from app_id to its organization's tier. Postgres remains authoritative;
// TierResolver is just the caching layer in front of this.
func (r *Repo) TierSource(ctx context.Context, appID int64) (string, error) {
	var tier string
	err := r.pool.QueryRow(ctx, `
		SELECT o.tier FROM apps a JOIN organizations o ON o.org_id = a.org_id WHERE a.app_id = $1
	`, appID).Scan(&tier)
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("apps: resolve tier source: %w", err)
	}
	return tier, nil
}
