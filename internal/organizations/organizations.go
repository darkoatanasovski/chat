// Package organizations owns the top of the B2B tenancy chain: the
// customer/business account. Tier lives here (never on an individual App or
// end-user) and caps how many Apps an org may create — see
// internal/apps.Repo.CountByOrg and quota.CapabilityAppCreate.
package organizations

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = fmt.Errorf("organization not found")

type Org struct {
	OrgID     int64
	Name      string
	Tier      string
	CreatedAt time.Time
}

type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

func (r *Repo) Create(ctx context.Context, name, tier string) (Org, error) {
	var o Org
	o.Name, o.Tier = name, tier
	err := r.pool.QueryRow(ctx, `
		INSERT INTO organizations (name, tier) VALUES ($1, $2)
		RETURNING org_id, created_at
	`, name, tier).Scan(&o.OrgID, &o.CreatedAt)
	if err != nil {
		return Org{}, fmt.Errorf("organizations: create: %w", err)
	}
	return o, nil
}

func (r *Repo) Get(ctx context.Context, orgID int64) (Org, error) {
	var o Org
	err := r.pool.QueryRow(ctx, `
		SELECT org_id, name, tier, created_at FROM organizations WHERE org_id = $1
	`, orgID).Scan(&o.OrgID, &o.Name, &o.Tier, &o.CreatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return Org{}, ErrNotFound
		}
		return Org{}, fmt.Errorf("organizations: get: %w", err)
	}
	return o, nil
}

// Service is the application-level entry point used by cmd/api, matching
// the users/channels packages' Repo+Service split (Repo owns queries,
// Service owns the operation's shape).
type Service struct {
	repo *Repo
}

func NewService(repo *Repo) *Service {
	return &Service{repo: repo}
}

func (s *Service) CreateOrg(ctx context.Context, name, tier string) (Org, error) {
	return s.repo.Create(ctx, name, tier)
}
