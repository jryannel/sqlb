package main

// `sqlb survey` — the one verb that reads a database instead of a declaration.
//
// It answers, for a whole existing database, the question the sqlb port keeps
// discovering ten tables at a time: which tables can sqlb's schema DSL
// describe, which cannot, and why.
//
// It is the repeatable form of a one-off adoption probe, with the one addition
// that makes the output triageable: every table is introspected ALONE as well
// as together, because introspect's report is per-construct but the drift gate
// is per-registry — one unmodelable table takes its whole module out of the
// gate (#109). Per-table isolation says which tables are adoptable today and
// which are blocked, instead of one flat list of skips.
//
// # Why this one compiles nothing
//
// Every other verb reads a registry, and a registry exists only once the schema
// package is linked in (ADR-0004), which is what the driver compile in
// driver.go is for. This verb reads a registry too — but it builds it by
// introspecting a live database, so there is no declaration to import and
// nothing to compile. That is the whole difference, and it is why `survey`
// takes two DSNs where the others take a package.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jryannel/sqlb/introspect"
	"github.com/jryannel/sqlb/migrate"
	"github.com/jryannel/sqlb/schema"
)

// defaultExcluded names the bookkeeping tables of the migration runners a Go
// Postgres project is most likely to be on. They are excluded rather than
// reported because no declaration will ever describe them, and a survey that
// counts them as blockers overstates the work.
//
// The list is a default, not a policy: -exclude replaces it, because a runner
// this does not know about would otherwise show up as an unmodelable table and
// read as a real result. Entries absent from the database are dropped before
// the list reaches introspect — see the narrowing in survey.
var defaultExcluded = []string{
	"goose_db_version",       // goose
	"schema_migrations",      // golang-migrate, dbmate, ActiveRecord-style
	"atlas_schema_revisions", // atlas
	"_sqlx_migrations",       // sqlx
	"flyway_schema_history",  // flyway
}

// tableResult is one table's verdict from the per-table isolation phase.
type tableResult struct {
	Name    string
	Skips   []introspect.Skip
	Err     string
	Columns int
}

// report is where the survey writes, and the discarded write error is the same
// deliberate form as everywhere else in this command: a program whose stdout
// has gone away has no remaining channel on which to report that.
//
// The whole output is one markdown document meant to be redirected to a file,
// so a construct that failed to introspect is a *line in the report* rather
// than an error — it goes to stdout with everything else. Only a failure that
// stops the survey from running at all comes back as an error.
type report struct{ w io.Writer }

func (r report) printf(format string, a ...any) { _, _ = fmt.Fprintf(r.w, format, a...) }

// surveyUsage is separate from the top-level usage because this is the only
// verb whose arguments are not a package, and folding two argument shapes into
// one block made both harder to read.
const surveyUsage = `sqlb survey reports which of a database's tables sqlb can describe, and why not.

Usage:

    sqlb survey [flags] <src-migrated-dsn> <dst-empty-dsn>

Flags:

    -modules a,b,c    table-name prefixes to group the per-table verdict by, for
                      a modular monolith whose tables are named <module>_<table>
    -exclude t1,t2    tables to leave out entirely, replacing the built-in
                      migration-runner list, which is:
                          %s

<src-migrated-dsn> is the database to survey; it is only read from.
<dst-empty-dsn> is a scratch database the round-trip phase writes into, and it
must already carry the extensions the source uses — Diff renders no CREATE
EXTENSION, so a bootstrap into a bare database fails once per table with
"function uuid_generate_v4() does not exist" rather than once with the missing
extension named.
`

