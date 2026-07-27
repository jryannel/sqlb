package sqlb_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jryannel/sqlb"
)

// The model under test mirrors what codegen emits from a schema declaration:
// db tags for column names, sqlb tags for capabilities.
type User struct {
	ID        string    `db:"id" sqlb:"pk,default"`
	Email     string    `db:"email" sqlb:"filter,search"`
	Name      string    `db:"name" sqlb:"filter,search,sort"`
	Age       *int32    `db:"age" sqlb:"filter,sort"`
	OrgID     string    `db:"org_id" sqlb:"filter"`
	Password  string    `db:"password_hash" sqlb:"hidden"`
	CreatedAt time.Time `db:"created_at" sqlb:"sort,readonly,default"`
}

func (User) TableName() string { return "users" }

func TestModelReflection(t *testing.T) {
	m := sqlb.ModelOf[User]()

	if m.Table != "users" {
		t.Errorf("table = %q, want %q", m.Table, "users")
	}
	if m.PK == nil || m.PK.Name != "id" {
		t.Fatalf("primary key = %v, want id", m.PK)
	}
	if !m.PK.HasDefault {
		t.Error("id should carry HasDefault from its `default` tag")
	}
	if got, want := len(m.Columns), 7; got != want {
		t.Errorf("mapped %d columns, want %d", got, want)
	}
	if col := m.Column("password_hash"); col == nil || !col.Hidden {
		t.Error("password_hash should be mapped and hidden")
	}
	if got, want := len(m.Selectable()), 6; got != want {
		t.Errorf("Selectable returned %d columns, want %d (hidden excluded)", got, want)
	}
	// search implies filter, so that ?email=... works on a searchable column.
	if col := m.Column("email"); !col.Filterable {
		t.Error("a searchable column should also be filterable")
	}
}

func TestTableNameDerivation(t *testing.T) {
	type Category struct {
		ID string `db:"id"`
	}
	type Box struct {
		ID string `db:"id"`
	}
	if got := sqlb.ModelOf[Category]().Table; got != "categories" {
		t.Errorf("Category maps to %q, want %q", got, "categories")
	}
	if got := sqlb.ModelOf[Box]().Table; got != "boxes" {
		t.Errorf("Box maps to %q, want %q", got, "boxes")
	}
}

