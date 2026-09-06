// Package migrate applies embedded SQL migrations on service startup, under a
// Postgres advisory lock so concurrent replicas can't race, tracked in the same
// schema_migrations table deploy/railway/migrate.sh uses (so the two mechanisms
// interoperate). Additive-only migrations plus fail-fast on error keep a bad
// migration from serving a half-migrated schema.
package migrate

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Apply runs every not-yet-applied *.sql file in fsys/dir (lexical order) against
// pool, recording each in schema_migrations. lockKey namespaces the advisory
// lock per database. sentinelTable names a table the init migration creates: if
// schema_migrations is empty but that table already exists (a DB migrated before
// this runner existed, e.g. by the psql script), the init file is baselined
// (recorded, not re-run) so we never try to re-CREATE existing objects.
func Apply(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS, dir, sentinelTable string, lockKey int64, log *slog.Logger) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("migrate: acquire conn: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", lockKey); err != nil {
		return fmt.Errorf("migrate: advisory lock: %w", err)
	}
	defer func() { _, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", lockKey) }()

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		filename TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("migrate: ensure schema_migrations: %w", err)
	}

	applied := map[string]bool{}
	rows, err := conn.Query(ctx, "SELECT filename FROM schema_migrations")
	if err != nil {
		return fmt.Errorf("migrate: read schema_migrations: %w", err)
	}
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			rows.Close()
			return fmt.Errorf("migrate: scan schema_migrations: %w", err)
		}
		applied[f] = true
	}
	rows.Close()

	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return fmt.Errorf("migrate: read dir %s: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	// Baseline a pre-tracking DB: init schema present, nothing recorded.
	if len(applied) == 0 && len(names) > 0 {
		var exists bool
		if err := conn.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", sentinelTable).Scan(&exists); err != nil {
			return fmt.Errorf("migrate: sentinel check: %w", err)
		}
		if exists {
			if _, err := conn.Exec(ctx, "INSERT INTO schema_migrations(filename) VALUES($1) ON CONFLICT DO NOTHING", names[0]); err != nil {
				return fmt.Errorf("migrate: baseline record: %w", err)
			}
			applied[names[0]] = true
			log.Info("migrate: baselined pre-existing schema", "dir", dir, "baseline", names[0])
		}
	}

	for _, name := range names {
		if applied[name] {
			continue
		}
		body, err := fs.ReadFile(fsys, dir+"/"+name)
		if err != nil {
			return fmt.Errorf("migrate: read %s: %w", name, err)
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("migrate: begin %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migrate: apply %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations(filename) VALUES($1)", name); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migrate: record %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("migrate: commit %s: %w", name, err)
		}
		log.Info("migrate: applied", "dir", dir, "file", name)
	}
	return nil
}
