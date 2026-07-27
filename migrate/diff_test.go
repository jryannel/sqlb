package migrate_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/jryannel/sqlb/migrate"
	"github.com/jryannel/sqlb/schema"
)

// build makes a registry from a declaration function, so a test can state a
// before and an after state side by side.
func build(decl func(r *schema.Registry)) *schema.Registry {
	r := schema.NewRegistry()
	decl(r)
	return r
}

func diff(t *testing.T, current, target *schema.Registry) []migrate.Change {
	t.Helper()
	changes, err := migrate.Diff(current, target)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	return changes
}

// only returns the single change a test expects, failing with the whole set if
// there is not exactly one — which is more useful than an index out of range.
func only(t *testing.T, changes []migrate.Change) migrate.Change {
	t.Helper()
	if len(changes) != 1 {
		t.Fatalf("want 1 change, got %d:\n%s", len(changes), render(changes))
	}
	return changes[0]
}

// find returns the one change whose Up contains substr.
func find(t *testing.T, changes []migrate.Change, substr string) migrate.Change {
	t.Helper()
	var hits []migrate.Change
	for _, c := range changes {
		if strings.Contains(c.Up, substr) {
			hits = append(hits, c)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("want exactly 1 change containing %q, got %d:\n%s", substr, len(hits), render(changes))
	}
	return hits[0]
}

func render(changes []migrate.Change) string {
	var b strings.Builder
	for i, c := range changes {
		b.WriteString(strings.Repeat(" ", 2))
		b.WriteString(strings.TrimSpace(c.Up))
		if c.Destructive {
			b.WriteString("   [destructive: " + c.Reason + "]")
		}
		switch c.Stage {
		case migrate.StageValidate:
			b.WriteString("   [validate]")
		case migrate.StageConcurrent:
			b.WriteString("   [concurrent]")
		}
		if i < len(changes)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// renderFiles renders changes the way a project would, so that a test can ask
// what the generated files actually execute rather than what the changes say.
func renderFiles(t *testing.T, changes []migrate.Change) map[string]string {
	t.Helper()
	files, err := migrate.Render(migrate.Migration{
		Version: "20260727120000",
		Name:    "generated",
		Changes: changes,
	}, migrate.Options{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	return files
}

// liveSQL returns the lines of a rendered file that a runner would execute:
// everything neither blank nor commented out, which includes goose's own
// annotations.
func liveSQL(body string) []string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		out = append(out, line)
	}
	return out
}

func ups(changes []migrate.Change) []string {
	out := make([]string, len(changes))
	for i, c := range changes {
		out[i] = c.Up
	}
	return out
}

// Common fixtures.

func orgsAndUsers(r *schema.Registry) {
	orgs := r.Table("orgs",
		schema.UUIDv7("id").PrimaryKey(),
		schema.Text("name"),
	)
	r.Table("users",
		schema.UUIDv7("id").PrimaryKey(),
		schema.Text("email").Unique(),
		schema.Ref("org", orgs).OnDelete(schema.Cascade),
	)
}

func TestDiffEmpty(t *testing.T) {
	if changes := diff(t, nil, nil); len(changes) != 0 {
		t.Fatalf("empty to empty should produce nothing, got:\n%s", render(changes))
	}
}

func TestDiffUnchanged(t *testing.T) {
	// Two registries built from the same declaration are structurally equal,
	// so a diff between them must be empty. This is the property that makes a
	// generator safe to run repeatedly: regenerating an unchanged schema
	// produces no migration at all.
	changes := diff(t, build(orgsAndUsers), build(orgsAndUsers))
	if len(changes) != 0 {
		t.Fatalf("identical schemas should produce nothing, got:\n%s", render(changes))
	}
}

func TestDiffCreateTable(t *testing.T) {
	target := build(func(r *schema.Registry) {
		r.Table("posts",
			schema.UUIDv7("id").PrimaryKey(),
			schema.Text("slug").Unique(),
			schema.Varchar("title", 200),
			schema.Int("views").Default(schema.Value(0)),
			schema.Timestamp("published_at").Nullable(),
			schema.Enum("status", "draft", "live"),
		).Check("views_non_negative", `"views" >= 0`)
	})

	c := only(t, diff(t, nil, target))

	want := `CREATE TABLE "posts" (
    "id" uuid NOT NULL DEFAULT uuid_generate_v7(),
    "slug" text NOT NULL,
    "title" varchar(200) NOT NULL,
    "views" integer NOT NULL DEFAULT 0,
    "published_at" timestamptz,
    "status" text NOT NULL,
    CONSTRAINT "posts_pkey" PRIMARY KEY ("id"),
    CONSTRAINT "posts_slug_key" UNIQUE ("slug"),
    CONSTRAINT "posts_status_check" CHECK ("status" IN ('draft', 'live')),
    CONSTRAINT "views_non_negative" CHECK ("views" >= 0)
);`
	if c.Up != want {
		t.Fatalf("CREATE TABLE mismatch\n got:\n%s\nwant:\n%s", c.Up, want)
	}
	if c.Down != `DROP TABLE "posts";` {
		t.Fatalf("Down = %q", c.Down)
	}
	if c.Destructive {
		t.Fatal("creating a table is not destructive")
	}
}

func TestDiffCreateTableComments(t *testing.T) {
	target := build(func(r *schema.Registry) {
		r.Table("posts",
			schema.UUIDv7("id").PrimaryKey(),
			schema.Text("body").Comment("markdown source"),
		).Describe("published articles")
	})

	c := only(t, diff(t, nil, target))
	for _, want := range []string{
		`COMMENT ON TABLE "posts" IS 'published articles';`,
		`COMMENT ON COLUMN "posts"."body" IS 'markdown source';`,
	} {
		if !strings.Contains(c.Up, want) {
			t.Errorf("missing %s in:\n%s", want, c.Up)
		}
	}
}

func TestDiffCreateTableModulePrefix(t *testing.T) {
	// The storage name is what reaches SQL, so the diff must compare and
	// render prefixed names rather than the local ones.
	target := schema.NewModule("billing")
	target.Table("invoices", schema.UUIDv7("id").PrimaryKey())

	c := only(t, diff(t, nil, target))
	if !strings.Contains(c.Up, `CREATE TABLE "billing_invoices"`) {
		t.Fatalf("want prefixed table name, got:\n%s", c.Up)
	}
	if !strings.Contains(c.Up, `CONSTRAINT "billing_invoices_pkey"`) {
		t.Fatalf("want prefixed constraint name, got:\n%s", c.Up)
	}
}

func TestDiffCreateTableForeignKeyIsSeparate(t *testing.T) {
	// A foreign key is never inlined into CREATE TABLE. That is what lets
	// tables be created in any order without a dependency sort, and it means
	// one code path adds a reference whether the table is new or not.
	changes := diff(t, nil, build(orgsAndUsers))

	fk := find(t, changes, "FOREIGN KEY")
	want := `ALTER TABLE "users" ADD CONSTRAINT "users_org_id_fkey" ` +
		`FOREIGN KEY ("org_id") REFERENCES "orgs" ("id") ON DELETE CASCADE;`
	if fk.Up != want {
		t.Fatalf("foreign key SQL\n got: %s\nwant: %s", fk.Up, want)
	}

	// It must land after both tables exist.
	order := ups(changes)
	fkAt, usersAt := indexOf(order, "FOREIGN KEY"), indexOf(order, `CREATE TABLE "users"`)
	orgsAt := indexOf(order, `CREATE TABLE "orgs"`)
	if fkAt < usersAt || fkAt < orgsAt {
		t.Fatalf("foreign key added before its tables exist:\n%s", render(changes))
	}
}

func TestDiffForeignKeyDefaultActionsOmitted(t *testing.T) {
	// NO ACTION is the Postgres default. Emitting it would make an imported
	// schema differ from the database it was imported from.
	target := build(func(r *schema.Registry) {
		orgs := r.Table("orgs", schema.UUIDv7("id").PrimaryKey())
		r.Table("users", schema.UUIDv7("id").PrimaryKey(), schema.Ref("org", orgs))
	})
	fk := find(t, diff(t, nil, target), "FOREIGN KEY")
	if strings.Contains(fk.Up, "NO ACTION") {
		t.Fatalf("NO ACTION should not be rendered: %s", fk.Up)
	}
}

func TestDiffExternalRefHasNoForeignKey(t *testing.T) {
	// An external reference is a column and an index, never a constraint:
	// module isolation depends on there being nothing to migrate across the
	// boundary (ADR-0015).
	target := build(func(r *schema.Registry) {
		r.Table("invoices",
			schema.UUIDv7("id").PrimaryKey(),
			schema.ExternalRef("tenant", "tenants.id"),
		)
	})
	changes := diff(t, nil, target)
	for _, c := range changes {
		if strings.Contains(c.Up, "FOREIGN KEY") {
			t.Fatalf("external reference produced a foreign key:\n%s", c.Up)
		}
	}
	find(t, changes, `CREATE INDEX "invoices_tenant_id_idx"`)
}

func TestDiffNewTableIndexIsNotConcurrent(t *testing.T) {
	// A table created in the same migration is empty, so its indexes cannot
	// contend with anything. Requiring CONCURRENTLY would force the migration
	// into a second file for no benefit.
	target := build(func(r *schema.Registry) {
		r.Table("posts",
			schema.UUIDv7("id").PrimaryKey(),
			schema.Text("slug"),
		).Index("slug")
	})

	idx := find(t, diff(t, nil, target), "CREATE INDEX")
	if idx.Stage == migrate.StageConcurrent {
		t.Fatal("index on a newly created table should not be concurrent")
	}
	if strings.Contains(idx.Up, "CONCURRENTLY") {
		t.Fatalf("unexpected CONCURRENTLY: %s", idx.Up)
	}
	if idx.Up != `CREATE INDEX "posts_slug_idx" ON "posts" ("slug");` {
		t.Fatalf("index SQL: %s", idx.Up)
	}
}

func TestDiffExistingTableIndexIsConcurrent(t *testing.T) {
	// The table already holds rows, so building the index without
	// CONCURRENTLY would lock it against writes for the duration.
	current := build(func(r *schema.Registry) {
		r.Table("posts", schema.UUIDv7("id").PrimaryKey(), schema.Text("slug"))
	})
	target := build(func(r *schema.Registry) {
		r.Table("posts", schema.UUIDv7("id").PrimaryKey(), schema.Text("slug")).Index("slug")
	})

	idx := only(t, diff(t, current, target))
	if idx.Stage != migrate.StageConcurrent {
		t.Fatal("index on an existing table must be concurrent")
	}
	if idx.Up != `CREATE INDEX CONCURRENTLY "posts_slug_idx" ON "posts" ("slug");` {
		t.Fatalf("index SQL: %s", idx.Up)
	}
	if idx.Down != `DROP INDEX CONCURRENTLY "posts_slug_idx";` {
		t.Fatalf("index Down: %s", idx.Down)
	}
}

func TestDiffIndexRedefinedIsDropAndCreate(t *testing.T) {
	// Changing an index under the same name has to be a drop and a create;
	// recognising it as one change rather than two unrelated ones is what
	// keeps the drop ordered before the create.
	current := build(func(r *schema.Registry) {
		r.Table("posts", schema.UUIDv7("id").PrimaryKey(),
			schema.Text("slug"), schema.Text("author")).
			AddIndex(schema.Index{Name: "posts_lookup", Columns: []string{"slug"}})
	})
	target := build(func(r *schema.Registry) {
		r.Table("posts", schema.UUIDv7("id").PrimaryKey(),
			schema.Text("slug"), schema.Text("author")).
			AddIndex(schema.Index{Name: "posts_lookup", Columns: []string{"slug", "author"}, Unique: true})
	})

	changes := diff(t, current, target)
	if len(changes) != 2 {
		t.Fatalf("want a drop and a create, got:\n%s", render(changes))
	}
	if !strings.HasPrefix(changes[0].Up, "DROP INDEX") {
		t.Fatalf("drop must come first:\n%s", render(changes))
	}
	if changes[1].Up != `CREATE UNIQUE INDEX CONCURRENTLY "posts_lookup" ON "posts" ("slug", "author");` {
		t.Fatalf("create SQL: %s", changes[1].Up)
	}
}

func TestDiffPartialAndMethodIndex(t *testing.T) {
	target := build(func(r *schema.Registry) {
		r.Table("posts", schema.UUIDv7("id").PrimaryKey(), schema.JSON("meta")).
			AddIndex(schema.Index{
				Name:    "posts_meta_gin",
				Columns: []string{"meta"},
				Method:  "gin",
				Where:   `"meta" IS NOT NULL`,
			})
	})
	idx := find(t, diff(t, nil, target), "posts_meta_gin")
	want := `CREATE INDEX "posts_meta_gin" ON "posts" USING gin ("meta") WHERE "meta" IS NOT NULL;`
	if idx.Up != want {
		t.Fatalf("\n got: %s\nwant: %s", idx.Up, want)
	}
}

func TestDiffDropTable(t *testing.T) {
	current := build(func(r *schema.Registry) {
		r.Table("legacy", schema.UUIDv7("id").PrimaryKey(), schema.Text("note"))
	})

	c := only(t, diff(t, current, nil))
	if c.Up != `DROP TABLE "legacy";` {
		t.Fatalf("Up = %q", c.Up)
	}
	if !c.Destructive {
		t.Fatal("dropping a table must be destructive")
	}
	if c.Reason == "" {
		t.Fatal("a destructive change must give a reason")
	}
	// The Down restores the structure. It cannot restore the rows, and the
	// Reason says so rather than the Down pretending otherwise.
	if !strings.HasPrefix(c.Down, `CREATE TABLE "legacy"`) {
		t.Fatalf("Down = %q", c.Down)
	}
}

func TestDiffAddColumn(t *testing.T) {
	base := func(r *schema.Registry) {
		r.Table("posts", schema.UUIDv7("id").PrimaryKey())
	}

	cases := []struct {
		name        string
		column      *schema.Field
		wantUp      string
		destructive bool
	}{{
		name:   "nullable",
		column: schema.Text("subtitle").Nullable(),
		wantUp: `ALTER TABLE "posts" ADD COLUMN "subtitle" text;`,
	}, {
		name:   "not null with default",
		column: schema.BigInt("views").Default(schema.Value(0)),
		wantUp: `ALTER TABLE "posts" ADD COLUMN "views" bigint NOT NULL DEFAULT 0;`,
	}, {
		// Postgres 11+ adds a NOT NULL column with a default without a table
		// rewrite, but one without a default is simply rejected on any table
		// that has rows.
		name:        "not null without default",
		column:      schema.Text("title"),
		wantUp:      `ALTER TABLE "posts" ADD COLUMN "title" text NOT NULL;`,
		destructive: true,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := build(func(r *schema.Registry) {
				r.Table("posts", schema.UUIDv7("id").PrimaryKey(), tc.column)
			})
			c := only(t, diff(t, build(base), target))
			if c.Up != tc.wantUp {
				t.Fatalf("\n got: %s\nwant: %s", c.Up, tc.wantUp)
			}
			if c.Destructive != tc.destructive {
				t.Fatalf("Destructive = %v, want %v", c.Destructive, tc.destructive)
			}
			if c.Destructive && c.Reason == "" {
				t.Fatal("destructive change without a reason")
			}
		})
	}
}

// slugAndItsDependents is the schema shape that produced the bug these three
// tests cover: a column added NOT NULL with no default, carrying a unique
// constraint, an index and a hand-written CHECK. The add renders commented out,
// so none of the other three can run until somebody uncomments it.
func slugAndItsDependents(slug *schema.Field) func(r *schema.Registry) {
	return func(r *schema.Registry) {
		r.Table("orgs",
			schema.UUIDv7("id").PrimaryKey(),
			schema.Text("name"),
			slug,
		).Index("slug").Check("slug_not_blank", `"slug" <> ''`)
	}
}

func TestDiffDefersWhatACommentedOutColumnCarries(t *testing.T) {
	base := build(func(r *schema.Registry) {
		r.Table("orgs", schema.UUIDv7("id").PrimaryKey(), schema.Text("name"))
	})
	changes := diff(t, base, build(slugAndItsDependents(schema.Text("slug").Unique())))

	add := find(t, changes, `ADD COLUMN "slug"`)
	if !add.Destructive {
		t.Fatal("adding a NOT NULL column with no default must be destructive")
	}
	if add.DependsOn != "" {
		t.Errorf("the add itself waits for nothing: %q", add.DependsOn)
	}

	// Each of these names the column the commented-out statement would have
	// added, so each has to be commented out with it. The note says how well
	// that is known: the column list of a UNIQUE or an index is built here and
	// is exact, while a hand-written CHECK expression is not read at all, so it
	// waits on the possibility rather than on a match.
	for _, tc := range []struct{ substr, prefix string }{
		{`ADD CONSTRAINT "orgs_slug_key"`, `"orgs"."slug"`},
		{`CREATE INDEX CONCURRENTLY "orgs_slug_idx"`, `"orgs"."slug"`},
		{`ADD CONSTRAINT "slug_not_blank"`, `possibly "orgs"."slug"`},
	} {
		c := find(t, changes, tc.substr)
		if c.DependsOn == "" {
			t.Errorf("%s must wait for the commented-out column add", tc.substr)
			continue
		}
		if !strings.HasPrefix(c.DependsOn, tc.prefix) {
			t.Errorf("%s should say it waits for %s, got: %s", tc.substr, tc.prefix, c.DependsOn)
		}
	}

	// The whole point: the migration is a no-op until reviewed, rather than a
	// file that fails partway through.
	for name, body := range renderFiles(t, changes) {
		if live := liveSQL(body); len(live) > 0 {
			t.Errorf("%s still executes %d statement(s) that depend on a commented-out "+
				"column:\n%s", name, len(live), strings.Join(live, "\n"))
		}
	}
}

// TestDiffDefersNothingWhenTheColumnIsLive is the other direction, without
// which the test above proves only that something is commented out (ADR-0016).
// The same schema with a nullable column has no commented-out add, so nothing
// waits for one and every statement is emitted live.
func TestDiffDefersNothingWhenTheColumnIsLive(t *testing.T) {
	base := build(func(r *schema.Registry) {
		r.Table("orgs", schema.UUIDv7("id").PrimaryKey(), schema.Text("name"))
	})
	changes := diff(t, base, build(slugAndItsDependents(schema.Text("slug").Unique().Nullable())))

	for _, c := range changes {
		if c.DependsOn != "" {
			t.Errorf("nothing is commented out here, so nothing waits: %s\n%s", c.Up, c.DependsOn)
		}
	}
	for name, body := range renderFiles(t, changes) {
		if len(liveSQL(body)) == 0 {
			t.Errorf("%s should hold live SQL:\n%s", name, body)
		}
	}
}

// TestDiffDefersOnlyWhatNamesTheColumn. The dependency is on a column, not on a
// table: a change made in the same migration that does not name the pending one
// stays live, or a single destructive add would freeze everything around it.
func TestDiffDefersOnlyWhatNamesTheColumn(t *testing.T) {
	base := build(func(r *schema.Registry) {
		r.Table("orgs", schema.UUIDv7("id").PrimaryKey(), schema.Text("name"))
	})
	target := build(func(r *schema.Registry) {
		r.Table("orgs",
			schema.UUIDv7("id").PrimaryKey(),
			schema.Text("name"),
			schema.Text("slug").Unique(),     // NOT NULL, no default: commented out
			schema.Text("region").Nullable(), // ordinary, and indexed below
		).Index("name").Index("region")
	})
	changes := diff(t, base, target)

	if c := find(t, changes, `"orgs_slug_key"`); c.DependsOn == "" {
		t.Error("the constraint over the pending column must wait for it")
	}
	for _, substr := range []string{`"orgs_name_idx"`, `"orgs_region_idx"`, `ADD COLUMN "region"`} {
		if c := find(t, changes, substr); c.DependsOn != "" {
			t.Errorf("%s does not name the pending column and must stay live: %s", substr, c.DependsOn)
		}
	}
}

func TestDiffDropColumn(t *testing.T) {
	current := build(func(r *schema.Registry) {
		r.Table("posts", schema.UUIDv7("id").PrimaryKey(), schema.Text("legacy_slug").Nullable())
	})
	target := build(func(r *schema.Registry) {
		r.Table("posts", schema.UUIDv7("id").PrimaryKey())
	})

	c := only(t, diff(t, current, target))
	if c.Up != `ALTER TABLE "posts" DROP COLUMN "legacy_slug";` {
		t.Fatalf("Up = %q", c.Up)
	}
	if !c.Destructive || c.Reason == "" {
		t.Fatalf("dropping a column must be destructive with a reason: %+v", c)
	}
	if c.Down != `ALTER TABLE "posts" ADD COLUMN "legacy_slug" text;` {
		t.Fatalf("Down = %q", c.Down)
	}
}

func TestDiffRenameIsDropAndAdd(t *testing.T) {
	// A rename cannot be told from a drop and an add when only the before and
	// after states are known. Emitting drop-and-add is lossy but never
	// silently wrong, and the destructive guard keeps it commented out.
	current := build(func(r *schema.Registry) {
		r.Table("posts", schema.UUIDv7("id").PrimaryKey(), schema.Text("headline").Nullable())
	})
	target := build(func(r *schema.Registry) {
		r.Table("posts", schema.UUIDv7("id").PrimaryKey(), schema.Text("title").Nullable())
	})

	changes := diff(t, current, target)
	add := find(t, changes, `ADD COLUMN "title"`)
	drop := find(t, changes, `DROP COLUMN "headline"`)
	if add.Destructive {
		t.Error("the add half is not destructive")
	}
	if !drop.Destructive {
		t.Error("the drop half must be destructive")
	}
	if indexOf(ups(changes), `ADD COLUMN "title"`) > indexOf(ups(changes), `DROP COLUMN "headline"`) {
		t.Fatalf("the add must precede the drop:\n%s", render(changes))
	}
}

func TestDiffColumnType(t *testing.T) {
	cases := []struct {
		name        string
		from, to    *schema.Field
		wantType    string
		destructive bool
	}{
		{"int to bigint widens", schema.Int("n"), schema.BigInt("n"), "bigint", false},
		{"bigint to int narrows", schema.BigInt("n"), schema.Int("n"), "integer", true},
		{"int to numeric widens", schema.Int("n"), schema.Numeric("n"), "numeric", false},
		{"varchar to text widens", schema.Varchar("n", 50), schema.Text("n"), "text", false},
		{"text to varchar narrows", schema.Text("n"), schema.Varchar("n", 50), "varchar(50)", true},
		{"longer varchar widens", schema.Varchar("n", 50), schema.Varchar("n", 100), "varchar(100)", false},
		{"shorter varchar narrows", schema.Varchar("n", 100), schema.Varchar("n", 50), "varchar(50)", true},
		{"unrelated type narrows", schema.Text("n"), schema.JSON("n"), "jsonb", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			table := func(f *schema.Field) func(*schema.Registry) {
				return func(r *schema.Registry) {
					r.Table("t", schema.UUIDv7("id").PrimaryKey(), f)
				}
			}
			c := only(t, diff(t, build(table(tc.from)), build(table(tc.to))))
			want := `ALTER TABLE "t" ALTER COLUMN "n" TYPE ` + tc.wantType + `;`
			if c.Up != want {
				t.Fatalf("\n got: %s\nwant: %s", c.Up, want)
			}
			if c.Destructive != tc.destructive {
				t.Fatalf("Destructive = %v, want %v", c.Destructive, tc.destructive)
			}
			// No USING clause is generated: Postgres refusing a cast it cannot
			// make implicitly is the correct outcome, and a generated USING
			// would silently truncate on the narrowing cases.
			if strings.Contains(c.Up, "USING") {
				t.Fatalf("USING should never be generated: %s", c.Up)
			}
		})
	}
}

func TestDiffNullability(t *testing.T) {
	table := func(f *schema.Field) func(*schema.Registry) {
		return func(r *schema.Registry) {
			r.Table("t", schema.UUIDv7("id").PrimaryKey(), f)
		}
	}

	t.Run("set not null", func(t *testing.T) {
		c := only(t, diff(t,
			build(table(schema.Text("n").Nullable())),
			build(table(schema.Text("n")))))
		if c.Up != `ALTER TABLE "t" ALTER COLUMN "n" SET NOT NULL;` {
			t.Fatalf("Up = %q", c.Up)
		}
		// The fix for a failure here is a backfill, not a retry, so it is
		// worth stopping a reviewer on.
		if !c.Destructive || c.Reason == "" {
			t.Fatalf("SET NOT NULL must be flagged with a reason: %+v", c)
		}
	})

	t.Run("drop not null", func(t *testing.T) {
		c := only(t, diff(t,
			build(table(schema.Text("n"))),
			build(table(schema.Text("n").Nullable()))))
		if c.Up != `ALTER TABLE "t" ALTER COLUMN "n" DROP NOT NULL;` {
			t.Fatalf("Up = %q", c.Up)
		}
		if c.Destructive {
			t.Fatal("relaxing a constraint loses nothing")
		}
	})
}

func TestDiffDefault(t *testing.T) {
	table := func(f *schema.Field) func(*schema.Registry) {
		return func(r *schema.Registry) {
			r.Table("t", schema.UUIDv7("id").PrimaryKey(), f)
		}
	}

	cases := []struct {
		name     string
		from, to *schema.Field
		wantUp   string
		wantDown string
	}{{
		name:     "added",
		from:     schema.Int("n").Nullable(),
		to:       schema.Int("n").Nullable().Default(schema.Value(7)),
		wantUp:   `ALTER TABLE "t" ALTER COLUMN "n" SET DEFAULT 7;`,
		wantDown: `ALTER TABLE "t" ALTER COLUMN "n" DROP DEFAULT;`,
	}, {
		name:     "removed",
		from:     schema.Int("n").Nullable().Default(schema.Value(7)),
		to:       schema.Int("n").Nullable(),
		wantUp:   `ALTER TABLE "t" ALTER COLUMN "n" DROP DEFAULT;`,
		wantDown: `ALTER TABLE "t" ALTER COLUMN "n" SET DEFAULT 7;`,
	}, {
		name:     "changed to an expression",
		from:     schema.Timestamp("n").Nullable(),
		to:       schema.Timestamp("n").Nullable().Default(schema.Now()),
		wantUp:   `ALTER TABLE "t" ALTER COLUMN "n" SET DEFAULT now();`,
		wantDown: `ALTER TABLE "t" ALTER COLUMN "n" DROP DEFAULT;`,
	}, {
		name:     "string literal is escaped",
		from:     schema.Text("n").Nullable(),
		to:       schema.Text("n").Nullable().Default(schema.Value("it's")),
		wantUp:   `ALTER TABLE "t" ALTER COLUMN "n" SET DEFAULT 'it''s';`,
		wantDown: `ALTER TABLE "t" ALTER COLUMN "n" DROP DEFAULT;`,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := only(t, diff(t, build(table(tc.from)), build(table(tc.to))))
			if c.Up != tc.wantUp {
				t.Fatalf("Up\n got: %s\nwant: %s", c.Up, tc.wantUp)
			}
			if c.Down != tc.wantDown {
				t.Fatalf("Down\n got: %s\nwant: %s", c.Down, tc.wantDown)
			}
			if c.Destructive {
				t.Fatal("changing a default touches no existing row")
			}
			// Which is exactly what a reviewer needs told.
			if !strings.Contains(c.Comment, "not backfilled") {
				t.Fatalf("comment should warn that existing rows keep their values: %q", c.Comment)
			}
		})
	}
}

