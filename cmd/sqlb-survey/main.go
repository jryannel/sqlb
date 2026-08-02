// Command survey answers, for a whole existing database, the question the
// sqlb port keeps discovering ten tables at a time: which tables can sqlb's
// schema DSL describe, which cannot, and why.
//
// It is the repeatable form of a one-off adoption probe, with the one
// addition that makes the output triageable: every table is introspected
// ALONE as well as together, because introspect's report is per-construct but
// the drift gate is per-registry — one unmodelable table takes its whole
// module out of the gate (sqlb#109). Per-table isolation says which tables are
// adoptable today and which are blocked, instead of one flat list of skips.
//
// The scratch database must already carry the extensions the source uses:
// Diff renders no CREATE EXTENSION, so a bootstrap into a bare database fails
// once per table with "function uuid_generate_v4() does not exist" rather than
// once with the missing extension named.
//
// Usage: sqlb-survey <src-migrated-dsn> <dst-empty-dsn>
package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jryannel/sqlb/introspect"
	"github.com/jryannel/sqlb/migrate"
	"github.com/jryannel/sqlb/schema"
)

// excluded names the tables no declaration will ever describe: the migration
// runner's bookkeeping. River's tables are created at runtime and are not in
// this database, so they need no entry.
var excluded = []string{"goose_db_version"}

type tableResult struct {
	Name     string
	Skips    []introspect.Skip
	Err      string
	Columns  int
	Fixpoint string // "", "clean", or the residual description
}