func TestSelectSQL(t *testing.T) {
	tests := []struct {
		name string
		q    func() *sqlb.Builder[User]
		sql  string
		args []any
	}{
		{
			name: "all columns",
			q:    func() *sqlb.Builder[User] { return sqlb.Query[User]() },
			sql:  `SELECT "users"."id", "users"."email", "users"."name", "users"."age", "users"."org_id", "users"."password_hash", "users"."created_at" FROM "users"`,
		},
		{
			name: "single predicate needs no parentheses",
			q: func() *sqlb.Builder[User] {
				return sqlb.Query[User]().Select(sqlb.F("id")).Where(sqlb.F("age").Gte(18))
			},
			sql:  `SELECT "id" FROM "users" WHERE "age" >= $1`,
			args: []any{18},
		},
		{
			name: "conjunction is parenthesised so precedence never depends on order",
			q: func() *sqlb.Builder[User] {
				return sqlb.Query[User]().Select(sqlb.F("id")).
					Where(sqlb.F("age").Gte(18)).
					Where(sqlb.F("org_id").Eq("acme"))
			},
			sql:  `SELECT "id" FROM "users" WHERE ("age" >= $1) AND ("org_id" = $2)`,
			args: []any{18, "acme"},
		},
		{
			name: "disjunction nested in a conjunction",
			q: func() *sqlb.Builder[User] {
				return sqlb.Query[User]().Select(sqlb.F("id")).Where(
					sqlb.F("org_id").Eq("acme"),
					sqlb.Or(sqlb.F("age").Lt(18), sqlb.F("age").Gt(65)),
				)
			},
			sql:  `SELECT "id" FROM "users" WHERE ("org_id" = $1) AND (("age" < $2) OR ("age" > $3))`,
			args: []any{"acme", 18, 65},
		},
		{
			name: "in list",
			q: func() *sqlb.Builder[User] {
				return sqlb.Query[User]().Select(sqlb.F("id")).Where(sqlb.F("org_id").OneOf("a", "b"))
			},
			sql:  `SELECT "id" FROM "users" WHERE "org_id" IN ($1, $2)`,
			args: []any{"a", "b"},
		},
		{
			name: "between renders without parenthesising its bounds",
			q: func() *sqlb.Builder[User] {
				return sqlb.Query[User]().Select(sqlb.F("id")).Where(sqlb.F("age").Between(18, 65))
			},
			sql:  `SELECT "id" FROM "users" WHERE "age" BETWEEN $1 AND $2`,
			args: []any{18, 65},
		},
		{
			name: "null test",
			q: func() *sqlb.Builder[User] {
				return sqlb.Query[User]().Select(sqlb.F("id")).Where(sqlb.F("age").IsNull())
			},
			sql: `SELECT "id" FROM "users" WHERE "age" IS NULL`,
		},
		{
			name: "ordering and pagination",
			q: func() *sqlb.Builder[User] {
				return sqlb.Query[User]().Select(sqlb.F("id")).
					OrderBy(sqlb.F("created_at").Desc().NullsLast(), sqlb.F("name").Asc()).
					Page(3, 20)
			},
			sql: `SELECT "id" FROM "users" ORDER BY "created_at" DESC NULLS LAST, "name" ASC LIMIT 20 OFFSET 40`,
		},
		{
			name: "grouped aggregate",
			q: func() *sqlb.Builder[User] {
				return sqlb.Query[User]().
					Select(sqlb.F("org_id"), sqlb.Count().As("n"), sqlb.Avg(sqlb.F("age")).As("avg_age")).
					GroupBy(sqlb.F("org_id")).
					Having(sqlb.RawPred("count(*) > ?", 5))
			},
			sql:  `SELECT "org_id", count(*) AS "n", avg("age") AS "avg_age" FROM "users" GROUP BY "org_id" HAVING count(*) > $1`,
			args: []any{5},
		},
		{
			name: "join with alias",
			q: func() *sqlb.Builder[User] {
				return sqlb.Query[User]().Select(sqlb.F("users.id"), sqlb.F("o.name")).
					Join("orgs", "o", sqlb.F("users.org_id").EqField(sqlb.F("o.id")))
			},
			sql: `SELECT "users"."id", "o"."name" FROM "users" JOIN "orgs" AS "o" ON "users"."org_id" = "o"."id"`,
		},
		{
			name: "locking",
			q: func() *sqlb.Builder[User] {
				return sqlb.Query[User]().Select(sqlb.F("id")).
					Where(sqlb.F("id").Eq("u1")).ForUpdate().SkipLocked()
			},
			sql:  `SELECT "id" FROM "users" WHERE "id" = $1 FOR UPDATE SKIP LOCKED`,
			args: []any{"u1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, args, err := tt.q().SQL()
			if err != nil {
				t.Fatalf("SQL() error: %v", err)
			}
			if got != tt.sql {
				t.Errorf("SQL mismatch\n got: %s\nwant: %s", got, tt.sql)
			}
			if !reflect.DeepEqual(normalise(args), normalise(tt.args)) {
				t.Errorf("args = %#v, want %#v", args, tt.args)
			}
		})
	}
}

// TestConditionalComposition covers the reason this builder exists: a filter
// that is absent must leave no trace in the SQL, without an if statement at
// every call site.
func TestConditionalComposition(t *testing.T) {
	build := func(search, org string, minAge int) string {
		q := sqlb.Query[User]().Select(sqlb.F("id")).
			Where(
				sqlb.If(search != "", sqlb.F("name").Contains(search)),
				sqlb.If(org != "", sqlb.F("org_id").Eq(org)),
				sqlb.If(minAge > 0, sqlb.F("age").Gte(minAge)),
			)
		sql, _, err := q.SQL()
		if err != nil {
			t.Fatalf("SQL() error: %v", err)
		}
		return sql
	}

	if got, want := build("", "", 0), `SELECT "id" FROM "users"`; got != want {
		t.Errorf("no filters:\n got: %s\nwant: %s", got, want)
	}
	if got, want := build("", "acme", 0), `SELECT "id" FROM "users" WHERE "org_id" = $1`; got != want {
		t.Errorf("one filter:\n got: %s\nwant: %s", got, want)
	}
	// And folds left, so the nesting is ((a AND b) AND c).
	want := `SELECT "id" FROM "users" WHERE (("name" ILIKE $1) AND ("org_id" = $2)) AND ("age" >= $3)`
	if got := build("ada", "acme", 18); got != want {
		t.Errorf("three filters:\n got: %s\nwant: %s", got, want)
	}
}

