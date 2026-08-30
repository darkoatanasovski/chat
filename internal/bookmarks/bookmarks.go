// Package bookmarks owns the control-plane bookmark_folders/bookmarks
// tables (migrations/control/0011_bookmarks.sql) — a user's own private,
// folder-organized set of saved messages. Unlike internal/reactions or
// internal/messages' pin support, nothing here is ever broadcast to other
// channel members or denormalized onto a message row: a bookmark exists
// only for the user who created it, which is exactly why it lives in the
// control plane rather than a shard (see the migration's own doc comment
// for the full "avoid a cross-shard scatter-gather" reasoning, mirroring
// why user_channels already works this way).
//
// Repo binds one *pgxpool.Pool at construction, the same as
// internal/blocks and internal/membership — control-plane-only data with
// no per-call shard routing needed.
package bookmarks

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

const pgUniqueViolation = "23505"

var (
	// ErrFolderNotFound covers both "no such folder" and "that folder
	// belongs to a different user" — Repo never distinguishes the two to a
	// caller, the same "ownership-scoped WHERE clause, wrong owner just
	// matches zero rows" discipline as internal/blocks.Repo.Unblock.
	ErrFolderNotFound = errors.New("bookmarks: folder not found")
	// ErrFolderNameTaken is returned by CreateFolder when userID already
	// has a folder with that exact name (bookmark_folders' UNIQUE
	// (user_id, name) constraint) — folder names are how a user tells
	// their own folders apart, so silently allowing duplicates would make
	// the list ambiguous to them.
	ErrFolderNameTaken = errors.New("bookmarks: folder name already in use")
	// ErrBookmarkNotFound covers "no such bookmark" and "belongs to a
	// different user," same reasoning as ErrFolderNotFound.
	ErrBookmarkNotFound = errors.New("bookmarks: bookmark not found")
)

type Folder struct {
	FolderID  uuid.UUID
	AppID     int64
	UserID    uuid.UUID
	Name      string
	CreatedAt time.Time
}

type Bookmark struct {
	BookmarkID uuid.UUID
	AppID      int64
	UserID     uuid.UUID
	ChannelID  uuid.UUID
	MessageID  uuid.UUID
	// FolderID is nil for an "unfiled" bookmark — every bookmark starts
	// here unless created with a folder, and Move can send it back at any
	// time (see Move's doc comment).
	FolderID  *uuid.UUID
	CreatedAt time.Time
}

type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// CreateFolder makes a new, initially-empty folder for userID.
func (r *Repo) CreateFolder(ctx context.Context, appID int64, userID uuid.UUID, name string) (Folder, error) {
	f := Folder{FolderID: uuid.Nil, AppID: appID, UserID: userID, Name: name, CreatedAt: time.Now().UTC()}
	folderID, err := uuid.NewV7()
	if err != nil {
		return Folder{}, fmt.Errorf("bookmarks: generate folder id: %w", err)
	}
	f.FolderID = folderID
	if _, err := r.pool.Exec(ctx, `
		INSERT INTO bookmark_folders (folder_id, app_id, user_id, name, created_at)
		VALUES ($1, $2, $3, $4, $5)
	`, f.FolderID, f.AppID, f.UserID, f.Name, f.CreatedAt); err != nil {
		if isUniqueViolation(err) {
			return Folder{}, ErrFolderNameTaken
		}
		return Folder{}, fmt.Errorf("bookmarks: insert folder: %w", err)
	}
	return f, nil
}

