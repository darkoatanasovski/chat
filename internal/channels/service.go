package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Service owns channel creation. In the cell model a channel needs no
// routing metadata of its own — its region and shard are its app's placement
// (config DB), and it lives entirely in this cell's database — so creation is
// just identity assignment plus the tenant-isolation stamp (appID). Quota
// checks (max_channels) remain the caller's responsibility (cmd/api).
type Service struct {
	repo *Repo
}

func NewService(repo *Repo) *Service {
	return &Service{repo: repo}
}

// CreateChannel mints a channel within appID (the tenant-isolation boundary
// it belongs to for its whole lifetime). No home_region / virtual_shard: the
// app is already pinned to one cell, and every channel it owns lives there.
func (s *Service) CreateChannel(ctx context.Context, name string, creator uuid.UUID, appID int64, visibility string, custom json.RawMessage) (Channel, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return Channel{}, fmt.Errorf("channels: generate id: %w", err)
	}
	if visibility == "" {
		visibility = VisibilityPrivate
	}
	if len(custom) == 0 {
		custom = json.RawMessage("{}")
	}

	c := Channel{
		ChannelID:  id,
		Name:       name,
		AppID:      appID,
		CreatedBy:  creator,
		CreatedAt:  time.Now().UTC(),
		Visibility: visibility,
		Custom:     custom,
	}
	if err := s.repo.CreateWithCreatorMembership(ctx, c); err != nil {
		return Channel{}, err
	}
	return c, nil
}