// survey runs the whole report, and is what `sqlb survey` dispatches to.
//
// Unlike the driving verbs it needs no module, no package and no compile, so it
// is reached before run resolves a package pattern.
func survey(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("sqlb survey", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, surveyUsage, strings.Join(defaultExcluded, "\n                          "))
	}
	modules := fs.String("modules", "", "comma-separated table-name prefixes to group the per-table verdict by")
	exclude := fs.String("exclude", "", "comma-separated tables to leave out entirely, replacing the built-in migration-runner list")
	if err := fs.Parse(args); err != nil {
		// flag has already printed what was wrong and the usage above it.
		return exitCode(2)
	}

	rest := fs.Args()
	for _, a := range rest {
		if strings.HasPrefix(a, "-") {
			return fmt.Errorf(
				"%q is a flag, and survey's flags go before the two DSNs: "+
					"sqlb survey -modules billing,catalog $SRC $SCRATCH", a)
		}
	}
	if len(rest) != 2 {
		fs.Usage()
		return exitCode(2)
	}

	ctx := context.Background()
	src, err := open(ctx, rest[0])
	if err != nil {
		return fmt.Errorf("the database to survey: %w", err)
	}
	defer src.Close()
	dst, err := open(ctx, rest[1])
	if err != nil {
		return fmt.Errorf("the scratch database: %w", err)
	}
	defer dst.Close()

	return runSurvey(ctx, report{stdout}, src, dst, *modules, *exclude)
}

