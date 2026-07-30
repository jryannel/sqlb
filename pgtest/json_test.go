package pgtest

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jryannel/sqlb"
	"github.com/jryannel/sqlb/filter"
	"github.com/jryannel/sqlb/schema"
)

// Containment against a real Postgres.
//
// The filter package's own tests compile `?metadata=hasdoc.{"lang":"de"}` to
// `"metadata" @> $1::jsonb` and compare it against a string somebody wrote.
// What that cannot check is what Postgres does with it:
//
//   - `@>` is subset containment, not equality. A row whose metadata carries
//     more keys than the filter named must still match, and that is the whole
//     reason a document column is filterable without declaring its keys.
//   - a jsonb column has to come back as a document, not as whatever the driver
//     decided a jsonb column was.
//   - the operator has to be one the declared GIN index can serve.
//
// The last is the one worth having, and the only one here that has been made to
// fail on purpose in the sense ADR-0016 asks for. Swapping `@>` for `=` leaves
// every result-based assertion in this file green — the rows are still right —
// and turns the plan into a Seq Scan. Results cannot tell a correct answer from
// a correct answer that scanned the table; only the plan can.
//
// The `::jsonb` cast is deliberately *not* claimed to be load bearing. It was
// removed to check, and the query still ran: pgx sends the parameter with an
// unspecified type and Postgres infers jsonb from the operator. The cast stays
// because it says what the statement means and costs nothing, not because
// anything here would catch its absence.

// JSONDoc is the model for a table with a document column.
type JSONDoc struct {
	ID       string          `db:"id" sqlb:"pk,default"`
	Title    string          `db:"title" sqlb:"sort"`
	Metadata json.RawMessage `db:"metadata" sqlb:"filter"`
}

func (JSONDoc) TableName() string { return "jsondocs" }

// jsonDocsRegistry declares the table JSONDoc maps to, with the GIN index that makes
// containment an index scan rather than a table scan.
func jsonDocsRegistry() *schema.Registry {
	r := schema.NewRegistry()
	r.Table("jsondocs",
		schema.UUIDv7("id").PrimaryKey(),
		schema.Text("title").Sortable(),
		schema.JSON("metadata").Filterable(),
	).AddIndex(schema.Index{Columns: []string{"metadata"}, Method: "gin"})
	return r
}

// seedJSONDocs inserts three rows whose metadata overlaps deliberately: one exact
// match for the filter below, one superset of it, and one that shares the key
// but not the value.
func seedJSONDocs(t *testing.T, db *pgxpool.Pool) {
	t.Helper()
	for _, row := range []struct{ title, metadata string }{
		{"exact", `{"lang":"de"}`},
		{"superset", `{"lang":"de","tier":"pro","tags":["urgent"]}`},
		{"same key, other value", `{"lang":"fr"}`},
	} {
		if _, err := db.Exec(context.Background(),
			`INSERT INTO jsondocs (title, metadata) VALUES ($1, $2::jsonb)`, row.title, row.metadata,
		); err != nil {
			t.Fatalf("inserting %q: %v", row.title, err)
		}
	}
}

// jsonDocsQuery parses a query string against the JSONDoc model and applies it.
func jsonDocsQuery(t *testing.T, query string) *sqlb.Builder[JSONDoc] {
	t.Helper()
	values, err := url.ParseQuery(query)
	if err != nil {
		t.Fatalf("bad test query %q: %v", query, err)
	}
	q, err := filter.Parse(values, filter.Options{Model: sqlb.ModelOf[JSONDoc]()})
	if err != nil {
		t.Fatalf("Parse(%q): %v", query, err)
	}
	return filter.Apply(sqlb.Query[JSONDoc]().OrderBy(sqlb.F("title").Asc()), q)
}

func TestJSONContainmentRunsAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	raw := freshDB(t)
	applySchema(t, raw, jsonDocsRegistry())
	seedJSONDocs(t, raw)

	docs, err := jsonDocsQuery(t, `metadata=hasdoc.{"lang":"de"}`).All(ctx, sqlb.New(raw))
	if err != nil {
		t.Fatalf("containment filter did not run: %v", err)
	}

	got := make([]string, len(docs))
	for i, d := range docs {
		got[i] = d.Title
	}
	want := []string{"exact", "superset"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("containment matched %v, want %v — a superset must match, and a different value must not", got, want)
	}
}

// A document column has to survive the round trip as a document: the filter is
// only useful if what comes back can be read as JSON rather than as whatever
// the driver decided a jsonb column was.
func TestJSONColumnScansAsADocument(t *testing.T) {
	ctx := context.Background()
	raw := freshDB(t)
	applySchema(t, raw, jsonDocsRegistry())
	seedJSONDocs(t, raw)

	docs, err := jsonDocsQuery(t, `metadata=hasdoc.{"tier":"pro"}`).All(ctx, sqlb.New(raw))
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("got %d rows, want 1", len(docs))
	}

	var decoded map[string]any
	if err := json.Unmarshal(docs[0].Metadata, &decoded); err != nil {
		t.Fatalf("the metadata column did not scan as JSON: %v (raw: %q)", err, docs[0].Metadata)
	}
	if decoded["tier"] != "pro" {
		t.Errorf("decoded metadata = %v, want tier=pro", decoded)
	}
}

// The claim this test exists for: `@>` is servable by the GIN index the schema
// declared. An operator outside the index's operator class answers the query
// correctly by scanning the table, so results cannot distinguish the two — only
// the plan can.
//
// enable_seqscan is turned off because three rows are far too few for the
// planner to prefer an index on cost. That makes this a test of whether the
// index *can* serve the operator, which is the part that would break, rather
// than of what the planner picks at a size this test does not have.
func TestJSONContainmentCanUseTheGINIndex(t *testing.T) {
	ctx := context.Background()
	raw := freshDB(t)
	applySchema(t, raw, jsonDocsRegistry())
	seedJSONDocs(t, raw)

	sqlText, args, err := jsonDocsQuery(t, `metadata=hasdoc.{"lang":"de"}`).SQL()
	if err != nil {
		t.Fatalf("SQL(): %v", err)
	}

	if _, err := raw.Exec(ctx, "SET enable_seqscan = off"); err != nil {
		t.Fatalf("disabling seqscan: %v", err)
	}

	rows, err := raw.Query(ctx, "EXPLAIN "+sqlText, args...)
	if err != nil {
		t.Fatalf("EXPLAIN: %v", err)
	}
	defer rows.Close()

	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scanning the plan: %v", err)
		}
		plan.WriteString(line + "\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading the plan: %v", err)
	}

	if !strings.Contains(plan.String(), "docs_metadata_idx") {
		t.Errorf("containment did not reach the GIN index, so it is a table scan wearing the right answer:\n%s", plan.String())
	}
}