func main() {
	if len(os.Args) != 3 {
		fatal("usage: sqlb-survey <src-migrated-dsn> <dst-empty-dsn>")
	}
	ctx := context.Background()
	src := open(ctx, os.Args[1])
	defer src.Close()
	dst := open(ctx, os.Args[2])
	defer dst.Close()

	all := listTables(ctx, src)
	fmt.Printf("# sqlb schema survey\n\n%d base tables (excluding %s)\n\n",
		len(all), strings.Join(excluded, ", "))

	// ---------------------------------------------------------------- phase A
	// Whole-schema introspect. This is what a drift gate over the entire
	// database would see.
	fmt.Printf("## Phase A — whole-schema introspect\n\n")
	regAll, repAll, errAll := introspect.Registry(ctx, src, introspect.Options{Exclude: excluded})
	if errAll != nil {
		fmt.Printf("REGISTRY ERROR: %s\n\n", oneline(errAll.Error()))
	} else {
		fmt.Printf("registry built: %d tables modelled\n\n", len(regAll.Tables()))
	}
	if repAll != nil {
		fmt.Printf("skipped constructs: %d\n", len(repAll.Skipped))
		fmt.Printf("notes: %d\n\n", len(repAll.Notes))
		printByReason(repAll.Skipped)
		for _, n := range repAll.Notes {
			fmt.Printf("  NOTE %s\n", oneline(n))
		}
		fmt.Println()
	}

	// ---------------------------------------------------------------- phase B
	// Per-table isolation. A table that imports clean on its own is adoptable
	// now; one that does not is a blocker with a name attached.
	fmt.Printf("## Phase B — per-table isolation\n\n")
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
	fmt.Printf("| verdict | tables |\n|---|---:|\n")
	fmt.Printf("| clean — imports with nothing dropped | %d |\n", len(clean))
	fmt.Printf("| partial — imports, constructs dropped | %d |\n", len(skipped))
	fmt.Printf("| refused — registry error | %d |\n\n", len(errored))

	if len(errored) > 0 {
		fmt.Printf("### Refused\n\n")
		for _, r := range errored {
			fmt.Printf("- **%s** — %s\n", r.Name, r.Err)
		}
		fmt.Println()
	}
	if len(skipped) > 0 {
		fmt.Printf("### Partial\n\n")
		for _, r := range skipped {
			fmt.Printf("- **%s** (%d cols)\n", r.Name, r.Columns)
			for _, s := range r.Skips {
				obj := s.Object
				if obj == "" {
					obj = "-"
				}
				fmt.Printf("    - `%s`: %s\n", obj, oneline(s.Reason))
				if s.Def != "" {
					fmt.Printf("      `%s`\n", oneline(s.Def))
				}
			}
		}
		fmt.Println()
	}
	fmt.Printf("### Clean (%d)\n\n", len(clean))
	names := make([]string, 0, len(clean))
	for _, r := range clean {
		names = append(names, r.Name)
	}
	fmt.Printf("%s\n\n", strings.Join(names, ", "))

	// ---------------------------------------------------------------- phase C
	// Fixpoint. Render the modelled registry's DDL into an empty database and
	// re-introspect: anything the round trip does not preserve is a construct
	// the gate would report as drift forever.
	fmt.Printf("## Phase C — round-trip fixpoint\n\n")
	if errAll != nil {
		fmt.Printf("skipped: whole-schema registry did not build\n\n")
		return
	}
	regEmpty, _, err := introspect.Registry(ctx, dst, introspect.Options{Exclude: excluded})
	if err != nil {
		fmt.Printf("introspect dst: %s\n\n", oneline(err.Error()))
		return
	}
	create, err := migrate.Diff(regEmpty, regAll)
	if err != nil {
		fmt.Printf("diff empty->all: %s\n\n", oneline(err.Error()))
		return
	}
	var applyFails []string
	for _, c := range create {
		if _, err := dst.Exec(ctx, c.Up); err != nil {
			applyFails = append(applyFails, fmt.Sprintf("%s — %s", c.Comment, oneline(err.Error())))
		}
	}
	fmt.Printf("DDL statements: %d, apply failures: %d\n\n", len(create), len(applyFails))
	for i, f := range applyFails {
		if i >= 20 {
			fmt.Printf("  … and %d more\n", len(applyFails)-20)
			break
		}
		fmt.Printf("  FAIL %s\n", f)
	}
	if len(applyFails) > 0 {
		fmt.Println()
	}

	regBack, _, err := introspect.Registry(ctx, dst, introspect.Options{Exclude: excluded})
	if err != nil {
		fmt.Printf("re-introspect dst: %s\n\n", oneline(err.Error()))
		return
	}
	residual, err := migrate.Diff(regAll, regBack)
	if err != nil {
		fmt.Printf("diff all->back: %s\n\n", oneline(err.Error()))
		return
	}
	fmt.Printf("residual changes after round trip: **%d** (0 == fixpoint)\n\n", len(residual))
	byComment := map[string]int{}
	for _, c := range residual {
		byComment[kindOf(c.Comment)]++
	}
	if len(residual) > 0 {
		kinds := keys(byComment)
		sort.Strings(kinds)
		fmt.Printf("| residual kind | count |\n|---|---:|\n")
		for _, k := range kinds {
			fmt.Printf("| %s | %d |\n", k, byComment[k])
		}
		fmt.Println()
		for i, c := range residual {
			if i >= 30 {
				fmt.Printf("  … and %d more\n", len(residual)-30)
				break
			}
			fmt.Printf("  - %-45s %s\n", c.Comment, oneline(c.Up))
		}
		fmt.Println()
	}

	fmt.Printf("## Verdict\n\n")
	fmt.Printf("- tables: %d — %d clean, %d partial, %d refused\n", len(all), len(clean), len(skipped), len(errored))
	fmt.Printf("- skipped constructs (whole schema): %d\n", lenSkips(repAll))
	fmt.Printf("- DDL apply failures: %d\n", len(applyFails))
	fmt.Printf("- fixpoint residual: %d\n", len(residual))
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

func printByReason(skips []introspect.Skip) {
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
		fmt.Printf("### [%d] %s\n\n", len(by[r]), oneline(r))
		for _, s := range by[r] {
			where := s.Table
			if s.Object != "" {
				where += "." + s.Object
			}
			fmt.Printf("  - %-50s %s\n", where, oneline(s.Def))
		}
		fmt.Println()
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

func listTables(ctx context.Context, db *pgxpool.Pool) []string {
	rows, err := db.Query(ctx, `
		SELECT tablename FROM pg_tables
		WHERE schemaname = 'public' AND tablename <> ALL($1)
		ORDER BY tablename`, excluded)
	if err != nil {
		fatal("list tables: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			fatal("scan table: %v", err)
		}
		out = append(out, t)
	}
	return out
}

func open(ctx context.Context, dsn string) *pgxpool.Pool {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		fatal("open: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		fatal("ping: %v", err)
	}
	return pool
}

func oneline(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}

func fatal(f string, a ...any) {
	fmt.Fprintf(os.Stderr, f+"\n", a...)
	os.Exit(1)
}
