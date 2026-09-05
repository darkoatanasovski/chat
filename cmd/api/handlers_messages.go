package api

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/darkoatanasovski/chat/internal/channels"
	"github.com/darkoatanasovski/chat/internal/messages"
	"github.com/darkoatanasovski/chat/internal/quota"
)

const unlimitedThreadDepth = 0

type messageRow messages.Message

type sendMessageRequest struct {
	ClientMessageID string  `json:"client_message_id"`
	Body            string  `json:"body"`
	// ParentID, if set, makes this a reply — see internal/messages.Repo.Send
	// and apps.App.MaxThreadDepth for how nesting depth is enforced. Only
	// accepted when this app's "threads_and_replies" capability is on (403
	// otherwise).
	ParentID *string `json:"parent_id,omitempty"`
	// PollID, if set, attaches an already-created poll (POST
	// /channels/{id}/polls) to this message — the poll must already exist
	// in this same channel (404 otherwise, see internal/polls.Repo.Exists).
	// Only accepted when this app's "polls" capability is on (403
	// otherwise).
	PollID *string `json:"poll_id,omitempty"`
	// QuotedMessageID, if set, quotes another message already in this same
	// channel (404 if it isn't — see internal/messages.Repo.Exists). Only
	// accepted when this app's "quotes" capability is on (403 otherwise).
	QuotedMessageID *string `json:"quoted_message_id,omitempty"`
	// Attachments is only accepted when this app's "uploads" capability is
	// on (403 otherwise) — see messages.Attachment's doc comment on why
	// this API only ever stores client-supplied references, never hosts
	// files itself.
	Attachments []messages.Attachment `json:"attachments,omitempty"`
	// Location is only accepted when this app's "location_sharing"
	// capability is on (403 otherwise).
	Location *messages.Location `json:"location,omitempty"`
	// Pending, if true, is only accepted when this app's "pending_messages"
	// capability is on (403 otherwise) — the message is created with
	// status=pending, visible only to its own sender until a moderator
	// approves it (see cmd/api/handlers_moderation.go).
	Pending *bool `json:"pending,omitempty"`
}

type messageResponse struct {
	MessageID       string                    `json:"message_id"`
	ChannelID       string                    `json:"channel_id"`
	Sequence        int64                     `json:"sequence"`
	SenderID        string                    `json:"sender_id"`
	ClientMessageID string                    `json:"client_message_id"`
	Body            string                    `json:"body"`
	ParentID        *string                   `json:"parent_id,omitempty"`
	ReplyCount      int64                     `json:"reply_count"`
	PollID          *string                   `json:"poll_id,omitempty"`
	CreatedAt       string                    `json:"created_at"`
	// EditedAt is absent for a message that's never been edited — present
	// (and refreshed) after every PATCH /channels/{id}/messages/{message_id}.
	EditedAt        *string                   `json:"edited_at,omitempty"`
	ReactionCounts  map[string]int            `json:"reaction_counts"`
	LatestReactions []reactionSummaryResponse `json:"latest_reactions"`
	// PinnedAt/PinnedBy are both absent for a message that isn't currently
	// pinned, and both present together after POST
	// /channels/{id}/messages/{message_id}/pin — see
	// internal/messages.Message.PinnedAt's doc comment.
	PinnedAt *string `json:"pinned_at,omitempty"`
	PinnedBy *string `json:"pinned_by,omitempty"`
	// QuotedMessageID is absent unless this message quotes another one —
	// the "quotes" capability.
	QuotedMessageID *string `json:"quoted_message_id,omitempty"`
	// Attachments is always present, an empty array when there are none —
	// the "uploads" capability.
	Attachments []messages.Attachment `json:"attachments"`
	// LinkPreview is absent until url_enrichment's async fetch fills it in
	// (or forever, if it never does) — the "url_enrichment" capability.
	LinkPreview *messages.LinkPreview `json:"link_preview,omitempty"`
	// Location is absent unless this message shared one — the
	// "location_sharing" capability.
	Location *messages.Location `json:"location,omitempty"`
	// Status is "sent" or "pending" — the "pending_messages" capability.
	Status string `json:"status"`
}

