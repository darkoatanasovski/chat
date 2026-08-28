// Package apps owns the App (isolated chat instance, numeric id) and its
// API credentials — the tenant-isolation boundary every end-user, channel,
// and message now belongs to exactly one of. An App belongs to exactly one
// Organization (internal/organizations), which is where tier lives; see
// TierResolver for how a request resolves "which tier governs this app"
// without storing tier redundantly on the app or its end-users.
package apps

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = fmt.Errorf("apps: app not found")

type App struct {
	AppID     int64
	OrgID     int64
	Name      string
	CreatedAt time.Time
}

type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

func (r *Repo) Create(ctx context.Context, orgID int64, name string) (App, error) {
	a := App{OrgID: orgID, Name: name}
	err := r.pool.QueryRow(ctx, `
		INSERT INTO apps (org_id, name) VALUES ($1, $2)
		RETURNING app_id, created_at
	`, orgID, name).Scan(&a.AppID, &a.CreatedAt)
	if err != nil {
		return App{}, fmt.Errorf("apps: create: %w", err)
	}
	return a, nil
}

func (r *Repo) Get(ctx context.Context, appID int64) (App, error) {
	var a App
	err := r.pool.QueryRow(ctx, `
		SELECT app_id, org_id, name, created_at FROM apps WHERE app_id = $1
	`, appID).Scan(&a.AppID, &a.OrgID, &a.Name, &a.CreatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return App{}, ErrNotFound
		}
		return App{}, fmt.Errorf("apps: get: %w", err)
	}
	return a, nil
}

// ListByOrg backs GET /organizations/{org_id}/apps.
func (r *Repo) ListByOrg(ctx context.Context, orgID int64) ([]App, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT app_id, org_id, name, created_at FROM apps WHERE org_id = $1 ORDER BY created_at
	`, orgID)
	if err != nil {
		return nil, fmt.Errorf("apps: list by org: %w", err)
	}
	defer rows.Close()

	var out []App
	for rows.Next() {
		var a App
		if err := rows.Scan(&a.AppID, &a.OrgID, &a.Name, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("apps: scan: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// CountByOrg backs the max_apps resource quota (INSTRUCTIONS.md §22/§25):
// always read from authoritative Postgres state, the same role
// channels.Repo.CountByCreator plays for max_channels.
func (r *Repo) CountByOrg(ctx context.Context, orgID int64) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `SELECT count(*) FROM apps WHERE org_id = $1`, orgID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("apps: count by org: %w", err)
	}
	return count, nil
}

// TierSource adapts Repo as TierResolver's Postgres fallback: the live join
// from app_id to its organization's tier. Postgres remains authoritative;
// TierResolver is just the caching layer in front of this.
func (r *Repo) TierSource(ctx context.Context, appID int64) (string, error) {
	var tier string
	err := r.pool.QueryRow(ctx, `
		SELECT o.tier FROM apps a JOIN organizations o ON o.org_id = a.org_id WHERE a.app_id = $1
	`, appID).Scan(&tier)
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("apps: resolve tier source: %w", err)
	}
	return tier, nil
}