// runSurvey is survey with the connections already open, which is what makes
// the report itself reachable from a test that has a database.
func runSurvey(ctx context.Context, out report, src, dst *pgxpool.Pool, modules, exclude string) error {
	wanted := defaultExcluded
	if strings.TrimSpace(exclude) != "" {
		wanted = nil
		for _, t := range strings.Split(exclude, ",") {
			if t = strings.TrimSpace(t); t != "" {
				wanted = append(wanted, t)
			}
		}
	}

	// Narrow the exclusion list to what the database actually holds.
	//
	// introspect reports a name in Exclude that it does not find, deliberately
	// — a typo would otherwise silently shrink what a gate checks. That is the
	// right behaviour for a gate and the wrong one for a default list covering
	// five migration runners, four of which are absent from any given project:
	// they would arrive as four skipped constructs, which is a finding about
	// this command's defaults rather than about the schema.
	present, err := listTables(ctx, src, nil)
	if err != nil {
		return err
	}
	excluded := intersect(wanted, present)

	all, err := listTables(ctx, src, excluded)
	if err != nil {
		return err
	}
	label := "nothing"
	if len(excluded) > 0 {
		label = strings.Join(excluded, ", ")
	}
	out.printf("# sqlb schema survey\n\n%d base tables (excluding %s)\n\n", len(all), label)

	// ---------------------------------------------------------------- phase A
	// Whole-schema introspect. This is what a drift gate over the entire
	// database would see.
	out.printf("## Phase A — whole-schema introspect\n\n")
	regAll, repAll, errAll := introspect.Registry(ctx, src, introspect.Options{Exclude: excluded})
	if errAll != nil {
		out.printf("REGISTRY ERROR: %s\n\n", oneline(errAll.Error()))
	} else {
		out.printf("registry built: %d tables modelled\n\n", len(regAll.Tables()))
	}
	if repAll != nil {
		out.printf("skipped constructs: %d\n", len(repAll.Skipped))
		out.printf("notes: %d\n\n", len(repAll.Notes))
		printByReason(out, repAll.Skipped)
		for _, n := range repAll.Notes {
			out.printf("  NOTE %s\n", oneline(n))
		}
		out.printf("\n")
	}

	// ---------------------------------------------------------------- phase B
	// Per-table isolation. A table that imports clean on its own is adoptable
	// now; one that does not is a blocker with a name attached.
	out.printf("## Phase B — per-table isolation\n\n")
	results := make([]tableResult, 0, len(all))
	for _, t := range all {
		r := tableResult{Name: t}
		reg, rep, err := introspect.Registry(ctx, src, introspect.Options{Only: []string{t}})
		if err != nil {
			r.Err = oneline(err.Error())
		} else if tbl := findTable(reg, t); tbl != nil {
			r.Columns = len(tbl.StoredFields())
		}
		if rep != nil {
			r.Skips = rep.Skipped
		}
		results = append(results, r)
	}

	var clean, skipped, errored []tableResult
	for _, r := range results {
		switch {
		case r.Err != "":
			errored = append(errored, r)
		case len(r.Skips) > 0:
			skipped = append(skipped, r)
		default:
			clean = append(clean, r)
		}
	}
	out.printf("| verdict | tables |\n|---|---:|\n")
	out.printf("| clean — imports with nothing dropped | %d |\n", len(clean))
	out.printf("| partial — imports, constructs dropped | %d |\n", len(skipped))
	out.printf("| refused — registry error | %d |\n\n", len(errored))

	if len(errored) > 0 {
		out.printf("### Refused\n\n")
		for _, r := range errored {
			out.printf("- **%s** — %s\n", r.Name, r.Err)
		}
		out.printf("\n")
	}
	if len(skipped) > 0 {
		out.printf("### Partial\n\n")
		for _, r := range skipped {
			out.printf("- **%s** (%d cols)\n", r.Name, r.Columns)
			for _, s := range r.Skips {
				obj := s.Object
				if obj == "" {
					obj = "-"
				}
				out.printf("    - `%s`: %s\n", obj, oneline(s.Reason))
				if s.Def != "" {
					out.printf("      `%s`\n", oneline(s.Def))
				}
			}
		}
		out.printf("\n")
	}
	out.printf("### Clean (%d)\n\n", len(clean))
	names := make([]string, 0, len(clean))
	for _, r := range clean {
		names = append(names, r.Name)
	}
	out.printf("%s\n\n", strings.Join(names, ", "))

	printByModule(out, modules, results)

	// ---------------------------------------------------------------- phase C
	out.printf("## Phase C — round-trip fixpoint\n\n")
	if errAll != nil {
		out.printf("skipped: whole-schema registry did not build\n\n")
		// A whole-schema registry that will not build is the survey's loudest
		// finding, not its failure — it is the answer to "can sqlb describe
		// this database", printed under Phase A and again here. Returning
		// errAll would replace two phases of report with one line on stderr.
		return nil //nolint:nilerr // errAll is reported in the document, which is the output
	}
	applyFails, residual, complete := fixpoint(ctx, out, dst, regAll, excluded)
	if !complete {
		return nil
	}

	out.printf("## Verdict\n\n")
	out.printf("- tables: %d — %d clean, %d partial, %d refused\n", len(all), len(clean), len(skipped), len(errored))
	out.printf("- skipped constructs (whole schema): %d\n", lenSkips(repAll))
	out.printf("- DDL apply failures: %d\n", len(applyFails))
	out.printf("- fixpoint residual: %d\n", len(residual))
	return nil
}