// ListFolders returns userID's own folders, newest first.
func (r *Repo) ListFolders(ctx context.Context, userID uuid.UUID) ([]Folder, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT folder_id, app_id, user_id, name, created_at FROM bookmark_folders
		WHERE user_id = $1
		ORDER BY created_at DESC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("bookmarks: list folders: %w", err)
	}
	defer rows.Close()

	var out []Folder
	for rows.Next() {
		var f Folder
		if err := rows.Scan(&f.FolderID, &f.AppID, &f.UserID, &f.Name, &f.CreatedAt); err != nil {
			return nil, fmt.Errorf("bookmarks: scan folder: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// RenameFolder renames one of userID's own folders. The WHERE clause scopes
// to user_id = userID, so attempting to rename a folder that doesn't exist
// or belongs to someone else both come back as ErrFolderNotFound —
// indistinguishable to the caller, same as Unblock's ownership check.
func (r *Repo) RenameFolder(ctx context.Context, userID, folderID uuid.UUID, newName string) (Folder, error) {
	var f Folder
	err := r.pool.QueryRow(ctx, `
		UPDATE bookmark_folders SET name = $1
		WHERE folder_id = $2 AND user_id = $3
		RETURNING folder_id, app_id, user_id, name, created_at
	`, newName, folderID, userID).Scan(&f.FolderID, &f.AppID, &f.UserID, &f.Name, &f.CreatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return Folder{}, ErrFolderNotFound
		}
		if isUniqueViolation(err) {
			return Folder{}, ErrFolderNameTaken
		}
		return Folder{}, fmt.Errorf("bookmarks: rename folder: %w", err)
	}
	return f, nil
}

// DeleteFolder removes one of userID's own folders. Bookmarks that were
// filed in it are not deleted — bookmark_folders' ON DELETE SET NULL
// (migrations/control/0011_bookmarks.sql) un-files them back to "unfiled"
// instead, the same way emptying a real folder doesn't throw away what was
// inside it.
func (r *Repo) DeleteFolder(ctx context.Context, userID, folderID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM bookmark_folders WHERE folder_id = $1 AND user_id = $2
	`, folderID, userID)
	if err != nil {
		return fmt.Errorf("bookmarks: delete folder: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrFolderNotFound
	}
	return nil
}

// folderOwnedByUser confirms folderID exists and belongs to userID — the
// ownership check Create/Move both need before letting a bookmark
// reference a folder, since the bookmarks.folder_id foreign key alone only
// guarantees the folder exists, not that it's this user's own (nothing
// stops a caller from passing someone else's folder_id otherwise).
func (r *Repo) folderOwnedByUser(ctx context.Context, userID, folderID uuid.UUID) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM bookmark_folders WHERE folder_id = $1 AND user_id = $2)
	`, folderID, userID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("bookmarks: check folder ownership: %w", err)
	}
	return exists, nil
}

// Create bookmarks (channelID, messageID) for userID, optionally filing it
// directly into folderID (nil leaves it unfiled). The caller is expected to
// have already confirmed the message actually exists — via
// internal/messages.Repo.Exists — since bookmarks.message_id can't carry a
// foreign key across the control-plane/shard database boundary (see the
// migration's doc comment); Create itself does no such check.
//
// Idempotent: bookmarking a message that's already bookmarked by this user
// is a no-op returning the *existing* bookmark unchanged (created=false) —
// it does not move it into a newly-requested folder. Moving an existing
// bookmark is Move's job, not Create's, the same separation of concerns as
// internal/reactions.Repo.Add never changing an existing reaction's
// timestamp on a repeat call.
func (r *Repo) Create(ctx context.Context, appID int64, userID, channelID, messageID uuid.UUID, folderID *uuid.UUID) (Bookmark, bool, error) {
	if folderID != nil {
		owned, err := r.folderOwnedByUser(ctx, userID, *folderID)
		if err != nil {
			return Bookmark{}, false, err
		}
		if !owned {
			return Bookmark{}, false, ErrFolderNotFound
		}
	}

	bookmarkID, err := uuid.NewV7()
	if err != nil {
		return Bookmark{}, false, fmt.Errorf("bookmarks: generate bookmark id: %w", err)
	}
	now := time.Now().UTC()
	tag, err := r.pool.Exec(ctx, `
		INSERT INTO bookmarks (bookmark_id, app_id, user_id, channel_id, message_id, folder_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (user_id, channel_id, message_id) DO NOTHING
	`, bookmarkID, appID, userID, channelID, messageID, folderID, now)
	if err != nil {
		return Bookmark{}, false, fmt.Errorf("bookmarks: insert: %w", err)
	}
	if tag.RowsAffected() == 0 {
		existing, err := r.getByMessage(ctx, userID, channelID, messageID)
		if err != nil {
			return Bookmark{}, false, err
		}
		return existing, false, nil
	}

	return Bookmark{
		BookmarkID: bookmarkID, AppID: appID, UserID: userID, ChannelID: channelID,
		MessageID: messageID, FolderID: folderID, CreatedAt: now,
	}, true, nil
}

func (r *Repo) getByMessage(ctx context.Context, userID, channelID, messageID uuid.UUID) (Bookmark, error) {
	var b Bookmark
	err := r.pool.QueryRow(ctx, `
		SELECT bookmark_id, app_id, user_id, channel_id, message_id, folder_id, created_at
		FROM bookmarks WHERE user_id = $1 AND channel_id = $2 AND message_id = $3
	`, userID, channelID, messageID).Scan(&b.BookmarkID, &b.AppID, &b.UserID, &b.ChannelID, &b.MessageID, &b.FolderID, &b.CreatedAt)
	if err != nil {
		return Bookmark{}, fmt.Errorf("bookmarks: read existing: %w", err)
	}
	return b, nil
}

// List returns every one of userID's bookmarks, newest first, regardless
// of folder — the "show me everything I've saved" view.
func (r *Repo) List(ctx context.Context, userID uuid.UUID) ([]Bookmark, error) {
	return r.query(ctx, `
		SELECT bookmark_id, app_id, user_id, channel_id, message_id, folder_id, created_at
		FROM bookmarks WHERE user_id = $1
		ORDER BY created_at DESC
	`, userID)
}

// ListByFolder returns userID's bookmarks scoped to one folder — folderID
// nil means "unfiled" (folder_id IS NULL) rather than "no filter"; List is
// the no-filter case. Ownership of a non-nil folderID isn't re-checked
// here: a folder_id belonging to a different user simply can't match any
// of userID's own bookmarks, so the WHERE clause already excludes it.
func (r *Repo) ListByFolder(ctx context.Context, userID uuid.UUID, folderID *uuid.UUID) ([]Bookmark, error) {
	if folderID == nil {
		return r.query(ctx, `
			SELECT bookmark_id, app_id, user_id, channel_id, message_id, folder_id, created_at
			FROM bookmarks WHERE user_id = $1 AND folder_id IS NULL
			ORDER BY created_at DESC
		`, userID)
	}
	return r.query(ctx, `
		SELECT bookmark_id, app_id, user_id, channel_id, message_id, folder_id, created_at
		FROM bookmarks WHERE user_id = $1 AND folder_id = $2
		ORDER BY created_at DESC
	`, userID, *folderID)
}

func (r *Repo) query(ctx context.Context, sql string, args ...any) ([]Bookmark, error) {
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("bookmarks: list: %w", err)
	}
	defer rows.Close()

	var out []Bookmark
	for rows.Next() {
		var b Bookmark
		if err := rows.Scan(&b.BookmarkID, &b.AppID, &b.UserID, &b.ChannelID, &b.MessageID, &b.FolderID, &b.CreatedAt); err != nil {
			return nil, fmt.Errorf("bookmarks: scan: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Move re-files one of userID's own bookmarks into a different folder
// (folderID nil un-files it back to "unfiled"). Ownership of both the
// bookmark (the WHERE clause) and, when set, the destination folder (the
// same check Create uses) are verified — a caller can only reorganize
// their own bookmarks into their own folders.
func (r *Repo) Move(ctx context.Context, userID, bookmarkID uuid.UUID, folderID *uuid.UUID) (Bookmark, error) {
	if folderID != nil {
		owned, err := r.folderOwnedByUser(ctx, userID, *folderID)
		if err != nil {
			return Bookmark{}, err
		}
		if !owned {
			return Bookmark{}, ErrFolderNotFound
		}
	}

	var b Bookmark
	err := r.pool.QueryRow(ctx, `
		UPDATE bookmarks SET folder_id = $1
		WHERE bookmark_id = $2 AND user_id = $3
		RETURNING bookmark_id, app_id, user_id, channel_id, message_id, folder_id, created_at
	`, folderID, bookmarkID, userID).Scan(&b.BookmarkID, &b.AppID, &b.UserID, &b.ChannelID, &b.MessageID, &b.FolderID, &b.CreatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return Bookmark{}, ErrBookmarkNotFound
		}
		return Bookmark{}, fmt.Errorf("bookmarks: move: %w", err)
	}
	return b, nil
}

// Delete removes one of userID's own bookmarks.
func (r *Repo) Delete(ctx context.Context, userID, bookmarkID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM bookmarks WHERE bookmark_id = $1 AND user_id = $2
	`, bookmarkID, userID)
	if err != nil {
		return fmt.Errorf("bookmarks: delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrBookmarkNotFound
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation
}
