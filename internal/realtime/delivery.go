package realtime

import (
	"context"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"github.com/darkoatanasovski/chat/internal/blocks"
	"github.com/darkoatanasovski/chat/internal/membership"
)

// Delivery resolves a channel's current members and pushes a frame to each
// of them — locally via Hub if this instance holds their connection,
// otherwise routed through Registry + Publisher to whichever instance does.
// It's the distribution logic every realtime producer needs regardless of
// where the frame originated: Fanout (Kafka-driven, durable events) and
// ConnectHandler's typing relay (client-driven, ephemeral) both use the same
// Delivery instance rather than duplicating this resolution.
type Delivery struct {
	hub            *Hub
	cache          *MembershipCache
	fallback       *membership.Repo // used only on cache miss
	blocksCache    *BlocksCache
	blocksFallback *blocks.Repo // used only on cache miss
	registry       *Registry
	publisher      *Publisher
	log            Logger
}

// Logger is the minimal slog.Logger surface Delivery needs, so callers don't
// have to import log/slog just to satisfy this field.
type Logger interface {
	Error(msg string, args ...any)
}

func NewDelivery(hub *Hub, cache *MembershipCache, fallback *membership.Repo, blocksCache *BlocksCache, blocksFallback *blocks.Repo, registry *Registry, publisher *Publisher, log Logger) *Delivery {
	return &Delivery{hub: hub, cache: cache, fallback: fallback, blocksCache: blocksCache, blocksFallback: blocksFallback, registry: registry, publisher: publisher, log: log}
}

// ToChannelMembers resolves channelID's current members (cache-first,
// Postgres fallback) and pushes frame to each, skipping exclude if it's a
// non-nil UUID (e.g. a typing event never echoes back to its own sender —
// message/reaction events pass uuid.Nil since the sender is expected to
// receive its own event back over the socket) and skipping anyone who has
// blocked, or been blocked by, actor — a message/reaction/read-state
// update/typing signal never reaches a member on either side of a block
// with whoever produced it, regardless of which of them initiated the
// block.
func (d *Delivery) ToChannelMembers(ctx context.Context, channelID uuid.UUID, frame []byte, actor, exclude uuid.UUID) error {
	members, ok, err := d.cache.Members(ctx, channelID)
	if err != nil {
		return err
	}
	if !ok {
		members, err = d.fallback.ListMembers(ctx, channelID)
		if err != nil {
			return err
		}
		_ = d.cache.SetMembers(ctx, channelID, members)
	}
	if len(members) == 0 {
		return nil
	}

	blockedWithActor, err := d.resolveBlocked(ctx, actor)
	if err != nil {
		return err
	}

	// This instance may hold none, some, or all of the channel's live
	// connections — unlike V1, it can no longer assume "not local" means
	// "not connected anywhere."
	var remote []uuid.UUID
	for _, userID := range members {
		if userID == exclude {
			continue
		}
		if blockedWithActor[userID] {
			continue
		}
		if d.hub.HasLocalUser(userID) {
			d.hub.DeliverToUser(userID, frame)
		} else {
			remote = append(remote, userID)
		}
	}
	if len(remote) > 0 {
		return d.deliverRemote(ctx, remote, frame)
	}
	return nil
}

// resolveBlocked returns the set of user IDs that have any block
// relationship with actor, cache-first with a Postgres fallback — the same
// shape as the channel-membership resolution above. uuid.Nil (no
// meaningful actor, e.g. a system-originated frame) always resolves to an
// empty set without touching cache or Postgres, and so does a nil
// blocksCache/blocksFallback — both are optional the way Fanout.metrics is:
// tests that don't care about block enforcement can leave them unset
// instead of needing to wire (or pre-populate the cache for) a dependency
// their scenario never exercises.
func (d *Delivery) resolveBlocked(ctx context.Context, actor uuid.UUID) (map[uuid.UUID]bool, error) {
	if actor == uuid.Nil || d.blocksCache == nil {
		return nil, nil
	}
	blocked, ok, err := d.blocksCache.Blocked(ctx, actor)
	if err != nil {
		return nil, err
	}
	if !ok {
		if d.blocksFallback == nil {
			return nil, nil
		}
		blocked, err = d.blocksFallback.BlockedPairsFor(ctx, actor)
		if err != nil {
			return nil, err
		}
		_ = d.blocksCache.SetBlocked(ctx, actor, blocked)
	}
	if len(blocked) == 0 {
		return nil, nil
	}
	out := make(map[uuid.UUID]bool, len(blocked))
	for _, id := range blocked {
		out[id] = true
	}
	return out, nil
}

