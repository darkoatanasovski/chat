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
)

type TierLimits struct {
	// MaxApps applies to an Organization (how many Apps it may create);
	// the other three apply to an App's end-users (channels/members/
	// messages within that app). Two different subjects share one
	// TierLimits shape because both ultimately resolve from the same
	// Organization.tier — see internal/apps.TierResolver.
	MaxApps              int `yaml:"max_apps"`
	MaxChannels          int `yaml:"max_channels"`
	MaxChannelMembers    int `yaml:"max_channel_members"`
	MessagesPerMinute    int `yaml:"messages_per_minute"`
	ReactionsPerMinute   int `yaml:"reactions_per_minute"`
	ReadUpdatesPerMinute int `yaml:"read_updates_per_minute"`
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