func messageResponseFrom(m messageRow) messageResponse {
	var parentID *string
	if m.ParentID != nil {
		s := m.ParentID.String()
		parentID = &s
	}
	var pollID *string
	if m.PollID != nil {
		s := m.PollID.String()
		pollID = &s
	}
	var editedAt *string
	if m.EditedAt != nil {
		s := m.EditedAt.Format(rfc3339Milli)
		editedAt = &s
	}
	var pinnedAt *string
	if m.PinnedAt != nil {
		s := m.PinnedAt.Format(rfc3339Milli)
		pinnedAt = &s
	}
	var pinnedBy *string
	if m.PinnedBy != nil {
		s := m.PinnedBy.String()
		pinnedBy = &s
	}
	var quotedMessageID *string
	if m.QuotedMessageID != nil {
		s := m.QuotedMessageID.String()
		quotedMessageID = &s
	}
	attachments := m.Attachments
	if attachments == nil {
		attachments = []messages.Attachment{}
	}
	return messageResponse{
		MessageID:       m.MessageID.String(),
		ChannelID:       m.ChannelID.String(),
		Sequence:        m.Sequence,
		SenderID:        m.SenderID.String(),
		ClientMessageID: m.ClientMessageID.String(),
		Body:            m.Body,
		ParentID:        parentID,
		ReplyCount:      m.ReplyCount,
		PollID:          pollID,
		CreatedAt:       m.CreatedAt.Format(rfc3339Milli),
		EditedAt:        editedAt,
		ReactionCounts:  m.ReactionCounts,
		LatestReactions: toReactionSummaryResponse(m.LatestReactions),
		PinnedAt:        pinnedAt,
		PinnedBy:        pinnedBy,
		QuotedMessageID: quotedMessageID,
		Attachments:     attachments,
		LinkPreview:     m.LinkPreview,
		Location:        m.Location,
		Status:          m.Status,
	}
}