func TestDiffUniqueConstraint(t *testing.T) {
	plain := func(r *schema.Registry) {
		r.Table("users", schema.UUIDv7("id").PrimaryKey(), schema.Text("email"))
	}
	unique := func(r *schema.Registry) {
		r.Table("users", schema.UUIDv7("id").PrimaryKey(), schema.Text("email").Unique())
	}

	t.Run("added", func(t *testing.T) {
		c := only(t, diff(t, build(plain), build(unique)))
		want := `ALTER TABLE "users" ADD CONSTRAINT "users_email_key" UNIQUE ("email");`
		if c.Up != want {
			t.Fatalf("\n got: %s\nwant: %s", c.Up, want)
		}
		if c.Destructive {
			t.Fatal("adding a unique constraint fails loudly, it does not lose data")
		}
		if !strings.Contains(c.Comment, "existing rows") {
			t.Fatalf("comment should warn about existing rows: %q", c.Comment)
		}
	})

	t.Run("dropped", func(t *testing.T) {
		c := only(t, diff(t, build(unique), build(plain)))
		if c.Up != `ALTER TABLE "users" DROP CONSTRAINT "users_email_key";` {
			t.Fatalf("Up = %q", c.Up)
		}
		if c.Down != `ALTER TABLE "users" ADD CONSTRAINT "users_email_key" UNIQUE ("email");` {
			t.Fatalf("Down = %q", c.Down)
		}
	})
}

