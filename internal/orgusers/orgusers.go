// Package orgusers owns individual human accounts for the customer-facing
// dashboard — email + bcrypt password, more than one person per
// Organization, each with a role. Distinct from internal/organizations'
// existing org-admin token (POST /organizations), which mints one shared,
// unattributed credential for the whole org and stays as-is for
// programmatic/automation use (e.g. tools/loadtest); this package is real
// per-person auth on top of the same organizations table.
package orgusers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	RoleOwner  = "owner"
	RoleMember = "member"
)

const pgUniqueViolation = "23505"

var (
	ErrNotFound   = errors.New("orgusers: not found")
	ErrEmailTaken = errors.New("orgusers: email already registered")
)

// User's PasswordHash is only ever populated by GetByEmail (login needs it
// to verify against) — handlers build their own response DTOs and simply
// never include it, the same way apps.Credential's Secret only appears in
// the one response that's allowed to carry it.
type User struct {
	UserID       uuid.UUID
	OrgID        int64
	Email        string
	PasswordHash string
	Role         string
	CreatedAt    time.Time
}

type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

func (r *Repo) Create(ctx context.Context, orgID int64, email, passwordHash, role string) (User, error) {
	userID, err := uuid.NewV7()
	if err != nil {
		return User{}, fmt.Errorf("orgusers: generate user id: %w", err)
	}
	u := User{UserID: userID, OrgID: orgID, Email: email, Role: role}
	err = r.pool.QueryRow(ctx, `
		INSERT INTO org_users (user_id, org_id, email, password_hash, role)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING created_at
	`, userID, orgID, email, passwordHash, role).Scan(&u.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return User{}, ErrEmailTaken
		}
		return User{}, fmt.Errorf("orgusers: create: %w", err)
	}
	return u, nil
}

// GetByEmail is the login lookup — the only place PasswordHash is
// populated, since verifying it is the entire point of this call.
func (r *Repo) GetByEmail(ctx context.Context, email string) (User, error) {
	var u User
	err := r.pool.QueryRow(ctx, `
		SELECT user_id, org_id, email, password_hash, role, created_at
		FROM org_users WHERE email = $1
	`, email).Scan(&u.UserID, &u.OrgID, &u.Email, &u.PasswordHash, &u.Role, &u.CreatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return User{}, ErrNotFound
		}
		return User{}, fmt.Errorf("orgusers: get by email: %w", err)
	}
	return u, nil
}

// GetByID is the live per-request resolution a session token's subject
// (user_id) goes through — org_id and role are never trusted from the
// token itself (INSTRUCTIONS.md §43), always read fresh here, the same
// reasoning apps.TierResolver and routing.RegionResolver already apply to
// their own mutable state.
func (r *Repo) GetByID(ctx context.Context, userID uuid.UUID) (User, error) {
	var u User
	err := r.pool.QueryRow(ctx, `
		SELECT user_id, org_id, email, role, created_at
		FROM org_users WHERE user_id = $1
	`, userID).Scan(&u.UserID, &u.OrgID, &u.Email, &u.Role, &u.CreatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return User{}, ErrNotFound
		}
		return User{}, fmt.Errorf("orgusers: get by id: %w", err)
	}
	return u, nil
}

// ListByOrg backs the dashboard's team page.
func (r *Repo) ListByOrg(ctx context.Context, orgID int64) ([]User, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT user_id, org_id, email, role, created_at
		FROM org_users WHERE org_id = $1 ORDER BY created_at
	`, orgID)
	if err != nil {
		return nil, fmt.Errorf("orgusers: list by org: %w", err)
	}
	defer rows.Close()

	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.UserID, &u.OrgID, &u.Email, &u.Role, &u.CreatedAt); err != nil {
			return nil, fmt.Errorf("orgusers: scan: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// CountOwners backs the "can't remove/demote the last owner" guard — an org
// with zero owners can never invite/remove anyone again, a dead end no
// action here should ever produce.
func (r *Repo) CountOwners(ctx context.Context, orgID int64) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `
		SELECT count(*) FROM org_users WHERE org_id = $1 AND role = $2
	`, orgID, RoleOwner).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("orgusers: count owners: %w", err)
	}
	return count, nil
}

// RemoveMember is scoped by orgID so a caller can never remove a member
// belonging to a different org — the same "check ownership, not just
// existence" shape apps.CredentialRepo.Revoke already uses.
func (r *Repo) RemoveMember(ctx context.Context, orgID int64, userID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM org_users WHERE org_id = $1 AND user_id = $2
	`, orgID, userID)
	if err != nil {
		return fmt.Errorf("orgusers: remove member: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation
}