// TestLikeEscaping guards the case where a user types a wildcard into a search
// box and would otherwise match everything.
func TestLikeEscaping(t *testing.T) {
	_, args, err := sqlb.Query[User]().Select(sqlb.F("id")).
		Where(sqlb.F("name").Contains("100%_off")).SQL()
	if err != nil {
		t.Fatalf("SQL() error: %v", err)
	}
	if got, want := args[0], `%100\%\_off%`; got != want {
		t.Errorf("pattern = %q, want %q", got, want)
	}
}

func TestCountSQL(t *testing.T) {
	h := newHarness(t, []string{"count"}, [][]driver.Value{{int64(3)}})
	defer h.close()

	q := sqlb.Query[User]().Where(sqlb.F("org_id").Eq("acme")).
		OrderBy(sqlb.F("name").Asc()).Page(2, 10)

	n, err := q.Count(context.Background(), h.db)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 3 {
		t.Errorf("count = %d, want 3", n)
	}
	// Ordering and pagination must not survive into the count, or a paged list
	// would report its page size as the total.
	want := `SELECT count(*) FROM "users" WHERE "org_id" = $1`
	if got := h.lastQuery(); got != want {
		t.Errorf("count SQL\n got: %s\nwant: %s", got, want)
	}
}

func TestInsertOmitsDefaultedZeroColumns(t *testing.T) {
	u := &User{Email: "ada@example.com", Name: "Ada", OrgID: "acme"}
	sql, args, err := sqlb.InsertRows(u).SQL()
	if err != nil {
		t.Fatalf("SQL() error: %v", err)
	}
	// id and created_at carry database defaults and hold zero values, so they
	// are left for the database to fill.
	want := `INSERT INTO "users" ("email", "name", "age", "org_id", "password_hash") VALUES ($1, $2, $3, $4, $5)` +
		` RETURNING "id", "email", "name", "age", "org_id", "password_hash", "created_at"`
	if sql != want {
		t.Errorf("insert SQL\n got: %s\nwant: %s", sql, want)
	}
	if len(args) != 5 {
		t.Errorf("bound %d args, want 5", len(args))
	}
}

func TestInsertKeepsExplicitValueOverDefault(t *testing.T) {
	u := &User{ID: "fixed-id", Email: "ada@example.com"}
	sql, _, err := sqlb.InsertRows(u).SQL()
	if err != nil {
		t.Fatalf("SQL() error: %v", err)
	}
	if !contains(sql, `"id"`) {
		t.Errorf("an explicitly set id must be written, got: %s", sql)
	}
}

func TestUpsert(t *testing.T) {
	u := &User{Email: "ada@example.com", Name: "Ada"}
	sql, _, err := sqlb.InsertRows(u).OnConflictUpdate([]string{"email"}, "name").SQL()
	if err != nil {
		t.Fatalf("SQL() error: %v", err)
	}
	if !contains(sql, `ON CONFLICT ("email") DO UPDATE SET "name" = EXCLUDED."name"`) {
		t.Errorf("upsert clause missing from: %s", sql)
	}
}

func TestUnscopedMutationsAreRefused(t *testing.T) {
	if _, _, err := sqlb.UpdateRows[User]().Set("name", "x").SQL(); !errors.Is(err, sqlb.ErrUnscoped) {
		t.Errorf("unscoped update error = %v, want ErrUnscoped", err)
	}
	if _, _, err := sqlb.DeleteRows[User]().SQL(); !errors.Is(err, sqlb.ErrUnscoped) {
		t.Errorf("unscoped delete error = %v, want ErrUnscoped", err)
	}
	// Everything is the explicit opt-in.
	if _, _, err := sqlb.DeleteRows[User]().Everything().SQL(); err != nil {
		t.Errorf("Everything() should permit the delete, got %v", err)
	}
}

func TestUpdateRejectsUnknownColumn(t *testing.T) {
	_, _, err := sqlb.UpdateRows[User]().Set("nam", "typo").Where(sqlb.F("id").Eq("u1")).SQL()
	if err == nil {
		t.Fatal("expected an error naming the unknown column")
	}
	if !contains(err.Error(), `"nam"`) {
		t.Errorf("error should name the offending column, got: %v", err)
	}
}

