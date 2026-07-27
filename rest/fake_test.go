package rest_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// The models under test mirror what codegen emits: db tags for column names,
// sqlb tags for capabilities, json tags for the wire, and `json:"-"` on the
// hidden column so it cannot be marshalled by accident.
type Post struct {
	ID    string `db:"id" json:"id" sqlb:"pk,default,filter,readonly"`
	OrgID string `db:"org_id" json:"org_id" sqlb:"filter,immutable"`
	Title string `db:"title" json:"title" sqlb:"filter,sort,search"`
	Body  string `db:"body" json:"body" sqlb:"search"`
	// Excerpt declares nothing, so it is readable but not filterable, sortable
	// or searchable — the default an opt-in capability model gives a column.
	Excerpt   string    `db:"excerpt" json:"excerpt"`
	Status    string    `db:"status" json:"status" sqlb:"default,filter,sort"`
	ViewCount int64     `db:"view_count" json:"view_count" sqlb:"default,filter,sort,readonly"`
	Secret    string    `db:"secret" json:"-" sqlb:"hidden"`
	CreatedAt time.Time `db:"created_at" json:"created_at" sqlb:"default,sort,readonly"`
}

func (Post) TableName() string { return "posts" }

// PostCreate is the create body: writable columns only, with the defaulted ones
// optional so the database supplies them when the request stays quiet.
type PostCreate struct {
	OrgID  string  `json:"org_id"`
	Title  string  `json:"title"`
	Body   string  `json:"body"`
	Status *string `json:"status,omitempty"`
}

func (c PostCreate) Row() (*Post, error) {
	p := &Post{OrgID: c.OrgID, Title: c.Title, Body: c.Body}
	if c.Status != nil {
		p.Status = *c.Status
	}
	return p, nil
}

// PostUpdate is the patch body: every field a pointer, so absent and zero are
// distinguishable.
type PostUpdate struct {
	Title  *string `json:"title,omitempty"`
	Body   *string `json:"body,omitempty"`
	Status *string `json:"status,omitempty"`
	OrgID  *string `json:"org_id,omitempty"`
}

func (u PostUpdate) Changes() (map[string]any, error) {
	out := map[string]any{}
	if u.Title != nil {
		out["title"] = *u.Title
	}
	if u.Body != nil {
		out["body"] = *u.Body
	}
	if u.Status != nil {
		out["status"] = *u.Status
	}
	if u.OrgID != nil {
		out["org_id"] = *u.OrgID
	}
	return out, nil
}

// Keyless has no primary key, so it can only be listed.
type Keyless struct {
	Name string `db:"name" json:"name" sqlb:"filter"`
}

func (Keyless) TableName() string { return "keyless" }

// Leaky is a mistake the binder should catch: a hidden column that would still
// be serialised if anything marshalled the struct.
type Leaky struct {
	ID     string `db:"id" json:"id" sqlb:"pk"`
	Secret string `db:"secret" json:"secret" sqlb:"hidden"`
}

func (Leaky) TableName() string { return "leaky" }

// reply is one canned result, matched against the statement text.
type reply struct {
	match string
	cols  []string
	rows  [][]driver.Value
	err   error
}

// fakeDB is a database that answers from a script. Each reply matches the first
// statement containing its substring, so a test can distinguish the page query
// from the count query without a real database.
type fakeDB struct {
	t  *testing.T
	db *sql.DB

	mu      sync.Mutex
	replies []reply
	log     []string
	// args are the bind parameters of each statement, so a test can assert on
	// the values a request produced and not only on the SQL around them.
	args [][]any
}

var fakeSeq struct {
	sync.Mutex
	n int
}

func newFakeDB(t *testing.T, replies ...reply) *fakeDB {
	t.Helper()
	fakeSeq.Lock()
	fakeSeq.n++
	name := fmt.Sprintf("resttest%d", fakeSeq.n)
	fakeSeq.Unlock()

	f := &fakeDB{t: t, replies: replies}
	sql.Register(name, &fakeDriver{f: f})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatalf("opening the fake driver: %v", err)
	}
	f.db = db
	t.Cleanup(func() { _ = db.Close() })
	return f
}

