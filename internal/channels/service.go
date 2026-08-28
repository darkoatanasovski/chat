package channels

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/darkoatanasovski/chat/internal/routing"
)

// Service ties channel creation to virtual-shard assignment. Quota checks
// (max_channels) are the caller's responsibility (cmd/api), matching
// INSTRUCTIONS.md §23's Allow(subject, capability) being called at the
// operation boundary rather than buried in domain services.
type Service struct {
	repo   *Repo
	router *routing.Router
}

func NewService(repo *Repo, router *routing.Router) *Service {
	return &Service{repo: repo, router: router}
}

// CreateChannel assigns home_region from the creator's region (V1's simple
// placement policy: a channel is born in its creator's region). virtual_shard
// is a pure function of the new channel_id. appID stamps the tenant-isolation
// boundary the channel belongs to for its whole lifetime.
func (s *Service) CreateChannel(ctx context.Context, name string, creator uuid.UUID, creatorRegion string, appID int64) (Channel, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return Channel{}, fmt.Errorf("channels: generate id: %w", err)
	}
	virtualShard := s.router.VirtualShard(id.String())

	c := Channel{
		ChannelID:    id,
		Name:         name,
		HomeRegion:   creatorRegion,
		VirtualShard: virtualShard,
		AppID:        appID,
		CreatedBy:    creator,
		CreatedAt:    time.Now().UTC(),
	}
	if err := s.repo.CreateWithCreatorMembership(ctx, c); err != nil {
		return Channel{}, err
	}
	return c, nil
}
