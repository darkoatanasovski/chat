package apps

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/darkoatanasovski/chat/internal/platform/secretbox"
)

// ErrCredentialNotFound covers "no such key", "revoked", and "secret
// mismatch" uniformly — the caller (an unauthenticated HTTP request) must
// not be able to distinguish which case it hit.
var ErrCredentialNotFound = fmt.Errorf("apps: credential not found or revoked")

type Credential struct {
	CredentialID uuid.UUID
	AppID        int64
	Key          string
	CreatedAt    time.Time
	RevokedAt    *time.Time
}

// IssuedCredential is what Create returns — the only response that ever
// carries the secret directly, without a Reveal round-trip, since it's
// already in hand right after minting it.
type IssuedCredential struct {
	Credential
	Secret string
}

type CredentialRepo struct {
	pool *pgxpool.Pool
	box  *secretbox.Box
}

// NewCredentialRepo takes a secretbox.Box so Create can store a recoverable
// (encrypted, not just hashed) copy of every secret it mints — see Reveal
// below and migrations/control/0008_app_credential_secret_encrypted.sql for
// why both a hash and an encrypted copy are kept side by side.
func NewCredentialRepo(pool *pgxpool.Pool, box *secretbox.Box) *CredentialRepo {
	return &CredentialRepo{pool: pool, box: box}
}

func (r *CredentialRepo) Create(ctx context.Context, appID int64) (IssuedCredential, error) {
	key, err := randomToken("key_", 16)
	if err != nil {
		return IssuedCredential{}, err
	}
	secret, err := randomToken("secret_", 32)
	if err != nil {
		return IssuedCredential{}, err
	}
	credentialID := uuid.New()

	encrypted, err := r.box.Seal(secret)
	if err != nil {
		return IssuedCredential{}, fmt.Errorf("apps: encrypt credential secret: %w", err)
	}

	var createdAt time.Time
	err = r.pool.QueryRow(ctx, `
		INSERT INTO app_credentials (credential_id, app_id, key, secret_hash, secret_encrypted)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING created_at
	`, credentialID, appID, key, hashSecret(secret), encrypted).Scan(&createdAt)
	if err != nil {
		return IssuedCredential{}, fmt.Errorf("apps: create credential: %w", err)
	}

	return IssuedCredential{
		Credential: Credential{CredentialID: credentialID, AppID: appID, Key: key, CreatedAt: createdAt},
		Secret:     secret,
	}, nil
}

// Verify looks up key and, only if it exists, isn't revoked, and secret
// matches its stored hash, returns the credential's app_id. This is checked
// live against Postgres on every call rather than trusting a signed token —
// that's what makes Revoke take effect immediately (INSTRUCTIONS.md §43: a
// revoked credential must never be honored again; a JWT can't be
// invalidated before its own expiry without a separate blocklist, which
// would just reintroduce the same live-lookup requirement this avoids).
func (r *CredentialRepo) Verify(ctx context.Context, key, secret string) (int64, error) {
	var appID int64
	var secretHash string
	var revokedAt *time.Time
	err := r.pool.QueryRow(ctx, `
		SELECT app_id, secret_hash, revoked_at FROM app_credentials WHERE key = $1
	`, key).Scan(&appID, &secretHash, &revokedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return 0, ErrCredentialNotFound
		}
		return 0, fmt.Errorf("apps: verify credential: %w", err)
	}
	if revokedAt != nil {
		return 0, ErrCredentialNotFound
	}
	if subtle.ConstantTimeCompare([]byte(hashSecret(secret)), []byte(secretHash)) != 1 {
		return 0, ErrCredentialNotFound
	}
	return appID, nil
}

// IsActive is the live half of app-JWT verification (requireAppJWT): the
// JWT's signature already proves api_key/app_id weren't tampered with since
// POST /apps/token minted it, but only a fresh read here can prove the
// credential hasn't been revoked since — same immediate-revocation
// guarantee Verify already gives Basic auth, deliberately paid again here
// (see the auth package's doc comment for why a signed token can't avoid
// this the way an unsigned one could pretend to).
func (r *CredentialRepo) IsActive(ctx context.Context, appID int64, key string) (bool, error) {
	var revokedAt *time.Time
	err := r.pool.QueryRow(ctx, `
		SELECT revoked_at FROM app_credentials WHERE key = $1 AND app_id = $2
	`, key, appID).Scan(&revokedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return false, nil
		}
		return false, fmt.Errorf("apps: check credential active: %w", err)
	}
	return revokedAt == nil, nil
}

// Reveal decrypts and returns a credential's secret on demand, scoped to
// appID the same way Revoke is (ownership check baked into the WHERE
// clause, not a separate lookup+compare) so one org can never reveal
// another's credential by guessing a credential_id. Works for revoked
// credentials too — revocation is about stopping future use, not about
// hiding what the value was from the org that owns it.
//
// Returns ErrCredentialNotFound both when no such row exists AND when the
// row predates secret_encrypted being populated (migrated-in-place rows
// created before 0008_app_credential_secret_encrypted.sql only have a
// hash) — the caller can't do anything useful with either case beyond
// telling the user to rotate the credential.
func (r *CredentialRepo) Reveal(ctx context.Context, appID int64, credentialID uuid.UUID) (string, error) {
	var encrypted []byte
	err := r.pool.QueryRow(ctx, `
		SELECT secret_encrypted FROM app_credentials WHERE credential_id = $1 AND app_id = $2
	`, credentialID, appID).Scan(&encrypted)
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", ErrCredentialNotFound
		}
		return "", fmt.Errorf("apps: reveal credential: %w", err)
	}
	if len(encrypted) == 0 {
		return "", ErrCredentialNotFound
	}
	secret, err := r.box.Open(encrypted)
	if err != nil {
		return "", fmt.Errorf("apps: decrypt credential secret: %w", err)
	}
	return secret, nil
}

// ListByApp never returns secrets or their hashes — only what's needed to
// let an org tell its credentials apart (key, created_at, revoked_at).
func (r *CredentialRepo) ListByApp(ctx context.Context, appID int64) ([]Credential, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT credential_id, app_id, key, created_at, revoked_at
		FROM app_credentials WHERE app_id = $1 ORDER BY created_at
	`, appID)
	if err != nil {
		return nil, fmt.Errorf("apps: list credentials: %w", err)
	}
	defer rows.Close()

	var out []Credential
	for rows.Next() {
		var c Credential
		if err := rows.Scan(&c.CredentialID, &c.AppID, &c.Key, &c.CreatedAt, &c.RevokedAt); err != nil {
			return nil, fmt.Errorf("apps: scan credential: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Revoke is scoped to appID so a caller can never revoke a credential
// belonging to a different app by guessing a credential_id — the same
// "check ownership, not just existence" shape as every channel-scoped
// handler already uses for membership.
func (r *CredentialRepo) Revoke(ctx context.Context, appID int64, credentialID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE app_credentials SET revoked_at = now()
		WHERE credential_id = $1 AND app_id = $2 AND revoked_at IS NULL
	`, credentialID, appID)
	if err != nil {
		return fmt.Errorf("apps: revoke credential: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrCredentialNotFound
	}
	return nil
}

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// randomToken is used for both the key (semi-public identifier) and the
// secret (shown once) — both are high-entropy random bytes, so a fast hash
// (SHA-256, above) is the right tool for storing the secret, unlike a
// human-chosen password where a slow hash (bcrypt/argon2) would matter.
func randomToken(prefix string, n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("apps: generate token: %w", err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(buf), nil
}