func TestDiffPinnedConstraintNames(t *testing.T) {
	// Adopting an existing database depends on this: a schema whose
	// constraint names do not match the ones already there would produce a
	// diff that drops and recreates every constraint on the first run.
	current := build(func(r *schema.Registry) {
		r.Table("users",
			schema.UUIDv7("id").PrimaryKey(),
			schema.Text("email").Unique().ConstraintNamed("uq_user_email"),
		).PrimaryKeyNamed("users_id_pk")
	})
	if changes := diff(t, current, current); len(changes) != 0 {
		t.Fatalf("a schema against itself must be empty, got:\n%s", render(changes))
	}

	c := only(t, diff(t, nil, current))
	for _, want := range []string{`CONSTRAINT "users_id_pk" PRIMARY KEY`, `CONSTRAINT "uq_user_email" UNIQUE`} {
		if !strings.Contains(c.Up, want) {
			t.Errorf("missing %s in:\n%s", want, c.Up)
		}
	}
}

func TestDiffEnumValues(t *testing.T) {
	table := func(values ...string) func(*schema.Registry) {
		return func(r *schema.Registry) {
			r.Table("posts", schema.UUIDv7("id").PrimaryKey(), schema.Enum("status", values...))
		}
	}

	t.Run("value added", func(t *testing.T) {
		changes := diff(t, build(table("draft", "live")), build(table("draft", "live", "archived")))
		if len(changes) != 2 {
			t.Fatalf("want a drop and an add, got:\n%s", render(changes))
		}
		if changes[0].Up != `ALTER TABLE "posts" DROP CONSTRAINT "posts_status_check";` {
			t.Fatalf("drop first: %s", changes[0].Up)
		}
		want := `ALTER TABLE "posts" ADD CONSTRAINT "posts_status_check" ` +
			`CHECK ("status" IN ('draft', 'live', 'archived'));`
		if changes[1].Up != want {
			t.Fatalf("\n got: %s\nwant: %s", changes[1].Up, want)
		}
		if strings.Contains(changes[1].Comment, "no longer permits") {
			t.Fatalf("nothing was removed: %q", changes[1].Comment)
		}
	})

	t.Run("value removed", func(t *testing.T) {
		changes := diff(t, build(table("draft", "live", "archived")), build(table("draft", "live")))
		add := find(t, changes, "ADD CONSTRAINT")
		// Removing a value cannot lose data — Postgres rejects the statement —
		// but the fix is in the rows, not in the schema, so it is named.
		if add.Destructive {
			t.Fatal("a rejected statement is not data loss")
		}
		if !strings.Contains(add.Comment, `no longer permits 'archived'`) {
			t.Fatalf("comment should name the removed value: %q", add.Comment)
		}
	})
}

