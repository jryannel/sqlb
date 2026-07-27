// Package migrate turns a schema change into migration files for an existing
// migration runner.
//
// There are three layers. Diff compares two schema registries and returns the
// Changes between them. The DDL layer renders those changes as Postgres
// statements. A Format renders a set of changes as the files a particular
// runner expects. They are separable on purpose: the first is a pure function
// over two data structures, the second knows only Postgres, and the third
// knows only goose or golang-migrate.
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

	// Lock names the lock the statement takes when holding it costs
	// something — when the time it is held grows with the number of rows in
	// the table rather than being a catalog write. Most changes leave it "".
	//
	// The generator cannot know whether this matters: a full scan of a
	// thousand rows is free and a full scan of a billion is an outage, and
	// nothing in a schema says which table is which. So a locking change is
	// rendered live with the lock named above it, not commented out like a
	// destructive one. Destructive is commented out because applying it is
	// irreversible; this is reversible, it is just occasionally very slow.
	// Use Migration.Blocking to gate on it where the table sizes are known.
	//
	// The lock is held until the transaction commits, not until the statement
	// finishes — so everything else in the same file waits behind it.
	Lock string
	// Hazard explains what the lock costs and what to do on a table too large
	// to hold it, and is required when Lock is set.
	Hazard string
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
		if c.Lock != "" && c.Hazard == "" {
			return fmt.Errorf("migrate: locking change %d of %s_%s does not say what the lock costs", i, m.Version, m.Name)
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

// Blocking returns the changes that hold a lock for a time proportional to the
// size of the table, in the order they are applied.
//
// It is the hook for a policy this package cannot have: whether a full scan is
// acceptable depends on how many rows the table holds, which is not in the
// schema. A project that knows its big tables can refuse a migration touching
// one, or route it to whoever sequences an expand/contract rollout.
func (m Migration) Blocking() []Change {
	var out []Change
	for _, c := range m.Changes {
		if c.Lock != "" {
			out = append(out, c)
		}
	}
	return out
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
	// Wrapped to the same column as a lock hazard: these two are the notes a
	// reviewer has to actually read, and one of them running off the screen
	// while the other does not is a good way to have it skipped.
	for _, line := range wrap("DESTRUCTIVE: "+c.Reason, 74, "  ") {
		b.WriteString("-- " + line + "\n")
	}
	b.WriteString("-- Review, then uncomment to apply. Generated commented out on purpose.\n")
	for _, line := range strings.Split(sql, "\n") {
		b.WriteString("-- " + line + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// upComment renders the comment block above a change's Up: what the change is
// for, and what its lock costs when that is worth knowing before applying it.
//
// The hazard is stated here rather than commenting the statement out, because
// the fact that matters — how many rows the table holds — is not in the schema.
// A migration nobody can apply without editing trains people to edit without
// reading, which is how the destructive guard would stop working too.
func upComment(c Change) string {
	var lines []string
	if c.Comment != "" {
		lines = wrap(c.Comment, 74, "  ")
	}
	if c.Lock != "" {
		lines = append(lines, wrap("LOCK "+c.Lock+": "+c.Hazard, 74, "  ")...)
	}
	return strings.Join(lines, "\n")
}

// wrap breaks text into lines of at most width characters, indenting every line
// after the first, so that a hazard note reads as a paragraph in a file someone
// reviews rather than as one line they scroll past.
func wrap(s string, width int, indent string) []string {
	var out []string
	line := ""
	for _, word := range strings.Fields(s) {
		switch {
		case line == "":
			line = word
		case len(line)+1+len(word) <= width:
			line += " " + word
		default:
			out = append(out, line)
			line = indent + word
		}
	}
	if line != "" {
		out = append(out, line)
	}
	return out
}

// needsStatementBlock reports whether SQL contains a semicolon anywhere but the
// end, which is how a runner tells one statement from several. Function bodies
// and DO blocks need explicit delimiting.
//
// Line comments are stripped first. A semicolon inside one does not delimit
// anything, and prose reaches here routinely: a destructive change renders as
// nothing but comment lines, and its Reason is written by a human.
func needsStatementBlock(sql string) bool {
	var code []string
	for _, line := range strings.Split(sql, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		code = append(code, line)
	}
	trimmed := strings.TrimRight(strings.TrimSpace(strings.Join(code, "\n")), ";")
	return strings.Contains(trimmed, ";")
}

const header = "-- Generated by sqlb. Review before applying."
