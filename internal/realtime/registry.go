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
// user_id -> {region, gateway_id, connection_id} for every device. It is
// consulted on every remote delivery (see GatewaysForUsers) as well as
// serving observability and the seam a future cross-gateway presence
// feature would use.
//
// Per-connection entries carry a TTL refreshed by Heartbeat; a gateway that
// crashes without unregistering leaves entries to expire naturally rather
// than lingering forever. userGatewaysKey mirrors the same connectionTTL
// bound: it's refreshed on every Register and Unregister for that user, so
// it lags true liveness by at most connectionTTL after the user's last
// connection-lifecycle event on any gateway.
type Registry struct {
	redis *redis.Client
}

func NewRegistry(redisClient *redis.Client) *Registry {
	return &Registry{redis: redisClient}
}

func (r *Registry) Register(ctx context.Context, userID uuid.UUID, connID, region, gatewayID string) error {
	setKey := userSetKey(userID)
	connKey := connectionKey(userID, connID)
	gwKey := userGatewaysKey(userID)

	pipe := r.redis.TxPipeline()
	pipe.SAdd(ctx, setKey, connID)
	pipe.HSet(ctx, connKey, "region", region, "gateway_id", gatewayID)
	pipe.Expire(ctx, connKey, connectionTTL)
	pipe.HIncrBy(ctx, gwKey, gatewayID, 1)
	pipe.Expire(ctx, gwKey, connectionTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("realtime: register connection: %w", err)
	}
	return nil
}

func (r *Registry) Heartbeat(ctx context.Context, userID uuid.UUID, connID string) error {
	return r.redis.Expire(ctx, connectionKey(userID, connID), connectionTTL).Err()
}

// decrGatewayScript atomically decrements this user's refcount for one
// gateway and removes the field once it reaches zero, so a user who
// disconnects their last device from a gateway doesn't leave a stale
// zero-count entry for GatewaysForUsers to filter out on every future call.
var decrGatewayScript = redis.NewScript(`
local v = redis.call("HINCRBY", KEYS[1], ARGV[1], -1)
if v <= 0 then
	redis.call("HDEL", KEYS[1], ARGV[1])
end
return v
`)

func (r *Registry) Unregister(ctx context.Context, userID uuid.UUID, connID string) error {
	connKey := connectionKey(userID, connID)

	pipe := r.redis.TxPipeline()
	gwIDCmd := pipe.HGet(ctx, connKey, "gateway_id")
	pipe.SRem(ctx, userSetKey(userID), connID)
	pipe.Del(ctx, connKey)
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return fmt.Errorf("realtime: unregister connection: %w", err)
	}

	if gatewayID, err := gwIDCmd.Result(); err == nil && gatewayID != "" {
		gwKey := userGatewaysKey(userID)
		if err := decrGatewayScript.Run(ctx, r.redis, []string{gwKey}, gatewayID).Err(); err != nil {
			return fmt.Errorf("realtime: decrement gateway refcount: %w", err)
		}
		if err := r.redis.Expire(ctx, gwKey, connectionTTL).Err(); err != nil {
			return fmt.Errorf("realtime: refresh gateway refcount ttl: %w", err)
		}
	}
	return nil
}

// GatewaysForUsers returns, for each of userIDs that has at least one live
// connection, the distinct set of gateway_id values currently holding one —
// a user's devices can be spread across more than one gateway instance, so
// this can return more than one gateway per user. It is the read side of
// Register's writes: Fanout uses it to route delivery to members that
// aren't connected to the local process (see pubsub.go), once per
// message.created/reaction.updated/read.updated event, so its cost directly
// bounds fanout throughput.
//
// One pipelined round trip regardless of len(userIDs): one HGETALL per user
// against userGatewaysKey, which Register/Unregister keep as a live
// gateway_id -> refcount map. This resolves each user's gateway set in work
// proportional to the number of *users* and their *distinct* gateways, not
// their total connection count — a user with 50 sockets on 2 gateways costs
// the same one HGETALL as a user with 2 sockets on 2 gateways. An earlier
// version instead listed every connection ID per user and issued one HGET
// per connection to resolve its gateway, which made this call scale with
// total socket count; under thousands of connections spread across a
// channel's members, that made every fanout event issue thousands of Redis
// commands from the single-threaded Fanout consumer and became the
// dominant source of delivery latency under load.
func (r *Registry) GatewaysForUsers(ctx context.Context, userIDs []uuid.UUID) (map[uuid.UUID][]string, error) {
	if len(userIDs) == 0 {
		return nil, nil
	}

	pipe := r.redis.Pipeline()
	cmds := make([]*redis.MapStringStringCmd, len(userIDs))
	for i, id := range userIDs {
		cmds[i] = pipe.HGetAll(ctx, userGatewaysKey(id))
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return nil, fmt.Errorf("realtime: resolve gateway ids: %w", err)
	}

	out := make(map[uuid.UUID][]string)
	for i, id := range userIDs {
		fields, err := cmds[i].Result()
		if err != nil || len(fields) == 0 {
			continue
		}
		for gatewayID, count := range fields {
			if count == "0" {
				continue
			}
			out[id] = append(out[id], gatewayID)
		}
	}
	return out, nil
}

func userSetKey(userID uuid.UUID) string {
	return "conn:index:" + userID.String()
}

func connectionKey(userID uuid.UUID, connID string) string {
	return "conn:" + userID.String() + ":" + connID
}

func userGatewaysKey(userID uuid.UUID) string {
	return "conn:gateways:" + userID.String()
}