func TestDiffForeignKeyOrdering(t *testing.T) {
	// A foreign key depends on the unique or primary key it points at, so its
	// drop must come before that constraint's, and its add after.
	current := build(orgsAndUsers)
	target := build(func(r *schema.Registry) {
		r.Table("orgs", schema.UUIDv7("id").PrimaryKey(), schema.Text("name"))
		r.Table("users", schema.UUIDv7("id").PrimaryKey(), schema.Text("email").Unique())
	})

	changes := diff(t, current, target)
	order := ups(changes)
	fkDrop := indexOf(order, `DROP CONSTRAINT "users_org_id_fkey"`)
	colDrop := indexOf(order, `DROP COLUMN "org_id"`)
	if fkDrop == -1 || colDrop == -1 {
		t.Fatalf("expected both a constraint drop and a column drop:\n%s", render(changes))
	}
	if fkDrop > colDrop {
		t.Fatalf("the foreign key must be dropped before its column:\n%s", render(changes))
	}
}

func TestDiffPhaseOrdering(t *testing.T) {
	current := build(func(r *schema.Registry) {
		r.Table("posts",
			schema.UUIDv7("id").PrimaryKey(),
			schema.Text("legacy").Nullable(),
			schema.Text("kept").Nullable(),
		).Index("kept")
		r.Table("tags", schema.UUIDv7("id").PrimaryKey())
	})
	target := build(func(r *schema.Registry) {
		r.Table("posts",
			schema.UUIDv7("id").PrimaryKey(),
			schema.Text("title").Nullable(),
			schema.Text("kept").Nullable(),
		)
		r.Table("authors", schema.UUIDv7("id").PrimaryKey())
	})

	got := ups(diff(t, current, target))
	want := []string{
		`CREATE TABLE "authors" (` + "\n" +
			`    "id" uuid NOT NULL DEFAULT uuid_generate_v7(),` + "\n" +
			`    CONSTRAINT "authors_pkey" PRIMARY KEY ("id")` + "\n" + `);`,
		`DROP INDEX CONCURRENTLY "posts_kept_idx";`,
		`ALTER TABLE "posts" ADD COLUMN "title" text;`,
		`ALTER TABLE "posts" DROP COLUMN "legacy";`,
		`DROP TABLE "tags";`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("phase order\n got: %#v\nwant: %#v", got, want)
	}
}

