// Package quota is the single, centralized place tier/capability checks are
// made (INSTRUCTIONS.md §22/§23) — domain code calls Quota.Allow(...) instead
// of scattering `if user.Tier == "FREE"` checks around the codebase.
package quota

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

const (
	TierFree       = "FREE"
	TierPro        = "PRO"
	TierBusiness   = "BUSINESS"
	TierEnterprise = "ENTERPRISE"
)

// Capabilities. Keep this list in sync with call sites in cmd/api handlers.
const (
	CapabilityAppCreate        = "app.create"
	CapabilityChannelCreate    = "channel.create"
	CapabilityChannelMemberAdd = "channel.member.add"
	CapabilityMessageSend      = "message.send"
	// CapabilityReactionWrite covers both adding and removing a reaction —
	// one shared bucket, since gating only one direction would let spam
	// toggling a reaction on/off be a free end-run around the limit.
	CapabilityReactionWrite = "reaction.write"
	// CapabilityReadUpdate gates POST /channels/{id}/read — typing has no
	// HTTP endpoint to gate (it never leaves the WebSocket, see
	// internal/realtime.ConnectHandler.relayTyping) and needs no durable
	// quota since a dropped typing frame costs nothing.
	CapabilityReadUpdate = "read.update"
	// CapabilityPollCreate gates POST /channels/{id}/polls.
	CapabilityPollCreate = "poll.create"
	// CapabilityPollVote covers both casting and retracting a vote — one
	// shared bucket, same reasoning as CapabilityReactionWrite covering
	// both add and remove.
	CapabilityPollVote = "poll.vote"
	// CapabilityMessageEdit gates PATCH /channels/{id}/messages/{message_id}
	// — separate from CapabilityMessageSend since editing is gated first by
	// apps.App.MessageEditEnabled (an on/off switch, not a rate limit) and
	// deserves its own budget rather than eating into the same bucket as
	// composing new messages.
	CapabilityMessageEdit = "message.edit"
	// CapabilityMessagePin covers both pinning and unpinning a message —
	// one shared bucket, same reasoning as CapabilityReactionWrite covering
	// both add and remove: gating only one direction would let spam
	// toggling a pin on/off be a free end-run around the limit.
	CapabilityMessagePin = "message.pin"
	// CapabilityBookmarkWrite covers every bookmark-side mutation a caller
	// can make on their own private bookmarks — create, move, delete a
	// bookmark, and create, rename, delete a folder. All of it is scoped
	// to the caller's own data (internal/bookmarks never lets one user
	// touch another's bookmarks) and none of it fans out to other channel
	// members, so unlike message/reaction/poll capabilities there's no
	// "shared state, needs its own tight bucket" reason to split folder
	// actions from bookmark actions into separate capabilities.
	CapabilityBookmarkWrite = "bookmark.write"
)

type TierLimits struct {
	// MaxApps applies to an Organization (how many Apps it may create);
	// the other three apply to an App's end-users (channels/members/
	// messages within that app). Two different subjects share one
	// TierLimits shape because both ultimately resolve from the same
	// Organization.tier — see internal/apps.TierResolver.
	MaxApps               int `yaml:"max_apps"`
	MaxChannels           int `yaml:"max_channels"`
	MaxChannelMembers     int `yaml:"max_channel_members"`
	MessagesPerMinute     int `yaml:"messages_per_minute"`
	ReactionsPerMinute    int `yaml:"reactions_per_minute"`
	ReadUpdatesPerMinute  int `yaml:"read_updates_per_minute"`
	PollsPerMinute        int `yaml:"polls_per_minute"`
	PollVotesPerMinute    int `yaml:"poll_votes_per_minute"`
	MessageEditsPerMinute int `yaml:"message_edits_per_minute"`
	MessagePinsPerMinute  int `yaml:"message_pins_per_minute"`
	BookmarksPerMinute    int `yaml:"bookmarks_per_minute"`
	// RetentionDays is how long a message survives before the per-shard
	// retention sweep (cmd/worker) deletes it. <= 0 means "keep forever" —
	// never applies to rate/resource checks, only to that background job.
	RetentionDays int `yaml:"retention_days"`
}

type tiersFile struct {
	Tiers map[string]TierLimits `yaml:"tiers"`
}

func LoadTiers(path string) (map[string]TierLimits, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("quota: read tiers config: %w", err)
	}
	var f tiersFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("quota: parse tiers config: %w", err)
	}
	if len(f.Tiers) == 0 {
		return nil, fmt.Errorf("quota: tiers config has no tiers defined")
	}
	return f.Tiers, nil
}
