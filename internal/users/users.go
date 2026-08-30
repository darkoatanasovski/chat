// Package users owns account identity: creation and lookup against the
// control-plane database. It does not know about tokens (internal/platform/auth)
// or quotas (internal/quota) — cmd/api wires those together. It does not
// know about tier either: tier lives on the Organization that owns the
// App a user belongs to, resolved live (internal/apps.TierResolver), never
// stored per-user — see docs/platform/security.md.
package users

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type User struct {
	UserID      uuid.UUID
	DisplayName string
	HomeRegion  string
	AppID       int64 // tenant-isolation boundary — see internal/apps
	CreatedAt   time.Time
	// LastActiveAt is nil until this user's first tracked activity — see
	// TouchActivity for what counts as activity and IsOnline for how it's
	// turned into an online/offline boolean.
	LastActiveAt *time.Time
}

// OnlineWindow is how recent LastActiveAt must be for IsOnline to report
// true. Sized to comfortably survive one missed WebSocket ping cycle
// (cmd/gateway pings every 30s and allows up to 60s before treating a
// connection as dead — internal/realtime/websocket.go) without flapping a
// still-connected user to "offline" between heartbeats.
const OnlineWindow = 90 * time.Second

// IsOnline derives online status the same way everywhere it's reported
// (GET /channels/{id}/members, the dashboard's end-user list): recency of
// LastActiveAt, never a separately tracked "connected" flag — see
// TouchActivity's doc comment for what keeps it fresh.
func IsOnline(lastActiveAt *time.Time) bool {
	return lastActiveAt != nil && time.Since(*lastActiveAt) < OnlineWindow
}

type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

func (r *Repo) Create(ctx context.Context, u User) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO users (user_id, display_name, home_region, app_id, created_at)
		VALUES ($1, $2, $3, $4, $5)
	`, u.UserID, u.DisplayName, u.HomeRegion, u.AppID, u.CreatedAt)
	if err != nil {
		return fmt.Errorf("users: create: %w", err)
	}
	return nil
}

// TouchActivity marks userID active as of at. Called from a small,
// deliberately bounded set of real activity signals — see cmd/api's
// touchPresence call sites and cmd/gateway's WS connect/pong/disconnect
// hooks — never from every authenticated request, so read-heavy traffic
// never turns into write traffic here. The WHERE guard makes concurrent or
// out-of-order calls safe: one carrying an older timestamp than what's
// already stored is a no-op rather than clobbering a newer value.
func (r *Repo) TouchActivity(ctx context.Context, userID uuid.UUID, at time.Time) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE users SET last_active_at = $2
		WHERE user_id = $1 AND (last_active_at IS NULL OR last_active_at < $2)
	`, userID, at)
	if err != nil {
		return fmt.Errorf("users: touch activity: %w", err)
	}
	return nil
}

func (r *Repo) Get(ctx context.Context, userID uuid.UUID) (User, error) {
	var u User
	err := r.pool.QueryRow(ctx, `
		SELECT user_id, display_name, home_region, app_id, created_at, last_active_at
		FROM users WHERE user_id = $1
	`, userID).Scan(&u.UserID, &u.DisplayName, &u.HomeRegion, &u.AppID, &u.CreatedAt, &u.LastActiveAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return User{}, fmt.Errorf("users: %w", ErrNotFound)
		}
		return User{}, fmt.Errorf("users: get: %w", err)
	}
	return u, nil
}

// CountByApp backs the dashboard's usage view — a raw end-user count per
// app, informational only (there's no aggregate-per-app limit to compare it
// against; max_channels et al. are enforced per end-user, not per app).
func (r *Repo) CountByApp(ctx context.Context, appID int64) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE app_id = $1`, appID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("users: count by app: %w", err)
	}
	return count, nil
}

// ListByApp backs the dashboard's end-users view — every end-user created
// within one app, newest first. Unbounded like the rest of the dashboard's
// admin-facing lists (apps_detail, team) rather than cursor-paginated: this
// is an operator view over one app's users, not an end-user-facing feed.
func (r *Repo) ListByApp(ctx context.Context, appID int64) ([]User, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT user_id, display_name, home_region, app_id, created_at, last_active_at
		FROM users WHERE app_id = $1 ORDER BY created_at DESC
	`, appID)
	if err != nil {
		return nil, fmt.Errorf("users: list by app: %w", err)
	}
	defer rows.Close()

	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.UserID, &u.DisplayName, &u.HomeRegion, &u.AppID, &u.CreatedAt, &u.LastActiveAt); err != nil {
			return nil, fmt.Errorf("users: list by app: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// CountByRegion backs the dashboard's world-map view — total end-users per
// home region across a set of an org's apps, grouped in one query rather
// than one round trip per app. Regions with zero users are simply absent
// from the returned map; the caller fills in any region it wants shown at 0.
func (r *Repo) CountByRegion(ctx context.Context, appIDs []int64) (map[string]int, error) {
	counts := map[string]int{}
	if len(appIDs) == 0 {
		return counts, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT home_region, count(*) FROM users WHERE app_id = ANY($1) GROUP BY home_region
	`, appIDs)
	if err != nil {
		return nil, fmt.Errorf("users: count by region: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var region string
		var count int
		if err := rows.Scan(&region, &count); err != nil {
			return nil, fmt.Errorf("users: count by region: %w", err)
		}
		counts[region] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("users: count by region: %w", err)
	}
	return counts, nil
}

// Service is the application-level entry point used by cmd/api. It assigns
// a UUIDv7 identity; the caller (cmd/api) is responsible for issuing an
// auth token once the user is durably created.
type Service struct {
	repo *Repo
}

func NewService(repo *Repo) *Service {
	return &Service{repo: repo}
}

// CreateUser mints a new identity within appID — the caller (cmd/api)
// resolves appID from the authenticated App credential (requireAppCredentials),
// never from anything client-asserted, same as it already trusts region.
func (s *Service) CreateUser(ctx context.Context, displayName, region string, appID int64) (User, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return User{}, fmt.Errorf("users: generate id: %w", err)
	}
	u := User{
		UserID:      id,
		DisplayName: displayName,
		HomeRegion:  region,
		AppID:       appID,
		CreatedAt:   time.Now().UTC(),
	}
	if err := s.repo.Create(ctx, u); err != nil {
		return User{}, err
	}
	return u, nil
}

// CountByApp backs the dashboard's usage view — see Repo.CountByApp.
func (s *Service) CountByApp(ctx context.Context, appID int64) (int, error) {
	return s.repo.CountByApp(ctx, appID)
}

// CountByRegion backs the dashboard's world-map view — see Repo.CountByRegion.
func (s *Service) CountByRegion(ctx context.Context, appIDs []int64) (map[string]int, error) {
	return s.repo.CountByRegion(ctx, appIDs)
}

// ListByApp backs the dashboard's end-users view — see Repo.ListByApp.
func (s *Service) ListByApp(ctx context.Context, appID int64) ([]User, error) {
	return s.repo.ListByApp(ctx, appID)
}

func (s *Service) Get(ctx context.Context, userID uuid.UUID) (User, error) {
	return s.repo.Get(ctx, userID)
}

// TouchActivity marks userID active right now — see Repo.TouchActivity.
// Implements internal/realtime.PresenceToucher, so cmd/gateway can inject
// this Service directly into the WS connect handler without that package
// importing internal/users.
func (s *Service) TouchActivity(ctx context.Context, userID uuid.UUID) error {
	return s.repo.TouchActivity(ctx, userID, time.Now().UTC())
}
