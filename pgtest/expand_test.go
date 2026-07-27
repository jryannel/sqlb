package pgtest

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/jryannel/sqlb"
	"github.com/jryannel/sqlb/example/blog"
	"github.com/jryannel/sqlb/schema"

	// Imported for its side effects: declaring a table registers it.
	_ "github.com/jryannel/sqlb/example/blog/blogschema"
)

// Expansion against a real Postgres.
//
// The engine's own tests compile ?expand to SQL and compare it against a string
// somebody wrote. That is worth having and it is not the same question: a
// json_build_object with the wrong argument shape, or a CASE that Postgres reads
// differently than intended, matches the golden string exactly and fails only
// when a database sees it. It already did once — the projection was unqualified,
// and `column reference "id" is ambiguous` is not a wrong answer, it is not a
// query at all.
//
// The security-relevant one is TestHiddenColumnsDoNotSurviveTheJoin, and it is
// the reason this file reads the raw JSON rather than the decoded struct. See
// the comment there.

// seedBlog inserts one org, one author with a password hash, and one post by
// that author, returning the author's id.
func seedBlog(t *testing.T, db *sql.DB) (orgID, authorID string) {
	t.Helper()

	if err := db.QueryRow(
		`INSERT INTO orgs (name, slug) VALUES ('Acme', 'acme') RETURNING id`,
	).Scan(&orgID); err != nil {
		t.Fatalf("inserting an org: %v", err)
	}
	if err := db.QueryRow(
		`INSERT INTO authors (org_id, email, name, password_hash)
		 VALUES ($1, 'ada@example.com', 'Ada', 'argon2id$v=19$correct-horse')
		 RETURNING id`, orgID,
	).Scan(&authorID); err != nil {
		t.Fatalf("inserting an author: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO posts (org_id, author_id, title, body)
		 VALUES ($1, $2, 'Hello', 'the body')`, orgID, authorID,
	); err != nil {
		t.Fatalf("inserting a post: %v", err)
	}
	return orgID, authorID
}

func TestExpandRunsAndScansAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	raw := freshDB(t)
	applySchema(t, raw, schema.DefaultRegistry())
	db := sqlb.New(raw)

	_, authorID := seedBlog(t, raw)

	posts, err := sqlb.Query[blog.Post]().Expand("author").All(ctx, db)
	if err != nil {
		t.Fatalf("expanding author: %v", err)
	}
	if len(posts) != 1 {
		t.Fatalf("got %d posts, want 1", len(posts))
	}

	got := posts[0]
	if got.Author == nil {
		t.Fatal("the relation was not filled in")
	}
	if got.Author.ID != authorID || got.Author.Email != "ada@example.com" {
		t.Errorf("expanded the wrong author: %+v", got.Author)
	}
	// The key is untouched: expansion adds the row, it does not replace the
	// reference.
	if got.AuthorID != authorID {
		t.Errorf("author_id = %q, want %q", got.AuthorID, authorID)
	}
}

// TestHiddenColumnsDoNotSurviveTheJoin is the one failure here that would be a
// security bug rather than a broken feature: if Hidden stopped holding across a
// join, ?expand would become a way to read a column the target refuses to serve
// directly — blog's password_hash, in the shipped example.
//
// It reads the raw JSON Postgres produced rather than the decoded struct, and
// the distinction is the whole test. blog.Author tags PasswordHash `json:"-"`,
// so json.Unmarshal drops the key whether or not it arrived; asserting on
// got.Author.PasswordHash == "" would pass with the hash sitting in the
// database's answer. That is a test passing for the wrong reason, and this file
// exists to stop exactly that kind of thing.
func TestHiddenColumnsDoNotSurviveTheJoin(t *testing.T) {
	ctx := context.Background()
	raw := freshDB(t)
	applySchema(t, raw, schema.DefaultRegistry())

	seedBlog(t, raw)

	text, args, err := sqlb.Query[blog.Post]().Expand("author").SQL()
	if err != nil {
		t.Fatalf("SQL: %v", err)
	}
	payload := expansionJSON(t, ctx, raw, text, args, "__expand_author")

	// Present, so the assertion below is about the hash and not about an empty
	// result.
	if !strings.Contains(payload, "ada@example.com") {
		t.Fatalf("the expansion did not carry the author at all: %s", payload)
	}
	if strings.Contains(payload, "password_hash") || strings.Contains(payload, "correct-horse") {
		t.Errorf("a hidden column of the expanded target reached the response: %s", payload)
	}
}

// A LEFT JOIN that matches nothing has to produce NULL, not an object of nulls.
// "there is no related row" and "there is one and every field is empty" are
// different answers and a client can act on the difference — so the CASE guard
// is asserted against the database that evaluates it, not against the string it
// compiles to.
//
// The row is orphaned behind the foreign key's back, because a schema that let
// this happen through its own DDL would be a different bug. What is under test
// is what the query does when it happens.
func TestAMissingTargetExpandsToNullNotAnEmptyObject(t *testing.T) {
	ctx := context.Background()
	raw := freshDB(t)
	applySchema(t, raw, schema.DefaultRegistry())
	db := sqlb.New(raw)

	_, authorID := seedBlog(t, raw)

	mustExec(t, raw, `ALTER TABLE posts DROP CONSTRAINT posts_author_id_fkey`)
	if _, err := raw.Exec(`DELETE FROM authors WHERE id = $1`, authorID); err != nil {
		t.Fatalf("orphaning the post: %v", err)
	}

	text, args, err := sqlb.Query[blog.Post]().Expand("author").SQL()
	if err != nil {
		t.Fatalf("SQL: %v", err)
	}
	if payload := expansionJSON(t, ctx, raw, text, args, "__expand_author"); payload != "" {
		t.Errorf("a reference to a row that is gone expanded to %s, want NULL", payload)
	}

	// And the scanner turns that into a nil field rather than a zero struct,
	// which is the same distinction one layer up.
	posts, err := sqlb.Query[blog.Post]().Expand("author").All(ctx, db)
	if err != nil {
		t.Fatalf("expanding a missing author: %v", err)
	}
	if len(posts) != 1 {
		t.Fatalf("got %d posts, want 1", len(posts))
	}
	if posts[0].Author != nil {
		t.Errorf("a missing target scanned as %+v, want nil", posts[0].Author)
	}
}

// Every other list parameter still has to work once a second table is in the
// statement. posts and authors share id, org_id, created_at and updated_at, so
// an unqualified reference to any of them is ambiguous — which Postgres refuses
// outright rather than resolving.
func TestExpandComposesWithTheOtherQueryParameters(t *testing.T) {
	ctx := context.Background()
	raw := freshDB(t)
	applySchema(t, raw, schema.DefaultRegistry())
	db := sqlb.New(raw)

	orgID, _ := seedBlog(t, raw)

	for name, q := range map[string]*sqlb.Builder[blog.Post]{
		"filter on a shared column name": sqlb.Query[blog.Post]().
			Expand("author").Where(sqlb.F("org_id").Eq(orgID)),
		"sort on a shared column name": sqlb.Query[blog.Post]().
			Expand("author").OrderBy(sqlb.F("created_at").Desc()),
		"an explicit projection over shared names": sqlb.Query[blog.Post]().
			Expand("author").Select(sqlb.F("id"), sqlb.F("org_id")),
	} {
		rows, err := q.All(ctx, db)
		if err != nil {
			text, _, _ := q.SQL()
			t.Errorf("%s: %v\n%s", name, err, text)
			continue
		}
		if len(rows) != 1 {
			t.Errorf("%s: got %d posts, want 1", name, len(rows))
		}
	}

	// Counting drops the join — it cannot change how many rows match — but the
	// statement still has to be valid.
	total, err := sqlb.Query[blog.Post]().Expand("author").Count(ctx, db)
	if err != nil {
		t.Fatalf("counting an expanded query: %v", err)
	}
	if total != 1 {
		t.Errorf("count = %d, want 1", total)
	}
}

// expansionJSON runs a compiled statement and returns the named expansion
// column of its first row, as the text Postgres produced. An empty string means
// the column was NULL.
func expansionJSON(t *testing.T, ctx context.Context, db *sql.DB, text string, args []any, column string) string {
	t.Helper()

	rows, err := db.QueryContext(ctx, text, args...)
	if err != nil {
		t.Fatalf("executing:\n%s\n%v", text, err)
	}
	defer rows.Close()

	names, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	at := -1
	for i, n := range names {
		if n == column {
			at = i
		}
	}
	if at < 0 {
		t.Fatalf("no %q in the result columns %v", column, names)
	}

	if !rows.Next() {
		t.Fatalf("the statement returned no rows:\n%s", text)
	}
	cells := make([]any, len(names))
	for i := range cells {
		cells[i] = new(sql.RawBytes)
	}
	if err := rows.Scan(cells...); err != nil {
		t.Fatalf("scanning: %v", err)
	}
	out := string(*cells[at].(*sql.RawBytes))
	if err := rows.Err(); err != nil {
		t.Fatalf("reading rows: %v", err)
	}
	return out
}