func TestDiffIndexOverADroppedColumnStaysInTheSameFile(t *testing.T) {
	// A concurrent index change is split into a file that runs after the one
	// holding the column drop — by which time Postgres has already dropped the
	// index along with the column, and DROP INDEX fails. So this one gives up
	// CONCURRENTLY, which costs nothing: DROP COLUMN takes an ACCESS EXCLUSIVE
	// lock on the same table moments later regardless.
	current := build(func(r *schema.Registry) {
		r.Table("posts",
			schema.UUIDv7("id").PrimaryKey(),
			schema.Text("legacy").Nullable(),
			schema.Text("kept").Nullable(),
		).Index("legacy").Index("kept")
	})
	target := build(func(r *schema.Registry) {
		r.Table("posts",
			schema.UUIDv7("id").PrimaryKey(),
			schema.Text("kept").Nullable(),
		)
	})

	changes := diff(t, current, target)

	legacy := find(t, changes, `"posts_legacy_idx"`)
	if legacy.Stage == migrate.StageConcurrent {
		t.Fatalf("this drop must stay with the column drop:\n%s", render(changes))
	}
	if legacy.Up != `DROP INDEX "posts_legacy_idx";` {
		t.Fatalf("Up = %q", legacy.Up)
	}
	// It must still be reversible: the Down recreates it after the column
	// comes back.
	if legacy.Down != `CREATE INDEX "posts_legacy_idx" ON "posts" ("legacy");` {
		t.Fatalf("Down = %q", legacy.Down)
	}

	// The other direction: an index whose columns all survive keeps
	// CONCURRENTLY, because nothing else is locking that table.
	kept := find(t, changes, `"posts_kept_idx"`)
	if kept.Stage != migrate.StageConcurrent {
		t.Fatalf("an ordinary index drop should stay concurrent:\n%s", render(changes))
	}

	// Both must precede the column drop within the change list.
	order := ups(changes)
	if indexOf(order, `DROP INDEX "posts_legacy_idx"`) > indexOf(order, `DROP COLUMN "legacy"`) {
		t.Fatalf("the index drop must precede the column drop:\n%s", render(changes))
	}

	// And the whole thing must render: the non-concurrent drop in the main
	// file, ahead of the column drop it depends on.
	files, err := migrate.Render(migrate.Migration{
		Version: "1", Name: "drop_legacy", Changes: changes,
	}, migrate.Options{AllowDestructive: true})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	main := files["1_drop_legacy.sql"]
	if !strings.Contains(main, `DROP INDEX "posts_legacy_idx";`) {
		t.Fatalf("index drop belongs in the main file:\n%s", main)
	}
	if strings.Index(main, `DROP INDEX "posts_legacy_idx"`) > strings.Index(main, `DROP COLUMN "legacy"`) {
		t.Fatalf("wrong order in the rendered file:\n%s", main)
	}
}

