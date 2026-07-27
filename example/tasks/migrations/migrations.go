// Package migrations carries the checked-in migration history and applies it
// with goose.
//
// The files are generated — `go run ./cmd/migrate` — and applied by a runner
// sqlb has nothing to do with. That split is the point of the migrate package:
// it produces files for the runner a project already has, because replacing a
// working migration runner is a much larger ask than adopting a code generator,
// and offers nothing in return.
//
// Embedding them means the binary and its schema ship together, so `go run
// ./cmd/server` against an empty database works with no separate step and no
// migration directory to lose.
package migrations

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	"github.com/pressly/goose/v3"
)

//go:embed *.sql
var files embed.FS

// FS exposes the embedded migrations, for a caller wanting to run goose itself.
func FS() embed.FS { return files }

// Apply brings the database up to the latest migration.
//
// goose records what it has applied in its own version table, so this is safe
// to call at every start: an up-to-date database is a no-op and a fresh one is
// built from nothing.
//
// Doing it at startup suits a demo and a single-instance service. It does not
// suit a rolling deploy, where several new instances would race to apply the
// same migration and the old code would briefly run against the new schema —
// there, migrations are a deployment step that finishes before any new instance
// starts. Saying that here is cheaper than someone finding out later.
func Apply(ctx context.Context, db *sql.DB) error {
	goose.SetBaseFS(files)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("migrations: %w", err)
	}
	if err := goose.UpContext(ctx, db, "."); err != nil {
		return fmt.Errorf("migrations: applying: %w", err)
	}
	return nil
}

// Reset rolls every migration back, for tests that want a clean database
// without recreating one.
//
// It is not called by the server and should not be: the Down sections drop
// every table in the schema.
func Reset(ctx context.Context, db *sql.DB) error {
	goose.SetBaseFS(files)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("migrations: %w", err)
	}
	if err := goose.DownToContext(ctx, db, ".", 0); err != nil {
		return fmt.Errorf("migrations: rolling back: %w", err)
	}
	return nil
}
