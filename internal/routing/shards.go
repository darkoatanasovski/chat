// Package routing answers the two questions every hot-path operation needs
// answered without a scatter/gather query (INSTRUCTIONS.md §6):
//
//	channel_id -> home_region -> virtual_shard -> physical_shard
//
// Virtual-shard assignment is a pure function of the key (no lookup at all).
// Physical-shard assignment is static config, loaded once at startup. Only
// home_region is real, authoritative data — it's the one thing this package
// caches (via Redis) instead of computing.
package routing

import (
	"fmt"
	"hash/fnv"
	"os"

	"gopkg.in/yaml.v3"
)

type ShardsConfig struct {
	VirtualShardCount int             `yaml:"virtual_shard_count"`
	PhysicalShards    []PhysicalShard `yaml:"physical_shards"`
}

type PhysicalShard struct {
	ID                string `yaml:"id"`
	DSNEnv            string `yaml:"dsn_env"`
	VirtualShardRange [2]int `yaml:"virtual_shard_range"`
}

func LoadShardsConfig(path string) (ShardsConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ShardsConfig{}, fmt.Errorf("routing: read shards config: %w", err)
	}
	var cfg ShardsConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return ShardsConfig{}, fmt.Errorf("routing: parse shards config: %w", err)
	}
	if cfg.VirtualShardCount <= 0 {
		return ShardsConfig{}, fmt.Errorf("routing: virtual_shard_count must be positive")
	}
	return cfg, nil
}

// Router resolves keys (channel_id or user_id) to virtual shards and virtual
// shards to physical shard IDs. It holds no connections itself — callers use
// the resulting physical shard ID to pick a pool from postgres.ShardPools.
type Router struct {
	cfg ShardsConfig
}

func NewRouter(cfg ShardsConfig) *Router {
	return &Router{cfg: cfg}
}

// VirtualShard computes hash(key) % virtual_shard_count. Deterministic, no
// I/O — this must never require a database round trip (INSTRUCTIONS.md §6).
func (r *Router) VirtualShard(key string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return int(h.Sum32()) % r.cfg.VirtualShardCount
}

// PhysicalShardID maps a virtual shard to the physical shard currently
// serving its range. Rebalancing means editing shards.yaml, not rehashing
// channel IDs (INSTRUCTIONS.md §8).
func (r *Router) PhysicalShardID(virtualShard int) (string, error) {
	for _, ps := range r.cfg.PhysicalShards {
		if virtualShard >= ps.VirtualShardRange[0] && virtualShard <= ps.VirtualShardRange[1] {
			return ps.ID, nil
		}
	}
	return "", fmt.Errorf("routing: no physical shard covers virtual shard %d", virtualShard)
}

// Resolve is the one-call convenience path domain code uses: key -> physical
// shard ID.
func (r *Router) Resolve(key string) (physicalShardID string, virtualShard int, err error) {
	vs := r.VirtualShard(key)
	id, err := r.PhysicalShardID(vs)
	return id, vs, err
}

func (r *Router) VirtualShardCount() int {
	return r.cfg.VirtualShardCount
}

func (r *Router) PhysicalShards() []PhysicalShard {
	return r.cfg.PhysicalShards
}