// ToUser pushes frame directly to userID's connection(s), wherever they
// live — the single-recipient counterpart to ToChannelMembers, for events
// (message_reminder.due, unread.reminder) that are addressed to exactly one
// person rather than a channel's whole membership. No block-list filtering
// applies: these are system-originated notices about the recipient's own
// state, not a communication from another user that a block could apply
// to. A recipient with no live connection anywhere is silently skipped,
// same best-effort contract as every other delivery path.
func (d *Delivery) ToUser(ctx context.Context, userID uuid.UUID, frame []byte) error {
	if d.hub.HasLocalUser(userID) {
		d.hub.DeliverToUser(userID, frame)
		return nil
	}
	return d.deliverRemote(ctx, []uuid.UUID{userID}, frame)
}

// ChannelsForUser returns userID's current channel_ids — backs
// relayConnectionEvent's "connection_events" capability, the one realtime
// producer in this package that needs "every channel this user is in"
// rather than "every member of this one channel." Always reads Postgres
// directly (no cache layer, unlike ToChannelMembers/IsMember): connects and
// disconnects happen far less often than channel-membership lookups
// elsewhere on the hot send/typing paths, so this stays a low-frequency
// read, same class as internal/messages.Repo.CountDailyByChannels.
func (d *Delivery) ChannelsForUser(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error) {
	channels, err := d.fallback.ListChannelsForUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("realtime: list channels for user: %w", err)
	}
	out := make([]uuid.UUID, len(channels))
	for i, c := range channels {
		out[i] = c.ChannelID
	}
	return out, nil
}

// IsMember reports whether userID is currently a member of channelID —
// same resolution as ToChannelMembers, used to verify a client-asserted
// channel_id on an inbound WS message before fanning anything out from it
// (INSTRUCTIONS.md §43: never trust client-asserted state).
func (d *Delivery) IsMember(ctx context.Context, channelID, userID uuid.UUID) (bool, error) {
	members, ok, err := d.cache.Members(ctx, channelID)
	if err != nil {
		return false, err
	}
	if !ok {
		members, err = d.fallback.ListMembers(ctx, channelID)
		if err != nil {
			return false, err
		}
		_ = d.cache.SetMembers(ctx, channelID, members)
	}
	return slices.Contains(members, userID), nil
}

// deliverRemote routes frame to whichever gateway instance(s) currently
// hold each of userIDs' connections, one Publish per destination gateway —
// a user with several devices spread across gateways gets one push to each.
// A user with no live connection anywhere (offline, or its registry entry
// expired) is silently skipped, exactly as a local-only delivery would skip
// them; this is the same best-effort contract, just extended across
// processes. A publish failure to one gateway is logged and does not fail
// the whole event — local members (if any) have already received their
// copy, and re-processing the event over a Publish error would just
// duplicate that local delivery for no benefit.
func (d *Delivery) deliverRemote(ctx context.Context, userIDs []uuid.UUID, frame []byte) error {
	byGateway, err := d.registry.GatewaysForUsers(ctx, userIDs)
	if err != nil {
		return fmt.Errorf("realtime: resolve remote gateways: %w", err)
	}

	targets := make(map[string][]uuid.UUID)
	for userID, gatewayIDs := range byGateway {
		for _, gatewayID := range gatewayIDs {
			targets[gatewayID] = append(targets[gatewayID], userID)
		}
	}

	for gatewayID, users := range targets {
		if err := d.publisher.Push(ctx, gatewayID, users, frame); err != nil {
			d.log.Error("delivery: push to remote gateway", "gateway_id", gatewayID, "error", err)
		}
	}
	return nil
}