// fixpoint renders the modelled registry's DDL into the empty database and
// re-introspects it: anything the round trip does not preserve is a construct
// the gate would report as drift forever.
//
// It returns no error, and that is the design rather than an omission. Every
// failure in here is one of the survey's *answers* — a diff that will not
// compute against this database is exactly the kind of thing the report exists
// to name, and turning it into a non-zero exit would throw away the two phases
// already written. So a failed step is printed where it happened and the phase
// stops; the bool says whether it got far enough for the verdict to have counts
// worth quoting.
func fixpoint(
	ctx context.Context, out report, dst *pgxpool.Pool,
	regAll *schema.Registry, excluded []string,
) (applyFails []string, residual []migrate.Change, complete bool) {
	regEmpty, _, err := introspect.Registry(ctx, dst, introspect.Options{Exclude: excluded})
	if err != nil {
		out.printf("introspect dst: %s\n\n", oneline(err.Error()))
		return nil, nil, false
	}
	create, err := migrate.Diff(regEmpty, regAll)
	if err != nil {
		out.printf("diff empty->all: %s\n\n", oneline(err.Error()))
		return nil, nil, false
	}
	for _, c := range create {
		if _, err := dst.Exec(ctx, c.Up); err != nil {
			applyFails = append(applyFails, fmt.Sprintf("%s — %s", c.Comment, oneline(err.Error())))
		}
	}
	out.printf("DDL statements: %d, apply failures: %d\n\n", len(create), len(applyFails))
	for i, f := range applyFails {
		if i >= 20 {
			out.printf("  … and %d more\n", len(applyFails)-20)
			break
		}
		out.printf("  FAIL %s\n", f)
	}
	if len(applyFails) > 0 {
		out.printf("\n")
	}

	regBack, _, err := introspect.Registry(ctx, dst, introspect.Options{Exclude: excluded})
	if err != nil {
		out.printf("re-introspect dst: %s\n\n", oneline(err.Error()))
		return nil, nil, false
	}
	residual, err = migrate.Diff(regAll, regBack)
	if err != nil {
		out.printf("diff all->back: %s\n\n", oneline(err.Error()))
		return nil, nil, false
	}
	out.printf("residual changes after round trip: **%d** (0 == fixpoint)\n\n", len(residual))
	byComment := map[string]int{}
	for _, c := range residual {
		byComment[kindOf(c.Comment)]++
	}
	if len(residual) > 0 {
		kinds := keys(byComment)
		sort.Strings(kinds)
		out.printf("| residual kind | count |\n|---|---:|\n")
		for _, k := range kinds {
			out.printf("| %s | %d |\n", k, byComment[k])
		}
		out.printf("\n")
		for i, c := range residual {
			if i >= 30 {
				out.printf("  … and %d more\n", len(residual)-30)
				break
			}
			out.printf("  - %-45s %s\n", c.Comment, oneline(c.Up))
		}
		out.printf("\n")
	}
	return applyFails, residual, true
}

// printByModule regroups the per-table verdict by table-name prefix.
//
// A flat verdict list under-reports a modular monolith. The gate is per
// registry, and a modular monolith has one registry per module (ADR-0015), so
// a table that cannot be modelled does not take out "the schema" — it takes
// out its module, and the count of affected modules is what decides how much
// of the port is blocked. Whether one blocked table means one app or six is
// not visible in a list sorted by table name.
//
// Prefixes are supplied rather than guessed: inferring them from table names
// would split hotel_rooms and hotels into two modules, and a wrong split reads
// as a real result.
func printByModule(out report, spec string, results []tableResult) {
	if strings.TrimSpace(spec) == "" {
		return
	}
	prefixes := make([]string, 0)
	for _, p := range strings.Split(spec, ",") {
		if p = strings.TrimSpace(p); p != "" {
			prefixes = append(prefixes, p)
		}
	}
	if len(prefixes) == 0 {
		return
	}
	// Longest prefix wins, so a module named "user" does not claim the tables
	// of one named "user_billing".
	sort.Slice(prefixes, func(i, j int) bool { return len(prefixes[i]) > len(prefixes[j]) })

	type mod struct{ clean, partial, refused int }
	mods := map[string]*mod{}
	for _, p := range prefixes {
		mods[p] = &mod{}
	}
	unclaimed := &mod{}
	var unclaimedNames []string

	for _, r := range results {
		m, name := unclaimed, ""
		for _, p := range prefixes {
			if strings.HasPrefix(r.Name, p+"_") {
				m, name = mods[p], p
				break
			}
		}
		switch {
		case r.Err != "":
			m.refused++
		case len(r.Skips) > 0:
			m.partial++
		default:
			m.clean++
		}
		if name == "" {
			unclaimedNames = append(unclaimedNames, r.Name)
		}
	}

	out.printf("### By module\n\n")
	out.printf("| module | tables | clean | partial | refused | gate |\n")
	out.printf("|---|---:|---:|---:|---:|---|\n")
	sort.Strings(prefixes)
	// Groups, not prefixes: the no-prefix row is a group that can be blocked,
	// so counting it in the numerator and not the denominator produced "3 of 3
	// modules blocked" over a table with a green row in it. Shared tables
	// blocking everything is the finding most worth keeping visible, so the
	// row stays in both halves of the fraction rather than being dropped from
	// the numerator.
	groups, blocked := len(prefixes), 0
	for _, p := range prefixes {
		m := mods[p]
		total := m.clean + m.partial + m.refused
		gate := "green"
		if m.partial+m.refused > 0 {
			gate = "**blocked**"
			blocked++
		}
		out.printf("| %s | %d | %d | %d | %d | %s |\n", p, total, m.clean, m.partial, m.refused, gate)
	}
	if n := unclaimed.clean + unclaimed.partial + unclaimed.refused; n > 0 {
		groups++
		gate := "green"
		if unclaimed.partial+unclaimed.refused > 0 {
			gate = "**blocked**"
			blocked++
		}
		out.printf("| _(no prefix)_ | %d | %d | %d | %d | %s |\n",
			n, unclaimed.clean, unclaimed.partial, unclaimed.refused, gate)
	}
	out.printf("\n**%d of %d modules blocked.**\n\n", blocked, groups)
	if len(unclaimedNames) > 0 {
		sort.Strings(unclaimedNames)
		out.printf("Tables matching no prefix (%d) — shared, or a prefix is missing from -modules:\n\n%s\n\n",
			len(unclaimedNames), strings.Join(unclaimedNames, ", "))
	}
}

