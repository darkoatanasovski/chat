package orgusers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// InviteTTL is how long an invite link stays acceptable — generous since
// there's no email delivery to retry; the owner shares the link manually
// and a stale one just needs a new invite created.
const InviteTTL = 7 * 24 * time.Hour

var ErrInviteNotFound = errors.New("orgusers: invite not found, expired, or already accepted")

type Invite struct {
	InviteID   uuid.UUID
	OrgID      int64
	Email      string
	Role       string
	InvitedBy  uuid.UUID
	CreatedAt  time.Time
	ExpiresAt  time.Time
	AcceptedAt *time.Time
}

type InviteRepo struct {
	pool *pgxpool.Pool
}

func NewInviteRepo(pool *pgxpool.Pool) *InviteRepo {
	return &InviteRepo{pool: pool}
}

// Create mints a new invite and returns its one-time raw token alongside
// the row — only the token's hash is ever stored, the same "shown once,
// never retrievable again" guarantee apps.CredentialRepo.Create makes for
// app credentials. There's no email infrastructure in this platform, so the
// caller (cmd/api) hands the token back to the inviting owner directly to
// share manually rather than this sending mail.
func (r *InviteRepo) Create(ctx context.Context, orgID int64, email, role string, invitedBy uuid.UUID) (Invite, string, error) {
	inviteID, err := uuid.NewV7()
	if err != nil {
		return Invite{}, "", fmt.Errorf("orgusers: generate invite id: %w", err)
	}
	token, err := randomToken(32)
	if err != nil {
		return Invite{}, "", err
	}

	inv := Invite{InviteID: inviteID, OrgID: orgID, Email: email, Role: role, InvitedBy: invitedBy}
	inv.ExpiresAt = time.Now().UTC().Add(InviteTTL)
	err = r.pool.QueryRow(ctx, `
		INSERT INTO org_invites (invite_id, org_id, email, token_hash, role, invited_by, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING created_at
	`, inviteID, orgID, email, hashToken(token), role, invitedBy, inv.ExpiresAt).Scan(&inv.CreatedAt)
	if err != nil {
		return Invite{}, "", fmt.Errorf("orgusers: create invite: %w", err)
	}
	return inv, token, nil
}

// GetByToken looks up an invite by its raw token (hashing it first) and
// returns ErrInviteNotFound uniformly for "no such token", "expired", and
// "already accepted" — the caller (an unauthenticated accept request) must
// not be able to distinguish which case it hit, the same reasoning
// apps.CredentialRepo.Verify already applies to app credentials.
func (r *InviteRepo) GetByToken(ctx context.Context, token string) (Invite, error) {
	var inv Invite
	err := r.pool.QueryRow(ctx, `
		SELECT invite_id, org_id, email, role, invited_by, created_at, expires_at, accepted_at
		FROM org_invites WHERE token_hash = $1
	`, hashToken(token)).Scan(&inv.InviteID, &inv.OrgID, &inv.Email, &inv.Role, &inv.InvitedBy, &inv.CreatedAt, &inv.ExpiresAt, &inv.AcceptedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return Invite{}, ErrInviteNotFound
		}
		return Invite{}, fmt.Errorf("orgusers: get invite: %w", err)
	}
	if inv.AcceptedAt != nil || time.Now().UTC().After(inv.ExpiresAt) {
		return Invite{}, ErrInviteNotFound
	}
	return inv, nil
}

func (r *InviteRepo) MarkAccepted(ctx context.Context, inviteID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE org_invites SET accepted_at = now() WHERE invite_id = $1 AND accepted_at IS NULL
	`, inviteID)
	if err != nil {
		return fmt.Errorf("orgusers: mark invite accepted: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrInviteNotFound
	}
	return nil
}

// ListPendingByOrg backs the dashboard's team page — invites sent but not
// yet accepted (and not yet expired), so the owner can see who they're
// still waiting on.
func (r *InviteRepo) ListPendingByOrg(ctx context.Context, orgID int64) ([]Invite, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT invite_id, org_id, email, role, invited_by, created_at, expires_at, accepted_at
		FROM org_invites
		WHERE org_id = $1 AND accepted_at IS NULL AND expires_at > now()
		ORDER BY created_at
	`, orgID)
	if err != nil {
		return nil, fmt.Errorf("orgusers: list pending invites: %w", err)
	}
	defer rows.Close()

	var out []Invite
	for rows.Next() {
		var inv Invite
		if err := rows.Scan(&inv.InviteID, &inv.OrgID, &inv.Email, &inv.Role, &inv.InvitedBy, &inv.CreatedAt, &inv.ExpiresAt, &inv.AcceptedAt); err != nil {
			return nil, fmt.Errorf("orgusers: scan invite: %w", err)
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func randomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("orgusers: generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