func TestDiffIsDeterministic(t *testing.T) {
	// A migration that reorders itself between runs is a diff nobody can
	// review, and map iteration is the obvious way to get one by accident.
	current := build(orgsAndUsers)
	target := build(func(r *schema.Registry) {
		orgs := r.Table("orgs",
			schema.UUIDv7("id").PrimaryKey(),
			schema.Text("name").Unique(),
			schema.Text("slug").Nullable(),
		).Index("slug")
		r.Table("users",
			schema.UUIDv7("id").PrimaryKey(),
			schema.Text("email"),
			schema.Ref("org", orgs),
		)
	})

	first := ups(diff(t, current, target))
	if len(first) < 5 {
		t.Fatalf("expected a change set worth checking, got %d", len(first))
	}
	for i := 0; i < 10; i++ {
		if got := ups(diff(t, current, target)); !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d differs\n got: %#v\nwant: %#v", i, got, first)
		}
	}
}

func TestDiffRendersAsAMigration(t *testing.T) {
	// Render validates what Diff must guarantee: every change has Up SQL, and
	// every destructive one gives a reason. Running the two together is the
	// cheapest check that the engine cannot emit a file the renderer rejects.
	current := build(orgsAndUsers)
	target := build(func(r *schema.Registry) {
		r.Table("orgs", schema.UUIDv7("id").PrimaryKey(), schema.Text("name")).Index("name")
	})

	files, err := migrate.Render(migrate.Migration{
		Version: "20260727150000",
		Name:    "drop_users",
		Changes: diff(t, current, target),
	}, migrate.Options{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	// The concurrent index change is split into its own file, because
	// NO TRANSACTION is file-scoped in goose.
	if len(files) != 2 {
		t.Fatalf("want 2 files (ordinary + indexes), got %d: %v", len(files), keys(files))
	}
	main := files["20260727150000_drop_users.sql"]
	if !strings.Contains(main, `-- DROP TABLE "users";`) {
		t.Errorf("the table drop should render commented out by default:\n%s", main)
	}
	// DROP TABLE takes the table's own constraints and indexes with it, so
	// emitting them separately would be noise that also fails on replay.
	if strings.Contains(main, "DROP CONSTRAINT") {
		t.Errorf("a dropped table needs no constraint drops:\n%s", main)
	}
}

func TestDiffRejectsInvalidSchema(t *testing.T) {
	// A schema that does not validate would produce DDL for a table that
	// cannot exist. Failing here beats failing halfway through a migration.
	target := build(func(r *schema.Registry) {
		r.Table("posts", schema.UUIDv7("id").PrimaryKey(), schema.Int("views").Searchable())
	})
	if _, err := migrate.Diff(nil, target); err == nil {
		t.Fatal("want an error for an invalid target schema")
	} else if !strings.Contains(err.Error(), "target schema is not valid") {
		t.Fatalf("error should say which side is invalid: %v", err)
	}

	if _, err := migrate.Diff(target, nil); err == nil {
		t.Fatal("want an error for an invalid current schema")
	} else if !strings.Contains(err.Error(), "current schema is not valid") {
		t.Fatalf("error should say which side is invalid: %v", err)
	}
}

func TestDiffComments(t *testing.T) {
	current := build(func(r *schema.Registry) {
		r.Table("posts",
			schema.UUIDv7("id").PrimaryKey(),
			schema.Text("body").Comment("the text"),
		).Describe("articles")
	})
	target := build(func(r *schema.Registry) {
		r.Table("posts",
			schema.UUIDv7("id").PrimaryKey(),
			schema.Text("body").Comment("markdown source"),
		).Describe("published articles")
	})

	changes := diff(t, current, target)
	col := find(t, changes, "COMMENT ON COLUMN")
	if col.Up != `COMMENT ON COLUMN "posts"."body" IS 'markdown source';` {
		t.Fatalf("Up = %q", col.Up)
	}
	if col.Down != `COMMENT ON COLUMN "posts"."body" IS 'the text';` {
		t.Fatalf("Down = %q", col.Down)
	}

	tbl := find(t, changes, "COMMENT ON TABLE")
	if tbl.Up != `COMMENT ON TABLE "posts" IS 'published articles';` {
		t.Fatalf("Up = %q", tbl.Up)
	}
}

func TestDiffCommentRemoved(t *testing.T) {
	// Postgres removes a comment by setting it to NULL, which is not the same
	// as setting it to the empty string.
	current := build(func(r *schema.Registry) {
		r.Table("posts", schema.UUIDv7("id").PrimaryKey()).Describe("articles")
	})
	target := build(func(r *schema.Registry) {
		r.Table("posts", schema.UUIDv7("id").PrimaryKey())
	})

	c := only(t, diff(t, current, target))
	if c.Up != `COMMENT ON TABLE "posts" IS NULL;` {
		t.Fatalf("Up = %q", c.Up)
	}
}

func indexOf(haystack []string, needle string) int {
	for i, s := range haystack {
		if strings.Contains(s, needle) {
			return i
		}
	}
	return -1
}