func lenSkips(r *introspect.Report) int {
	if r == nil {
		return 0
	}
	return len(r.Skipped)
}

func findTable(reg *schema.Registry, name string) *schema.TableDef {
	for _, t := range reg.Tables() {
		if t.Name() == name || t.LocalName() == name {
			return t
		}
	}
	return nil
}

func printByReason(out report, skips []introspect.Skip) {
	if len(skips) == 0 {
		return
	}
	by := map[string][]introspect.Skip{}
	for _, s := range skips {
		by[s.Reason] = append(by[s.Reason], s)
	}
	reasons := keys(by)
	sort.Slice(reasons, func(i, j int) bool { return len(by[reasons[i]]) > len(by[reasons[j]]) })
	for _, r := range reasons {
		out.printf("### [%d] %s\n\n", len(by[r]), oneline(r))
		for _, s := range by[r] {
			where := s.Table
			if s.Object != "" {
				where += "." + s.Object
			}
			out.printf("  - %-50s %s\n", where, oneline(s.Def))
		}
		out.printf("\n")
	}
}

// kindOf reduces a change comment to its verb so residuals can be counted by
// shape rather than listed one by one.
func kindOf(comment string) string {
	f := strings.Fields(comment)
	if len(f) >= 2 {
		return f[0] + " " + f[1]
	}
	if len(f) == 1 {
		return f[0]
	}
	return "(unlabelled)"
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func listTables(ctx context.Context, db *pgxpool.Pool, skip []string) ([]string, error) {
	// A nil slice binds as SQL NULL, and `tablename <> ALL(NULL)` is NULL
	// rather than true — so a nil skip list would match no rows at all.
	if skip == nil {
		skip = []string{}
	}
	rows, err := db.Query(ctx, `
		SELECT tablename FROM pg_tables
		WHERE schemaname = 'public' AND tablename <> ALL($1)
		ORDER BY tablename`, skip)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, fmt.Errorf("scan table: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// intersect returns the members of want that appear in have, order preserved.
func intersect(want, have []string) []string {
	set := make(map[string]bool, len(have))
	for _, h := range have {
		set[h] = true
	}
	var out []string
	for _, w := range want {
		if set[w] {
			out = append(out, w)
		}
	}
	return out
}

func open(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return pool, nil
}

func oneline(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}
