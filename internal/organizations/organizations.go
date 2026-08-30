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

	// DodoCustomerID/DodoSubscriptionID are set once this org has ever
	// checked out through Dodo Payments (see cmd/api/handlers_billing.go);
	// both are empty for an org that's never upgraded off FREE.
	DodoCustomerID     string
	DodoSubscriptionID string
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
		SELECT org_id, name, tier, created_at, coalesce(dodo_customer_id, ''), coalesce(dodo_subscription_id, '')
		FROM organizations WHERE org_id = $1
	`, orgID).Scan(&o.OrgID, &o.Name, &o.Tier, &o.CreatedAt, &o.DodoCustomerID, &o.DodoSubscriptionID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return Org{}, ErrNotFound
		}
		return Org{}, fmt.Errorf("organizations: get: %w", err)
	}
	return o, nil
}

// UpgradeTier records a successful Dodo Payments checkout: the org moves to
// tier and is now tied to this Dodo customer/subscription. Called from the
// subscription.active webhook, never directly from a client request — tier
// changes only ever follow a verified payment event (see
// cmd/api/handlers_billing.go).
func (r *Repo) UpgradeTier(ctx context.Context, orgID int64, tier, dodoCustomerID, dodoSubscriptionID string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE organizations SET tier = $1, dodo_customer_id = $2, dodo_subscription_id = $3 WHERE org_id = $4
	`, tier, dodoCustomerID, dodoSubscriptionID, orgID)
	if err != nil {
		return fmt.Errorf("organizations: upgrade tier: %w", err)
	}
	return nil
}

// DowngradeTier reverts an org to tier when its paid subscription ends
// (cancelled/expired/failed), but only if dodoSubscriptionID still matches
// the subscription on file. That guard matters because upgrading again
// (e.g. PRO -> BUSINESS) starts a brand-new Dodo subscription and
// overwrites dodo_subscription_id via UpgradeTier; a cancellation webhook
// for the now-superseded old subscription arriving after that must not
// clobber the org's current paid tier.
func (r *Repo) DowngradeTier(ctx context.Context, orgID int64, dodoSubscriptionID, tier string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE organizations SET tier = $1 WHERE org_id = $2 AND dodo_subscription_id = $3
	`, tier, orgID, dodoSubscriptionID)
	if err != nil {
		return fmt.Errorf("organizations: downgrade tier: %w", err)
	}
	return nil
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
