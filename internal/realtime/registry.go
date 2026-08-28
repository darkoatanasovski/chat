package realtime

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const connectionTTL = 60 * time.Second

// Registry is the cross-process connection directory (INSTRUCTIONS.md §21):
// user_id -> {region, gateway_id, connection_id} for every device. V1's
// fanout doesn't consult it (each gateway only needs to know about its own
// local connections, tracked in Hub) — it exists for observability today and
// as the seam a future cross-gateway push/presence feature would use.
//
// Per-connection entries carry a TTL refreshed by Heartbeat; a gateway that
// crashes without unregistering leaves entries to expire naturally rather
// than lingering forever.
type Registry struct {
	redis *redis.Client
}

func NewRegistry(redisClient *redis.Client) *Registry {
	return &Registry{redis: redisClient}
}

func (r *Registry) Register(ctx context.Context, userID uuid.UUID, connID, region, gatewayID string) error {
	setKey := userSetKey(userID)
	connKey := connectionKey(userID, connID)

	pipe := r.redis.TxPipeline()
	pipe.SAdd(ctx, setKey, connID)
	pipe.HSet(ctx, connKey, "region", region, "gateway_id", gatewayID)
	pipe.Expire(ctx, connKey, connectionTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("realtime: register connection: %w", err)
	}
	return nil
}

func (r *Registry) Heartbeat(ctx context.Context, userID uuid.UUID, connID string) error {
	return r.redis.Expire(ctx, connectionKey(userID, connID), connectionTTL).Err()
}

func (r *Registry) Unregister(ctx context.Context, userID uuid.UUID, connID string) error {
	pipe := r.redis.TxPipeline()
	pipe.SRem(ctx, userSetKey(userID), connID)
	pipe.Del(ctx, connectionKey(userID, connID))
	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("realtime: unregister connection: %w", err)
	}
	return nil
}

// GatewaysForUsers returns, for each of userIDs that has at least one live
// connection, the distinct set of gateway_id values currently holding one —
// a user's devices can be spread across more than one gateway instance, so
// this can return more than one gateway per user. It is the read side of
// Register's writes: Fanout uses it to route delivery to members that
// aren't connected to the local process (see pubsub.go).
//
// Two pipelined round trips regardless of len(userIDs): one SMEMBERS per
// user to list connection ids, then one HGET per connection found to read
// its gateway_id.
func (r *Registry) GatewaysForUsers(ctx context.Context, userIDs []uuid.UUID) (map[uuid.UUID][]string, error) {
	if len(userIDs) == 0 {
		return nil, nil
	}

	setPipe := r.redis.Pipeline()
	setCmds := make([]*redis.StringSliceCmd, len(userIDs))
	for i, id := range userIDs {
		setCmds[i] = setPipe.SMembers(ctx, userSetKey(id))
	}
	if _, err := setPipe.Exec(ctx); err != nil && err != redis.Nil {
		return nil, fmt.Errorf("realtime: list connections for users: %w", err)
	}

	type target struct {
		userID uuid.UUID
		connID string
	}
	var targets []target
	for i, id := range userIDs {
		connIDs, err := setCmds[i].Result()
		if err != nil {
			continue
		}
		for _, connID := range connIDs {
			targets = append(targets, target{userID: id, connID: connID})
		}
	}
	if len(targets) == 0 {
		return nil, nil
	}

	hgetPipe := r.redis.Pipeline()
	hgetCmds := make([]*redis.StringCmd, len(targets))
	for i, t := range targets {
		hgetCmds[i] = hgetPipe.HGet(ctx, connectionKey(t.userID, t.connID), "gateway_id")
	}
	if _, err := hgetPipe.Exec(ctx); err != nil && err != redis.Nil {
		return nil, fmt.Errorf("realtime: resolve gateway ids: %w", err)
	}

	out := make(map[uuid.UUID][]string)
	seen := make(map[uuid.UUID]map[string]bool)
	for i, t := range targets {
		gatewayID, err := hgetCmds[i].Result()
		if err != nil || gatewayID == "" {
			// Entry expired or was unregistered between the two passes —
			// that connection is simply gone, not an error for the caller.
			continue
		}
		if seen[t.userID] == nil {
			seen[t.userID] = make(map[string]bool)
		}
		if seen[t.userID][gatewayID] {
			continue
		}
		seen[t.userID][gatewayID] = true
		out[t.userID] = append(out[t.userID], gatewayID)
	}
	return out, nil
}

func userSetKey(userID uuid.UUID) string {
	return "conn:index:" + userID.String()
}

func connectionKey(userID uuid.UUID, connID string) string {
	return "conn:" + userID.String() + ":" + connID
}
