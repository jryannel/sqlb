// Package migrate renders schema changes as migration files for an existing
// migration runner.
//
// sqlb does not apply migrations and does not track which have run. Projects
// already have a runner — goose, golang-migrate, atlas, a shell script — and
// replacing a working one is a far larger ask than adopting a code generator,
// for no benefit sqlb could offer. This package produces files; your runner
// applies them.
//
// Goose is the default because it is what this project's authors use, and
// because its single-file Up/Down format is the one most likely to be pasted
// into by hand afterwards.
package migrate

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Change is one schema alteration, with the SQL to apply and reverse it.
type Change struct {
	// Up is the forward SQL. Required.
	Up string
	// Down reverses it. An empty Down renders a comment explaining that the
	// change is not automatically reversible, rather than a silently missing
	// section — a Down that does nothing is worse than one that says why.
	Down string

	// Comment explains what the change is for, rendered above the SQL.
	Comment string

	// Destructive marks a change that can lose data: dropping a column or
	// table, narrowing a type, adding NOT NULL without a default. Destructive
	// changes render commented out unless explicitly allowed.
	Destructive bool
	// Reason explains the danger, and is required when Destructive is set.
	Reason string

	// Concurrent marks SQL that cannot run inside a transaction, which in
	// practice means CREATE INDEX CONCURRENTLY and DROP INDEX CONCURRENTLY.
	// Building an index without CONCURRENTLY takes a lock that blocks writes
	// for the duration, so on a live table this is not optional.
	Concurrent bool
}

// Migration is an ordered set of changes released together.
type Migration struct {
	Version string
	Name    string
	Changes []Change
}

// TimestampVersion renders goose's default version format.
func TimestampVersion(t time.Time) string { return t.UTC().Format("20060102150405") }

// SequentialVersion renders the zero-padded sequential format, used with
// goose's -s flag.
func SequentialVersion(n int) string { return fmt.Sprintf("%05d", n) }

// Options control rendering.
type Options struct {
	// Format defaults to Goose.
	Format Format
	// AllowDestructive emits destructive SQL live instead of commented out.
	// It exists so that dropping a column is a deliberate act with a flag
	// attached, not something that happens because a generator decided it.
	AllowDestructive bool
}

func (o Options) format() Format {
	if o.Format == nil {
		return Goose
	}
	return o.Format
}

// Split separates changes that cannot share a file.
//
// Transaction control in both goose and golang-migrate is per file, not per
// statement. A migration containing CREATE INDEX CONCURRENTLY must therefore
// disable transactions for everything in it — which would silently remove the
// rollback guarantee from every other change that happened to be generated at
// the same time. Splitting them keeps the ordinary changes transactional.
//
// Concurrent changes come last, so the tables and columns they index exist by
// the time they run.
func Split(m Migration) []Migration {
	var ordinary, concurrent []Change
	for _, c := range m.Changes {
		if c.Concurrent {
			concurrent = append(concurrent, c)
			continue
		}
		ordinary = append(ordinary, c)
	}

	switch {
	case len(concurrent) == 0:
		return []Migration{m}
	case len(ordinary) == 0:
		return []Migration{m}
	}

	// The second file's version must sort after the first, and both must be
	// stable across runs, so it is derived rather than taken from a clock.
	return []Migration{
		{Version: m.Version, Name: m.Name, Changes: ordinary},
		{Version: bumpVersion(m.Version), Name: m.Name + "_indexes", Changes: concurrent},
	}
}

// bumpVersion increments the last digit so the follow-up file sorts after its
// parent without needing a second timestamp.
func bumpVersion(v string) string {
	if v == "" {
		return "1"
	}
	digits := []byte(v)
	for i := len(digits) - 1; i >= 0; i-- {
		if digits[i] < '0' || digits[i] > '9' {
			break
		}
		if digits[i] < '9' {
			digits[i]++
			return string(digits)
		}
		digits[i] = '0'
	}
	return v + "1"
}

// Format renders a migration to one or more files.
type Format interface {
	// Name identifies the format in diagnostics and configuration.
	Name() string
	// Render returns filename → contents.
	Render(m Migration, opts Options) (map[string]string, error)
}

// Render produces the files for a migration, splitting where required.
func Render(m Migration, opts Options) (map[string]string, error) {
	if err := m.validate(); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, part := range Split(m) {
		files, err := opts.format().Render(part, opts)
		if err != nil {
			return nil, err
		}
		for name, body := range files {
			if _, clash := out[name]; clash {
				return nil, fmt.Errorf("migrate: two files named %q", name)
			}
			out[name] = body
		}
	}
	return out, nil
}

// Write renders a migration into dir.
func Write(dir string, m Migration, opts Options) ([]string, error) {
	files, err := Render(m, opts)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		path := filepath.Join(dir, name)
		// Refusing to overwrite matters here: a migration that has already
		// been applied somewhere must not change under the runner's feet.
		if _, err := os.Stat(path); err == nil {
			return nil, fmt.Errorf("migrate: %s already exists; migrations are append-only once applied", path)
		}
		if err := os.WriteFile(path, []byte(files[name]), 0o644); err != nil {
			return nil, err
		}
	}
	return names, nil
}

func (m Migration) validate() error {
	if m.Version == "" {
		return fmt.Errorf("migrate: migration has no version")
	}
	if m.Name == "" {
		return fmt.Errorf("migrate: migration %s has no name", m.Version)
	}
	if len(m.Changes) == 0 {
		return fmt.Errorf("migrate: migration %s_%s has no changes", m.Version, m.Name)
	}
	for i, c := range m.Changes {
		if strings.TrimSpace(c.Up) == "" {
			return fmt.Errorf("migrate: change %d of %s_%s has no Up SQL", i, m.Version, m.Name)
		}
		if c.Destructive && c.Reason == "" {
			return fmt.Errorf("migrate: destructive change %d of %s_%s gives no reason", i, m.Version, m.Name)
		}
	}
	return nil
}

// Destructive reports whether any change can lose data.
func (m Migration) Destructive() bool {
	for _, c := range m.Changes {
		if c.Destructive {
			return true
		}
	}
	return false
}

// statement renders one change's SQL for a direction, commenting it out when
// destructive changes are not allowed.
func statement(sql string, c Change, opts Options) string {
	sql = strings.TrimSpace(sql)
	if sql == "" {
		return ""
	}
	if !c.Destructive || opts.AllowDestructive {
		return sql
	}
	var b strings.Builder
	b.WriteString("-- DESTRUCTIVE: " + c.Reason + "\n")
	b.WriteString("-- Review, then uncomment to apply. Generated commented out on purpose.\n")
	for _, line := range strings.Split(sql, "\n") {
		b.WriteString("-- " + line + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// needsStatementBlock reports whether SQL contains a semicolon anywhere but the
// end, which is how a runner tells one statement from several. Function bodies
// and DO blocks need explicit delimiting.
func needsStatementBlock(sql string) bool {
	trimmed := strings.TrimRight(strings.TrimSpace(sql), ";")
	return strings.Contains(trimmed, ";")
}

const header = "-- Generated by sqlb. Review before applying."