// TestBeforeQueryHook covers the tenant-scoping path: one registration
// constrains every read of the model.
func TestBeforeQueryHook(t *testing.T) {
	type Post struct {
		ID    string `db:"id" sqlb:"pk"`
		OrgID string `db:"org_id" sqlb:"filter"`
		Title string `db:"title"`
	}

	hooks := sqlb.On[Post]()
	defer hooks.Reset()
	hooks.BeforeQuery(func(ctx context.Context, q *sqlb.Builder[Post]) error {
		q.Where(sqlb.F("org_id").Eq("acme"))
		return nil
	})

	h := newHarness(t, []string{"id", "org_id", "title"}, [][]driver.Value{{"p1", "acme", "Hello"}})
	defer h.close()

	posts, err := sqlb.Query[Post]().Where(sqlb.F("title").Eq("Hello")).All(context.Background(), h.db)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(posts) != 1 || posts[0].Title != "Hello" {
		t.Fatalf("rows = %#v", posts)
	}
	want := `SELECT "posts"."id", "posts"."org_id", "posts"."title" FROM "posts" WHERE ("title" = $1) AND ("org_id" = $2)`
	if got := h.lastQuery(); got != want {
		t.Errorf("hooked SQL\n got: %s\nwant: %s", got, want)
	}
}

// TestHooksDoNotAccumulate guards the in-place mutation design: running the
// same builder twice must not apply the hook's predicate twice.
func TestHooksDoNotAccumulate(t *testing.T) {
	type Doc struct {
		ID    string `db:"id" sqlb:"pk"`
		OrgID string `db:"org_id"`
	}
	hooks := sqlb.On[Doc]()
	defer hooks.Reset()
	hooks.BeforeQuery(func(ctx context.Context, q *sqlb.Builder[Doc]) error {
		q.Where(sqlb.F("org_id").Eq("acme"))
		return nil
	})

	h := newHarness(t, []string{"id", "org_id"}, nil)
	defer h.close()

	q := sqlb.Query[Doc]()
	if _, err := q.All(context.Background(), h.db); err != nil {
		t.Fatalf("first All: %v", err)
	}
	first := h.lastQuery()
	if _, err := q.All(context.Background(), h.db); err != nil {
		t.Fatalf("second All: %v", err)
	}
	if second := h.lastQuery(); second != first {
		t.Errorf("running twice changed the SQL\nfirst:  %s\nsecond: %s", first, second)
	}
}

// TestHookErrorAborts confirms a failing hook stops the query rather than
// running it unscoped.
func TestHookErrorAborts(t *testing.T) {
	type Secret struct {
		ID string `db:"id" sqlb:"pk"`
	}
	sentinel := errors.New("no tenant in context")
	hooks := sqlb.On[Secret]()
	defer hooks.Reset()
	hooks.BeforeQuery(func(ctx context.Context, q *sqlb.Builder[Secret]) error { return sentinel })

	h := newHarness(t, []string{"id"}, nil)
	defer h.close()

	if _, err := sqlb.Query[Secret]().All(context.Background(), h.db); !errors.Is(err, sentinel) {
		t.Errorf("error = %v, want the hook's error", err)
	}
	if h.lastQuery() != "" {
		t.Errorf("no query should have been issued, got: %s", h.lastQuery())
	}
}

func TestCollectIntoAggregateShape(t *testing.T) {
	type OrgSize struct {
		OrgID string `db:"org_id"`
		N     int64  `db:"n"`
	}
	h := newHarness(t, []string{"org_id", "n"}, [][]driver.Value{
		{"acme", int64(12)}, {"globex", int64(4)},
	})
	defer h.close()

	rows, err := sqlb.Collect[OrgSize](context.Background(), h.db,
		sqlb.Query[User]().Select(sqlb.F("org_id"), sqlb.Count().As("n")).GroupBy(sqlb.F("org_id")))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(rows) != 2 || rows[0].OrgID != "acme" || rows[0].N != 12 {
		t.Fatalf("rows = %#v", rows)
	}
}

func TestScanIgnoresUnmappedColumns(t *testing.T) {
	h := newHarness(t,
		[]string{"id", "email", "name", "age", "org_id", "password_hash", "created_at", "row_number"},
		[][]driver.Value{{"u1", "ada@example.com", "Ada", int64(36), "acme", "", time.Time{}, int64(1)}})
	defer h.close()

	users, err := sqlb.Query[User]().All(context.Background(), h.db)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(users) != 1 || users[0].Name != "Ada" {
		t.Fatalf("rows = %#v", users)
	}
	if users[0].Age == nil || *users[0].Age != 36 {
		t.Errorf("nullable column did not scan: %#v", users[0].Age)
	}
}