// answer picks the reply for a statement, recording the statement either way.
func (f *fakeDB) answer(query string, args []driver.NamedValue) (reply, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, query)
	values := make([]any, 0, len(args))
	for _, a := range args {
		values = append(values, a.Value)
	}
	f.args = append(f.args, values)
	for _, r := range f.replies {
		if r.match == "" || strings.Contains(query, r.match) {
			return r, true
		}
	}
	return reply{}, false
}

// statements returns every statement the handler issued, in order.
func (f *fakeDB) statements() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.log...)
}

// lastStatement is the most recent statement, for asserting on compiled SQL.
func (f *fakeDB) lastStatement() string {
	stmts := f.statements()
	if len(stmts) == 0 {
		return ""
	}
	return stmts[len(stmts)-1]
}

// lastArgs is the bind parameters of the most recent statement.
func (f *fakeDB) lastArgs() []any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.args) == 0 {
		return nil
	}
	return append([]any(nil), f.args[len(f.args)-1]...)
}

type fakeDriver struct{ f *fakeDB }

func (d *fakeDriver) Open(string) (driver.Conn, error) { return &fakeConn{f: d.f}, nil }

type fakeConn struct{ f *fakeDB }

func (c *fakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("fake driver: prepared statements are not used")
}
func (c *fakeConn) Close() error              { return nil }
func (c *fakeConn) Begin() (driver.Tx, error) { return nil, errors.New("fake driver: no transactions") }

func (c *fakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	r, ok := c.f.answer(query, args)
	if !ok {
		return &fakeRows{}, nil
	}
	if r.err != nil {
		return nil, r.err
	}
	return &fakeRows{cols: r.cols, data: r.rows}, nil
}

func (c *fakeConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	r, ok := c.f.answer(query, args)
	if !ok {
		return fakeResult{}, nil
	}
	if r.err != nil {
		return nil, r.err
	}
	return fakeResult{n: int64(len(r.rows))}, nil
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

// postRow is the column set and one row of it, as the page query returns them.
func postCols() []string {
	return []string{"id", "org_id", "title", "body", "excerpt", "status", "view_count", "created_at"}
}

func postRow(id, title string) []driver.Value {
	return []driver.Value{id, "acme", title, "body text", "excerpt text", "draft", int64(3), time.Unix(0, 0).UTC()}
}

// Tenanted is the shape the ReadOnly-plus-hook pattern needs, and the one the
// Post model above cannot express: a read-only column with no database default,
// whose value a BeforeCreate hook supplies. A tenant id and an author id are
// both this.
type Tenanted struct {
	ID string `db:"id" json:"id" sqlb:"pk,default,filter,readonly"`
	// No `default`: nothing in the database will fill this in, so if the hook's
	// value does not reach the INSERT the row is written with a NULL.
	TenantID string `db:"tenant_id" json:"tenant_id" sqlb:"filter,readonly"`
	Title    string `db:"title" json:"title" sqlb:"filter,sort"`
}

func (Tenanted) TableName() string { return "tenanted" }

// TenantedCreate is what codegen would emit: the read-only columns are absent.
type TenantedCreate struct {
	Title string `json:"title"`
}

func (c TenantedCreate) Row() (*Tenanted, error) { return &Tenanted{Title: c.Title}, nil }

// SmugglingCreate is the hand-written body the clearing defends against: it
// sets a read-only column the schema says a request may not write.
type SmugglingCreate struct {
	Title    string `json:"title"`
	TenantID string `json:"tenant_id"`
}

func (c SmugglingCreate) Row() (*Tenanted, error) {
	return &Tenanted{Title: c.Title, TenantID: c.TenantID}, nil
}

func tenantedCols() []string { return []string{"id", "tenant_id", "title"} }

func tenantedRow(id, tenant, title string) []driver.Value {
	return []driver.Value{id, tenant, title}
}
