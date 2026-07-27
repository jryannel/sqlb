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

// Stage says which file a change belongs in.
//
// Transaction control is per file in every runner this package targets, so
// "a different transaction" and "a different file" are the same thing here —
// which is why this is a property of a change rather than of a statement.
type Stage int

const (
	// StageMain is an ordinary change, applied in the migration's own
	// transaction along with everything else. Almost everything is this.
	StageMain Stage = iota

	// StageValidate is the second half of a NOT VALID sequence: the statement
	// that scans the table to prove a constraint holds. It has to run in a
	// later transaction than the ADD CONSTRAINT it validates, because the brief
	// ACCESS EXCLUSIVE that the add takes is held until its transaction
	// commits — validating inside that transaction would hold the strong lock
	// for the length of the scan, which is the thing the sequence exists to
	// avoid.
	StageValidate

	// StageFinish is the cheap remainder of a sequence whose scanning is done:
	// the SET NOT NULL that a validated check has made instant, and the drop of
	// that check afterwards.
	//
	// It shares a file with StageValidate and must come after everything in it.
	// Both of these take ACCESS EXCLUSIVE, and a lock is held until the
	// transaction commits rather than until the statement ends — so a
	// validation scheduled after one of them would do its scan underneath it,
	// which is exactly what the sequence exists to prevent. They are cheap, so
	// running last costs nothing.
	StageFinish

	// StageAdopt is the catalog write that takes over what a concurrent index
	// build produced: ADD CONSTRAINT ... USING INDEX. It has to follow the
	// build, and it needs a transaction, so it cannot be in the file that has
	// none. Like StageFinish it takes ACCESS EXCLUSIVE and so goes after every
	// scan sharing its transaction.
	StageAdopt

	// StageConcurrent cannot run inside a transaction at all: CREATE INDEX
	// CONCURRENTLY and DROP INDEX CONCURRENTLY. Building an index without
	// CONCURRENTLY takes a lock that blocks writes for the duration, so on a
	// live table this is not optional.
	StageConcurrent
)

// stages are the change groups in the order they are applied, and the file each
// lands in. Order and file are separate because they answer different
// questions: what must run before what, and what must not share a transaction.
// Two stages sharing a file share a transaction, and the order between them is
// what keeps that safe.
//
// The shape is: change the catalog, then do the expensive work under the
// weakest lock that will carry it, then take a short strong lock to adopt the
// results. A file takes the suffix of the first stage in it, so its name
// describes what it leads with.
var stages = []struct {
	stage  Stage
	file   int
	suffix string
}{
	{StageMain, 0, ""},
	{StageConcurrent, 1, "_indexes"},
	{StageValidate, 2, "_validate"},
	{StageFinish, 2, "_validate"},
	{StageAdopt, 2, "_constraints"},
}

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

	// DependsOn names what this change cannot run without, when that is another
	// change in the same migration which is itself commented out. It renders
	// commented out too, and live again as soon as the change it waits on does.
	//
	// A destructive change is emitted commented out so that applying it is a
	// deliberate act. Anything depending on it — a constraint or an index over
	// a column an ADD COLUMN introduces — has to travel with it, or the file is
	// not a reviewable no-op but a migration that fails partway through: the
	// constraint names a column the commented-out statement never added.
	//
	// It is separate from Destructive because it means something different.
	// This change loses nothing and is not dangerous; it is merely waiting on a
	// decision nobody has taken yet, and the note it renders says that rather
	// than calling it destructive. It is separate from Lock for a plainer
	// reason: that is about a statement being slow, this is about it failing.
	DependsOn string

	// Stage says which file the change belongs in, for the changes that
	// cannot share one with the changes around them.
	Stage Stage

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

	// needsColumns are the columns this change names, as quoted
	// table."column", and needsTable is set instead when the change's
	// definition is hand-written SQL whose column references cannot be known
	// without parsing it. Diff records them so it can recognise a change that
	// depends on a column another change in the same migration adds, and
	// nothing else reads them; see markDependents.
	//
	// They are unexported for the same reason unblocked is: only Diff can fill
	// them in, since the table and the columns do not survive into the SQL.
	needsColumns []string
	needsTable   string

	// unblocked is the sequence that reaches the same state without holding
	// the lock for the length of a scan, for the changes that have one. Unblock
	// substitutes it and nothing else reads it.
	//
	// It is unexported because only Diff can build one: it takes the table, the
	// constraint and the column that produced the change, none of which survive
	// into the SQL. A Change assembled by hand simply has no alternative form,
	// and Unblock leaves it alone.
	unblocked []Change
}

