// Package migrations embeds the SQL migration files so they ship inside the
// single `chat` binary and can be applied on service startup
// (internal/platform/migrate), rather than only by the out-of-band psql
// script (deploy/railway/migrate.sh, still valid for CI/manual runs).
package migrations

import "embed"

//go:embed cell/*.sql config/*.sql
var FS embed.FS