func TestOneReportsAmbiguity(t *testing.T) {
	h := newHarness(t, []string{"id", "email", "name", "age", "org_id", "password_hash", "created_at"},
		[][]driver.Value{
			{"u1", "a@example.com", "A", nil, "acme", "", time.Time{}},
			{"u2", "b@example.com", "B", nil, "acme", "", time.Time{}},
		})
	defer h.close()

	_, err := sqlb.Query[User]().Where(sqlb.F("org_id").Eq("acme")).One(context.Background(), h.db)
	if err == nil || !contains(err.Error(), "more than one row") {
		t.Errorf("error = %v, want an ambiguity report", err)
	}
}

func TestOneReturnsNotFound(t *testing.T) {
	h := newHarness(t, []string{"id", "email", "name", "age", "org_id", "password_hash", "created_at"}, nil)
	defer h.close()

	_, err := sqlb.Query[User]().Where(sqlb.F("id").Eq("nope")).One(context.Background(), h.db)
	if !errors.Is(err, sqlb.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

func TestCloneIsIndependent(t *testing.T) {
	base := sqlb.Query[User]().Select(sqlb.F("id")).Where(sqlb.F("org_id").Eq("acme"))
	derived := base.Clone().Where(sqlb.F("age").Gte(18))

	baseSQL, _, _ := base.SQL()
	derivedSQL, _, _ := derived.SQL()
	if contains(baseSQL, "age") {
		t.Errorf("mutating the clone leaked into the base: %s", baseSQL)
	}
	if !contains(derivedSQL, "age") {
		t.Errorf("clone lost its added predicate: %s", derivedSQL)
	}
}

func TestRawPlaceholderRenumbering(t *testing.T) {
	sql, args, err := sqlb.Query[User]().Select(sqlb.F("id")).
		Where(sqlb.F("org_id").Eq("acme"), sqlb.RawPred("age % ? = ?", 2, 0)).SQL()
	if err != nil {
		t.Fatalf("SQL() error: %v", err)
	}
	want := `SELECT "id" FROM "users" WHERE ("org_id" = $1) AND (age % $2 = $3)`
	if sql != want {
		t.Errorf("SQL\n got: %s\nwant: %s", sql, want)
	}
	if len(args) != 3 {
		t.Errorf("args = %#v, want 3", args)
	}
}

func TestRawArgumentCountMismatch(t *testing.T) {
	_, _, err := sqlb.Query[User]().Where(sqlb.RawPred("age > ?")).SQL()
	if err == nil {
		t.Fatal("expected an error for a placeholder with no argument")
	}
}

func TestIdentifierQuotingNeutralisesInjection(t *testing.T) {
	// F takes an arbitrary string, so a caller could pass something hostile.
	// Quoting must contain it rather than letting it close the identifier.
	sql, _, err := sqlb.Query[User]().Select(sqlb.F(`id" FROM "users"; DROP TABLE users --`)).SQL()
	if err != nil {
		t.Fatalf("SQL() error: %v", err)
	}
	if contains(sql, "DROP TABLE users --;") {
		t.Fatalf("identifier escaped its quotes: %s", sql)
	}
	want := `SELECT "id"" FROM ""users""; DROP TABLE users --" FROM "users"`
	if sql != want {
		t.Errorf("SQL\n got: %s\nwant: %s", sql, want)
	}
}

func TestTypedColumnsCarryTheirType(t *testing.T) {
	age := sqlb.Typed[int32]("age")
	sql, args, err := sqlb.Query[User]().Select(sqlb.F("id")).Where(age.Gte(18)).SQL()
	if err != nil {
		t.Fatalf("SQL() error: %v", err)
	}
	if sql != `SELECT "id" FROM "users" WHERE "age" >= $1` {
		t.Errorf("SQL = %s", sql)
	}
	if _, ok := args[0].(int32); !ok {
		t.Errorf("arg type = %T, want int32", args[0])
	}
}

// --- test harness -----------------------------------------------------------

// harness is a database/sql driver that records statements and replays canned
// rows, so the builder, hooks and scanner can be tested end to end without a
// live Postgres.
type harness struct {
	t    *testing.T
	db   *sql.DB
	name string
	mu   sync.Mutex
	log  []string
	cols []string
	rows [][]driver.Value
	err  error
}

var harnessSeq struct {
	sync.Mutex
	n int
}

func newHarness(t *testing.T, cols []string, rows [][]driver.Value) *harness {
	t.Helper()
	harnessSeq.Lock()
	harnessSeq.n++
	name := fmt.Sprintf("sqlbtest%d", harnessSeq.n)
	harnessSeq.Unlock()

	h := &harness{t: t, name: name, cols: cols, rows: rows}
	sql.Register(name, &fakeDriver{h: h})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("opening the fake driver: %v", err)
	}
	h.db = db
	return h
}

func (h *harness) close() { _ = h.db.Close() }

// failWith makes the next statements fail, standing in for a database that
// rejects a query.
func (h *harness) failWith(msg string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.err = errors.New(msg)
}

func (h *harness) record(q string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.log = append(h.log, q)
}

func (h *harness) lastQuery() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.log) == 0 {
		return ""
	}
	return h.log[len(h.log)-1]
}

