package migrate

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/darkoatanasovski/chat/migrations"
)

// TestApply_CellMigrations runs the embedded cell migrations against a real
// Postgres (TEST_PG_DSN), then re-runs to prove idempotency, then simulates a
// pre-tracking DB to prove baselining. Skipped when TEST_PG_DSN is unset.
func TestApply_CellMigrations(t *testing.T) {
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		t.Skip("set TEST_PG_DSN to run")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if err := Apply(ctx, pool, migrations.FS, "cell", "messages", 990001, log); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	// columns exist
	for _, q := range []string{
		"SELECT custom FROM messages LIMIT 0",
		"SELECT body_tsv FROM messages LIMIT 0",
		"SELECT visibility, custom FROM channels LIMIT 0",
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("expected column present (%q): %v", q, err)
		}
	}
	// idempotent re-run
	if err := Apply(ctx, pool, migrations.FS, "cell", "messages", 990001, log); err != nil {
		t.Fatalf("second apply (idempotent): %v", err)
	}

	// baseline path: wipe the tracking table but keep the schema
	if _, err := pool.Exec(ctx, "DELETE FROM schema_migrations"); err != nil {
		t.Fatal(err)
	}
	if err := Apply(ctx, pool, migrations.FS, "cell", "messages", 990001, log); err != nil {
		t.Fatalf("baseline apply: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n < 2 {
		t.Fatalf("expected init baselined + 0002 recorded, got %d rows", n)
	}
}

// TestApply_ConfigMigrations runs the config-DB migrations on their own
// database (config and cell are separate databases in prod, so their
// like-named files never share a schema_migrations table).
func TestApply_ConfigMigrations(t *testing.T) {
	dsn := os.Getenv("TEST_PG_DSN")
	if dsn == "" {
		t.Skip("set TEST_PG_DSN to run")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = admin.Exec(ctx, "CREATE DATABASE cfgtest")
	admin.Close()

	cfgDSN := dsn[:strings.LastIndex(dsn, "/")] + "/cfgtest"
	pool, err := pgxpool.New(ctx, cfgDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if err := Apply(ctx, pool, migrations.FS, "config", "apps", 990002, log); err != nil {
		t.Fatalf("config apply: %v", err)
	}
	if _, err := pool.Exec(ctx, "SELECT retention_days FROM apps LIMIT 0"); err != nil {
		t.Fatalf("expected apps.retention_days present: %v", err)
	}
}
