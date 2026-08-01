// Package migrations carries the checked-in migration history.
//
// The files are generated — `go run ./cmd/migrate` — and applied by goose,
// which sqlb has nothing to do with. That split is the point of the migrate
// package: it produces files for the runner a project already has, because
// replacing a working migration runner is a much larger ask than adopting a
// code generator and offers nothing in return.
//
// Embedding them means the binary and its schema ship together, so `go run
// ./cmd/server` against an empty database works with no separate step.
package migrations

import "embed"

//go:embed *.sql
var files embed.FS

// FS exposes the embedded history. The store module wraps it in a
// sqlbfx.MigrationSet; nothing here knows what fx is.
func FS() embed.FS { return files }