type fakeDriver struct{ h *harness }

func (d *fakeDriver) Open(string) (driver.Conn, error) { return &fakeConn{h: d.h}, nil }

type fakeConn struct{ h *harness }

func (c *fakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("fake driver: prepared statements are not used")
}
func (c *fakeConn) Close() error              { return nil }
func (c *fakeConn) Begin() (driver.Tx, error) { return nil, errors.New("fake driver: no transactions") }

func (c *fakeConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.h.record(query)
	c.h.mu.Lock()
	err := c.h.err
	c.h.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return &fakeRows{cols: c.h.cols, data: c.h.rows}, nil
}

func (c *fakeConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	c.h.record(query)
	return fakeResult{n: int64(len(c.h.rows))}, nil
}

type fakeRows struct {
	cols []string
	data [][]driver.Value
	i    int
}

func (r *fakeRows) Columns() []string { return r.cols }
func (r *fakeRows) Close() error      { return nil }

func (r *fakeRows) Next(dest []driver.Value) error {
	if r.i >= len(r.data) {
		return io.EOF
	}
	copy(dest, r.data[r.i])
	r.i++
	return nil
}

type fakeResult struct{ n int64 }

func (r fakeResult) LastInsertId() (int64, error) { return 0, nil }
func (r fakeResult) RowsAffected() (int64, error) { return r.n, nil }