// Unblock replaces the changes that hold a long lock with the sequences that do
// not, where such a sequence exists. There are three:
//
//   - An ADD CONSTRAINT that would scan the table — a CHECK or a FOREIGN KEY —
//     becomes an ADD ... NOT VALID and a VALIDATE CONSTRAINT in a later
//     migration, moving the scan under a lock writers pass through.
//   - A SET NOT NULL becomes the same pair with the requirement set between
//     them, since Postgres accepts a validated check as proof and skips its own
//     scan.
//   - A UNIQUE or PRIMARY KEY, which has no NOT VALID form because there is no
//     way to build an index without reading every row, becomes a CREATE UNIQUE
//     INDEX CONCURRENTLY and an ADD CONSTRAINT ... USING INDEX that adopts it.
//
// A type change is left alone. Rewriting a table has no in-place form at all:
// the alternative is a second column, a batched backfill and a cutover, and
// only the person doing it knows what a batch costs or when the cutover can
// happen.
//
// It is a deliberate act rather than the default, for two reasons. The sequence
// is longer, splits the migration across files, and buys nothing on a table
// small enough that the scan is instant — which most tables are. And none of
// them is equivalent under failure. A plain statement that meets a bad row
// leaves nothing behind; these leave a constraint in place unvalidated and
// binding, or an invalid index that has to be dropped before the migration can
// be retried. That is usually the right trade on a large table and it is still
// a different outcome, so it is chosen rather than assumed.
//
// The end state on success is identical, which is what makes the substitution
// safe: the temporary check a SET NOT NULL needs is dropped by the same
// sequence that created it, and the index a unique constraint adopts is built
// under the name the constraint will take.
//
// The usual shape is to look before deciding:
//
//	changes, err := migrate.Diff(current, target)
//	if len(migrate.Migration{Changes: changes}.Blocking()) > 0 {
//		changes = migrate.Unblock(changes)
//	}
//
// Changes with no alternative are passed through untouched and still report
// themselves through Blocking.
func Unblock(changes []Change) []Change {
	out := make([]Change, 0, len(changes))
	for _, c := range changes {
		if len(c.unblocked) == 0 {
			out = append(out, c)
			continue
		}
		out = append(out, c.unblocked...)
	}
	return out
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
// statement, so a change needing a transaction of its own needs a file of its
// own. A migration containing CREATE INDEX CONCURRENTLY must disable
// transactions for everything in it — which would silently remove the rollback
// guarantee from every other change generated at the same time — and a
// VALIDATE CONSTRAINT must land after the transaction holding the ADD
// CONSTRAINT it validates has committed. Splitting keeps the ordinary changes
// transactional and gives each of the others what it needs.
//
// Files come out in stage order, so the tables, columns and indexes each one
// depends on exist by the time it runs.
func Split(m Migration) []Migration {
	byStage := make(map[Stage][]Change, len(stages))
	for _, c := range m.Changes {
		byStage[c.Stage] = append(byStage[c.Stage], c)
	}

	var out []Migration
	version := m.Version
	for _, s := range stages {
		changes := byStage[s.stage]
		if len(changes) == 0 {
			continue
		}
		// A stage sharing the previous one's file appends to it, in stage
		// order, which is the ordering that makes sharing safe.
		if n := len(out); n > 0 && s.file == fileOf(out[n-1].Changes[0].Stage) {
			out[n-1].Changes = append(out[n-1].Changes, changes...)
			continue
		}
		// Each file's version must sort after the last, and all of them must
		// be stable across runs, so they are derived rather than taken from a
		// clock.
		if len(out) > 0 {
			version = bumpVersion(version)
		}
		out = append(out, Migration{Version: version, Name: m.Name + s.suffix, Changes: changes})
	}
	if len(out) == 1 {
		// One file: keep the migration exactly as it was given, rather than
		// renaming it after whichever stage happened to fill it.
		return []Migration{m}
	}
	return out
}

func fileOf(s Stage) int {
	for _, e := range stages {
		if e.stage == s {
			return e.file
		}
	}
	return 0
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
//
// A change with a DependsOn is commented out by the same flag that commented
// out the change it waits on, so the two are uncommented together or not at
// all. Half an applied dependency is the failure this exists to prevent.
func statement(sql string, c Change, opts Options) string {
	sql = strings.TrimSpace(sql)
	if sql == "" {
		return ""
	}
	var note string
	switch {
	case opts.AllowDestructive:
		return sql
	case c.Destructive:
		note = "DESTRUCTIVE: " + c.Reason
	case c.DependsOn != "":
		note = "DEPENDS ON: " + c.DependsOn
	default:
		return sql
	}
	var b strings.Builder
	// Wrapped to the same column as a lock hazard: these two are the notes a
	// reviewer has to actually read, and one of them running off the screen
	// while the other does not is a good way to have it skipped.
	for _, line := range wrap(note, 74, "  ") {
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