// handleSendMessage enforces the client-generated idempotency key
// (INSTRUCTIONS.md §19), the per-user rate limit, and — for a channel whose
// home region isn't this instance — forwards the write there rather than
// touching Postgres locally (§5/§27).
func (a *App) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	identity, _ := identityFromContext(r.Context())

	channelID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid channel id")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	isMember, err := a.membershipRepo.IsMember(r.Context(), channelID, identity.UserID)
	if err != nil {
		a.log.Error("check membership", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check membership")
		return
	}
	if !isMember {
		writeError(w, http.StatusForbidden, "not a member of this channel")
		return
	}

	route, err := a.region.Resolve(r.Context(), channelID.String())
	if err != nil {
		if errors.Is(err, channels.ErrNotFound) {
			writeError(w, http.StatusNotFound, "channel not found")
			return
		}
		a.log.Error("resolve channel route", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load channel")
		return
	}
	// Defense in depth (INSTRUCTIONS.md §43): membership should only ever
	// exist within one app by construction, but never rely on that
	// invariant implicitly when the row already carries app_id to check
	// explicitly.
	if route.AppID != identity.AppID {
		writeError(w, http.StatusForbidden, "not a member of this channel")
		return
	}

	tier, err := a.appTiers.TierForApp(r.Context(), identity.AppID)
	if err != nil {
		a.log.Error("resolve app tier", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check rate limit")
		return
	}
	decision, err := a.quota.AllowRate(r.Context(), tier, quota.CapabilityMessageSend, "rate:message:user:"+identity.UserID.String())
	if err != nil {
		a.log.Error("rate limit check", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check rate limit")
		return
	}
	if !decision.Allowed {
		a.metrics.RateLimitRejectionsTotal.WithLabelValues(quota.CapabilityMessageSend).Inc()
		writeError(w, http.StatusTooManyRequests, decision.Reason)
		return
	}

	var req sendMessageRequest
	r.Body = io.NopCloser(bytes.NewReader(body))
	if !readJSON(w, r, &req) {
		return
	}
	clientMessageID, err := uuid.Parse(req.ClientMessageID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid client_message_id")
		return
	}

	// Loaded unconditionally now (previously only fetched when replying,
	// for MaxThreadDepth) — MaxMessageLength and every capability gate
	// below need it too, and it's always read live here, never cached, so
	// a just-changed setting (PATCH /apps/{app_id}) applies to the very
	// next send.
	app, err := a.appsRepo.Get(r.Context(), identity.AppID)
	if err != nil {
		a.log.Error("load app for send", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to send message")
		return
	}

	if req.Body == "" || len(req.Body) > app.MaxMessageLength {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("body is required (max %d chars)", app.MaxMessageLength))
		return
	}

	var parentID *uuid.UUID
	maxThreadDepth := unlimitedThreadDepth
	if req.ParentID != nil {
		if !app.ChannelCapabilities.ThreadsAndReplies {
			writeError(w, http.StatusForbidden, "threads and replies are not enabled for this app")
			return
		}
		id, err := uuid.Parse(*req.ParentID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid parent_id")
			return
		}
		parentID = &id
		maxThreadDepth = app.MaxThreadDepth
	}

	pool, _, _, err := a.shardPoolFor(channelID.String())
	if err != nil {
		a.log.Error("resolve shard", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to resolve shard")
		return
	}

	var pollID *uuid.UUID
	if req.PollID != nil {
		if !app.ChannelCapabilities.Polls {
			writeError(w, http.StatusForbidden, "polls are not enabled for this app")
			return
		}
		id, err := uuid.Parse(*req.PollID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid poll_id")
			return
		}
		// Checked here, not inside messages.Repo.Send, since a poll is a
		// separate entity (internal/polls) rather than an intrinsic
		// property of the messages table the way parent_id/thread depth
		// is — see PollID's doc comment on messages.Message.
		exists, err := a.pollsRepo.Exists(r.Context(), pool, channelID, id)
		if err != nil {
			a.log.Error("check poll exists", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to send message")
			return
		}
		if !exists {
			writeError(w, http.StatusNotFound, "poll not found in this channel")
			return
		}
		pollID = &id
	}

	var quotedMessageID *uuid.UUID
	if req.QuotedMessageID != nil {
		if !app.ChannelCapabilities.Quotes {
			writeError(w, http.StatusForbidden, "quotes are not enabled for this app")
			return
		}
		id, err := uuid.Parse(*req.QuotedMessageID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid quoted_message_id")
			return
		}
		// Same-DB existence check (unlike bookmarks' cross-database case,
		// messages and the message being quoted live in the same shard, so
		// this could in principle be a real FK — kept app-layer instead
		// purely to match parent_id's existing precedent of not having
		// one).
		exists, err := a.messagesRepo.Exists(r.Context(), pool, channelID, id)
		if err != nil {
			a.log.Error("check quoted message exists", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to send message")
			return
		}
		if !exists {
			writeError(w, http.StatusNotFound, "quoted message not found in this channel")
			return
		}
		quotedMessageID = &id
	}

	var attachments []messages.Attachment
	if len(req.Attachments) > 0 {
		if !app.ChannelCapabilities.Uploads {
			writeError(w, http.StatusForbidden, "uploads are not enabled for this app")
			return
		}
		attachments = req.Attachments
	}

	var location *messages.Location
	if req.Location != nil {
		if !app.ChannelCapabilities.LocationSharing {
			writeError(w, http.StatusForbidden, "location sharing is not enabled for this app")
			return
		}
		location = req.Location
	}

	status := messages.StatusSent
	if req.Pending != nil && *req.Pending {
		if !app.ChannelCapabilities.PendingMessages {
			writeError(w, http.StatusForbidden, "pending messages are not enabled for this app")
			return
		}
		status = messages.StatusPending
	}

	var msg messageRow
	err = a.metrics.TimePostgres("send_message", func() error {
		m, _, sendErr := a.messagesRepo.Send(r.Context(), pool, channelID, identity.UserID, clientMessageID, req.Body, parentID, maxThreadDepth, pollID, quotedMessageID, attachments, location, status)
		msg = messageRow(m)
		return sendErr
	})
	if err != nil {
		if errors.Is(err, messages.ErrParentNotFound) {
			writeError(w, http.StatusNotFound, "parent message not found in this channel")
			return
		}
		if errors.Is(err, messages.ErrThreadDepthExceeded) {
			writeError(w, http.StatusBadRequest, "reply would exceed this app's max thread depth")
			return
		}
		a.log.Error("send message", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to send message")
		return
	}
	a.metrics.MessagesSentTotal.WithLabelValues(a.cfg.Region).Inc()

	// Best-effort denormalization: the message is already durably committed
	// on its shard; a failure here only delays "last message" ordering in
	// GET /users/me/channels, never message durability.
	if members, err := a.membershipRepo.ListMembers(r.Context(), channelID); err == nil {
		_ = a.channelsRepo.UpdateLastMessage(r.Context(), channelID, members, msg.Sequence, msg.CreatedAt)
	}
	a.touchPresence(identity.UserID)

	// url_enrichment: best-effort, fire-and-forget — never blocks or fails
	// the send itself (see enrichLinkPreview's doc comment). Only kicked
	// off for a freshly-created message with a body (a duplicate retry
	// returning the original message doesn't need a second attempt).
	if app.ChannelCapabilities.URLEnrichment && status == messages.StatusSent {
		a.enrichLinkPreview(pool, channelID, msg.MessageID, msg.Body)
	}

	writeJSON(w, http.StatusCreated, messageResponseFrom(msg))
}

// handleListMessages backs GET /channels/{id}/messages?before=&limit= using
// cursor pagination only — never OFFSET (INSTRUCTIONS.md §11). Reads don't
// forward to the home region: message storage is reachable from any
// instance in this local topology (see docs/adr/0002 and
// docs/platform/multi-region.md for what a real geo-deployment adds here).
func (a *App) handleListMessages(w http.ResponseWriter, r *http.Request) {
	identity, _ := identityFromContext(r.Context())

	channelID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid channel id")
		return
	}

	isMember, err := a.membershipRepo.IsMember(r.Context(), channelID, identity.UserID)
	if err != nil {
		a.log.Error("check membership", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check membership")
		return
	}
	if !isMember {
		writeError(w, http.StatusForbidden, "not a member of this channel")
		return
	}

	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}

	pool, _, _, err := a.shardPoolFor(channelID.String())
	if err != nil {
		a.log.Error("resolve shard", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to resolve shard")
		return
	}

	// A caller never sees a message from anyone they have any block
	// relationship with — matches the realtime delivery filter
	// (internal/realtime.Delivery.ToChannelMembers) so history and live
	// delivery agree on who "can't communicate" with whom.
	blockedWith, err := a.blocksRepo.BlockedPairsFor(r.Context(), identity.UserID)
	if err != nil {
		a.log.Error("resolve blocked users", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list messages")
		return
	}

	var rows []messageRow
	err = a.metrics.TimePostgres("list_messages", func() error {
		msgs, listErr := a.messagesRepo.ListBefore(r.Context(), pool, channelID, before, limit, blockedWith)
		rows = make([]messageRow, len(msgs))
		for i, m := range msgs {
			rows[i] = messageRow(m)
		}
		return listErr
	})
	if err != nil {
		a.log.Error("list messages", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list messages")
		return
	}

	out := make([]messageResponse, len(rows))
	for i, m := range rows {
		out[i] = messageResponseFrom(m)
	}
	writeJSON(w, http.StatusOK, out)
}

type editMessageRequest struct {
	Body string `json:"body"`
}

// handleEditMessage backs PATCH /channels/{id}/messages/{message_id}. Two
// gates beyond ordinary channel-write access: the app must have
// apps.App.MessageEditEnabled on (read live, never cached, so flipping it
// via PATCH /apps/{app_id} takes effect on the very next attempt), and the
// caller must be the message's own sender — the second check is re-verified
// against the row itself inside internal/messages.Repo.Edit, not trusted
// from context here.
func (a *App) handleEditMessage(w http.ResponseWriter, r *http.Request) {
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

	app, err := a.appsRepo.Get(r.Context(), identity.AppID)
	if err != nil {
		a.log.Error("load app for message edit check", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to edit message")
		return
	}
	if !app.MessageEditEnabled {
		writeError(w, http.StatusForbidden, "message editing is not enabled for this app")
		return
	}

	if !a.checkMessageEditRateLimit(w, r, identity) {
		return
	}

	var req editMessageRequest
	r.Body = io.NopCloser(bytes.NewReader(body))
	if !readJSON(w, r, &req) {
		return
	}
	if req.Body == "" || len(req.Body) > app.MaxMessageLength {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("body is required (max %d chars)", app.MaxMessageLength))
		return
	}

	pool, _, _, err := a.shardPoolFor(channelID.String())
	if err != nil {
		a.log.Error("resolve shard", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to resolve shard")
		return
	}

	var msg messageRow
	err = a.metrics.TimePostgres("edit_message", func() error {
		m, editErr := a.messagesRepo.Edit(r.Context(), pool, channelID, messageID, identity.UserID, req.Body)
		msg = messageRow(m)
		return editErr
	})
	if err != nil {
		if errors.Is(err, messages.ErrNotFound) {
			writeError(w, http.StatusNotFound, "message not found")
			return
		}
		if errors.Is(err, messages.ErrNotMessageOwner) {
			writeError(w, http.StatusForbidden, "only the sender may edit this message")
			return
		}
		a.log.Error("edit message", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to edit message")
		return
	}
	a.touchPresence(identity.UserID)

	writeJSON(w, http.StatusOK, messageResponseFrom(msg))
}

// checkMessageEditRateLimit mirrors checkReactionRateLimit's shape, gating
// edits with their own budget (CapabilityMessageEdit) rather than sharing
// CapabilityMessageSend's — editing your own message and composing a new
// one are tracked separately.
func (a *App) checkMessageEditRateLimit(w http.ResponseWriter, r *http.Request, identity Identity) bool {
	tier, err := a.appTiers.TierForApp(r.Context(), identity.AppID)
	if err != nil {
		a.log.Error("resolve app tier", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check rate limit")
		return false
	}
	decision, err := a.quota.AllowRate(r.Context(), tier, quota.CapabilityMessageEdit, "rate:message-edit:user:"+identity.UserID.String())
	if err != nil {
		a.log.Error("rate limit check", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to check rate limit")
		return false
	}
	if !decision.Allowed {
		a.metrics.RateLimitRejectionsTotal.WithLabelValues(quota.CapabilityMessageEdit).Inc()
		writeError(w, http.StatusTooManyRequests, decision.Reason)
		return false
	}
	return true
}