// normalise makes bound arguments comparable across integer widths, which the
// builder does not narrow.
func normalise(args []any) []any {
	out := make([]any, len(args))
	for i, a := range args {
		switch v := a.(type) {
		case int:
			out[i] = int64(v)
		case int32:
			out[i] = int64(v)
		default:
			out[i] = a
		}
	}
	return out
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// A table name naming a Postgres schema must render as two identifiers.
// Quoting it as one makes Postgres look for a table literally called
// "billing.invoices", which fails a long way from its cause.
func TestQualifiedTableNames(t *testing.T) {
	type Invoice struct {
		ID     string `db:"id" sqlb:"pk"`
		Amount int64  `db:"amount"`
	}
	sqlb.Describe[Invoice]().Table("billing.invoices")

	sel, _, err := sqlb.Query[Invoice]().Select(sqlb.F("id")).
		Where(sqlb.F("amount").Gt(0)).SQL()
	if err != nil {
		t.Fatalf("SQL(): %v", err)
	}
	if want := `SELECT "id" FROM "billing"."invoices" WHERE "amount" > $1`; sel != want {
		t.Errorf("select\n got: %s\nwant: %s", sel, want)
	}

	del, _, err := sqlb.DeleteRows[Invoice]().Where(sqlb.F("id").Eq("i1")).SQL()
	if err != nil {
		t.Fatalf("SQL(): %v", err)
	}
	if want := `DELETE FROM "billing"."invoices" WHERE "id" = $1`; del != want {
		t.Errorf("delete\n got: %s\nwant: %s", del, want)
	}

	// An unqualified name must keep rendering as exactly one identifier.
	type Plain struct {
		ID string `db:"id" sqlb:"pk"`
	}
	plain, _, _ := sqlb.Query[Plain]().Select(sqlb.F("id")).SQL()
	if want := `SELECT "id" FROM "plains"`; plain != want {
		t.Errorf("unqualified\n got: %s\nwant: %s", plain, want)
	}
}

// dialect overriding is per statement, not global. There is deliberately no
// package-level setter: a mutable global read on every query's compile path
// would be a data race with no legitimate trigger.
type ansiDialect struct{}

func (ansiDialect) Placeholder(int) string     { return "?" }
func (ansiDialect) Name() string               { return "ansi" }
func (ansiDialect) QuoteIdent(s string) string { return "`" + s + "`" }

func TestDialectIsOverriddenPerStatement(t *testing.T) {
	q := sqlb.Query[User]().Select(sqlb.F("id")).Where(sqlb.F("age").Gte(18))

	def, _, err := q.Clone().SQL()
	if err != nil {
		t.Fatalf("SQL(): %v", err)
	}
	if def != `SELECT "id" FROM "users" WHERE "age" >= $1` {
		t.Errorf("default dialect: %s", def)
	}

	alt, _, err := q.Clone().UseDialect(ansiDialect{}).SQL()
	if err != nil {
		t.Fatalf("SQL(): %v", err)
	}
	if alt != "SELECT `id` FROM `users` WHERE `age` >= ?" {
		t.Errorf("overridden dialect: %s", alt)
	}

	// The override must not leak into any other statement.
	after, _, _ := sqlb.Query[User]().Select(sqlb.F("id")).SQL()
	if after != `SELECT "id" FROM "users"` {
		t.Errorf("a per-statement override leaked globally: %s", after)
	}
}

// A field with no matching result column would scan as its zero value, which
// is indistinguishable from a real zero: a mistyped alias on a Sum silently
// reports 0 revenue. Collect must refuse rather than return a wrong number.
func TestCollectRejectsUnmatchedFields(t *testing.T) {
	type Revenue struct {
		Status string  `db:"status"`
		Total  float64 `db:"revenue"`
	}
	// The query aliases "revenu" — one character off.
	h := newHarness(t, []string{"status", "revenu"}, [][]driver.Value{{"published", 1234.5}})
	defer h.close()

	_, err := sqlb.Collect[Revenue](context.Background(), h.db,
		sqlb.Query[User]().Select(sqlb.F("status"), sqlb.Sum(sqlb.F("total")).As("revenu")).
			GroupBy(sqlb.F("status")))
	if err == nil {
		t.Fatal("a mistyped alias must not scan as a silent zero")
	}
	for _, want := range []string{"Total", "revenue", "revenu"} {
		if !contains(err.Error(), want) {
			t.Errorf("error should name the field and both column names, got: %v", err)
		}
	}
}

func TestCollectAcceptsAnExactMatch(t *testing.T) {
	type Revenue struct {
		Status string  `db:"status"`
		Total  float64 `db:"revenue"`
	}
	h := newHarness(t, []string{"status", "revenue"}, [][]driver.Value{{"published", 1234.5}})
	defer h.close()

	rows, err := sqlb.Collect[Revenue](context.Background(), h.db,
		sqlb.Query[User]().Select(sqlb.F("status"), sqlb.Sum(sqlb.F("total")).As("revenue")))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(rows) != 1 || rows[0].Total != 1234.5 {
		t.Fatalf("rows = %#v", rows)
	}
}

// All stays permissive: a projection legitimately leaves fields unfilled, which
// is what ?select=id,name is.
func TestAllToleratesPartialProjection(t *testing.T) {
	h := newHarness(t, []string{"id", "name"}, [][]driver.Value{{"u1", "Ada"}})
	defer h.close()

	users, err := sqlb.Query[User]().Select(sqlb.F("id"), sqlb.F("name")).All(context.Background(), h.db)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(users) != 1 || users[0].Name != "Ada" || users[0].Email != "" {
		t.Fatalf("rows = %#v", users)
	}
}

// Describing a model after a statement has been built against it would race
// against every in-flight query and half-apply. It must refuse.
func TestDescribeAfterUsePanics(t *testing.T) {
	type Late struct {
		ID   string `db:"id" sqlb:"pk"`
		Name string `db:"name"`
	}
	_ = sqlb.Query[Late]() // closes the model

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("describing a model already in use should panic")
		}
		msg, _ := r.(string)
		if !contains(msg, "initialisation") {
			t.Errorf("the panic should say when Describe is safe: %v", r)
		}
	}()
	sqlb.Describe[Late]().Filterable("name")
}
