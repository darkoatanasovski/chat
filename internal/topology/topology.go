// Package topology loads infra/topology.yaml — the fixed map of regions ->
// cells (shards) -> per-cell endpoints and infrastructure. It is the static
// half of routing (which cells exist and where); the dynamic half (which App
// is pinned to which cell) lives in the config DB and is read via
// internal/appconfig. The router combines the two: appconfig gives it a
// {region, shard}, and this package gives it that shard's api/ws URLs.
//
// See docs/adr/0006-cell-based-tenant-routing.md and infra/README.md.
package topology

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Topology struct {
	ConfigDB struct {
		DSNEnv string `yaml:"dsn_env"`
	} `yaml:"config_db"`
	Regions []Region `yaml:"regions"`
}

type Region struct {
	ID       string `yaml:"id"`
	Hostname string `yaml:"hostname"`
	Cells    []Cell `yaml:"cells"`
}

type Cell struct {
	ID       string `yaml:"id"`
	Replicas struct {
		API    int `yaml:"api"`
		WS     int `yaml:"ws"`
		Worker int `yaml:"worker"`
	} `yaml:"replicas"`
	Postgres struct {
		DSNEnv string `yaml:"dsn_env"`
	} `yaml:"postgres"`
	Kafka struct {
		BrokersEnv string `yaml:"brokers_env"`
	} `yaml:"kafka"`
	Cache struct {
		AddrEnv string `yaml:"addr_env"`
	} `yaml:"cache"`
	Endpoints struct {
		API string `yaml:"api"`
		WS  string `yaml:"ws"`
	} `yaml:"endpoints"`
}

func Load(path string) (Topology, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Topology{}, fmt.Errorf("topology: read %s: %w", path, err)
	}
	var t Topology
	if err := yaml.Unmarshal(data, &t); err != nil {
		return Topology{}, fmt.Errorf("topology: parse %s: %w", path, err)
	}
	if len(t.Regions) == 0 {
		return Topology{}, fmt.Errorf("topology: no regions defined in %s", path)
	}
	return t, nil
}

// Index is a flattened, lookup-friendly view of a Topology, built once at
// startup. The router asks it for a cell by {region, shard}; app provisioning
// asks it for a cell to place a new app in.
type Index struct {
	byRegionShard map[string]Cell // "region/shard" -> cell
	regions       map[string]Region
	regionOrder   []string // declaration order, for a deterministic default
}

func NewIndex(t Topology) *Index {
	idx := &Index{
		byRegionShard: make(map[string]Cell),
		regions:       make(map[string]Region),
	}
	for _, r := range t.Regions {
		idx.regions[r.ID] = r
		idx.regionOrder = append(idx.regionOrder, r.ID)
		for _, c := range r.Cells {
			idx.byRegionShard[r.ID+"/"+c.ID] = c
		}
	}
	return idx
}

// Cell returns the cell pinned by a {region, shard} placement.
func (i *Index) Cell(region, shard string) (Cell, bool) {
	c, ok := i.byRegionShard[region+"/"+shard]
	return c, ok
}

// Region returns a region by id (for serving a direct regional hostname).
func (i *Index) Region(id string) (Region, bool) {
	r, ok := i.regions[id]
	return r, ok
}

// PlaceInRegion picks a cell to place a new app in. With region set, it uses
// the first cell of that region (a fuller policy — least-loaded — is future
// work). With region empty, it falls back to the first cell of the first
// declared region. Returns the chosen region id and shard id.
func (i *Index) PlaceInRegion(region string) (regionID, shard string, ok bool) {
	if region == "" {
		if len(i.regionOrder) == 0 {
			return "", "", false
		}
		region = i.regionOrder[0]
	}
	r, found := i.regions[region]
	if !found || len(r.Cells) == 0 {
		return "", "", false
	}
	return r.ID, r.Cells[0].ID, true
}

// Regions returns every region in declaration order (control-plane callers
// build per-cell connection pools from this).
func (i *Index) Regions() []Region {
	out := make([]Region, 0, len(i.regionOrder))
	for _, id := range i.regionOrder {
		out = append(out, i.regions[id])
	}
	return out
}
