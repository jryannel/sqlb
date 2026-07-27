package blog

import (
	"time"

	"github.com/jryannel/sqlb"
)

// Typed column sets, generated alongside the models.
//
// The query engine is reflective, so sqlb.F("titel") is a runtime error. These
// declarations put the column names and their Go types back under the compiler:
//
//	q.Where(blog.PostCols.Status.Eq(blog.PostStatusPublished))
//
// A misspelled column does not compile, and neither does comparing a status to
// an int. The cost is one small generated file per table — far less code than
// generating a whole builder API, because the builder stays generic and only
// the predicate construction is typed.
//
// Text columns are emitted as sqlb.TextCol, which is the only type carrying
// Contains, StartsWith and the other pattern operators — so Contains on an
// integer column does not compile rather than failing at the database.
//
// Nullable columns are typed as their base type: PublishedAt is *time.Time on
// the model but sqlb.Col[time.Time] here, so the comparand is a time.Time and
// NULL is expressed with IsNull rather than by comparing against a pointer.
//
// Capabilities are deliberately not enforced here. They gate the REST surface;
// Go code going through the engine directly is trusted, so every column offers
// every operator.

type orgColumns struct {
	ID        sqlb.Col[string]
	Name      sqlb.TextCol[string]
	Slug      sqlb.TextCol[string]
	CreatedAt sqlb.Col[time.Time]
	UpdatedAt sqlb.Col[time.Time]
}

// OrgCols are the typed columns of the orgs table.
var OrgCols = orgColumns{
	ID:        sqlb.Typed[string]("id"),
	Name:      sqlb.TextColumn[string]("name"),
	Slug:      sqlb.TextColumn[string]("slug"),
	CreatedAt: sqlb.Typed[time.Time]("created_at"),
	UpdatedAt: sqlb.Typed[time.Time]("updated_at"),
}

type authorColumns struct {
	ID        sqlb.Col[string]
	OrgID     sqlb.Col[string]
	Email     sqlb.TextCol[string]
	Name      sqlb.TextCol[string]
	CreatedAt sqlb.Col[time.Time]
	UpdatedAt sqlb.Col[time.Time]
}

// AuthorCols are the typed columns of the authors table.
//
// password_hash is absent: a Hidden column has no reason to be referenced from
// a query predicate, and leaving it out of the generated set means an
// accidental filter on it does not compile.
var AuthorCols = authorColumns{
	ID:        sqlb.Typed[string]("id"),
	OrgID:     sqlb.Typed[string]("org_id"),
	Email:     sqlb.TextColumn[string]("email"),
	Name:      sqlb.TextColumn[string]("name"),
	CreatedAt: sqlb.Typed[time.Time]("created_at"),
	UpdatedAt: sqlb.Typed[time.Time]("updated_at"),
}

type postColumns struct {
	ID          sqlb.Col[string]
	OrgID       sqlb.Col[string]
	AuthorID    sqlb.Col[string]
	Title       sqlb.TextCol[string]
	Body        sqlb.TextCol[string]
	Status      sqlb.Col[PostStatus]
	ViewCount   sqlb.Col[int64]
	PublishedAt sqlb.Col[time.Time]
	CreatedAt   sqlb.Col[time.Time]
	UpdatedAt   sqlb.Col[time.Time]
	DeletedAt   sqlb.Col[time.Time]
}

// PostCols are the typed columns of the posts table.
var PostCols = postColumns{
	ID:          sqlb.Typed[string]("id"),
	OrgID:       sqlb.Typed[string]("org_id"),
	AuthorID:    sqlb.Typed[string]("author_id"),
	Title:       sqlb.TextColumn[string]("title"),
	Body:        sqlb.TextColumn[string]("body"),
	Status:      sqlb.Typed[PostStatus]("status"),
	ViewCount:   sqlb.Typed[int64]("view_count"),
	PublishedAt: sqlb.Typed[time.Time]("published_at"),
	CreatedAt:   sqlb.Typed[time.Time]("created_at"),
	UpdatedAt:   sqlb.Typed[time.Time]("updated_at"),
	DeletedAt:   sqlb.Typed[time.Time]("deleted_at"),
}

// PostUpdate is a typed update statement for posts.
//
// The select builder is deliberately not wrapped: it has twenty-odd chainable
// methods whose return types would all have to be re-wrapped, and the generic
// column set already covers where the mistakes happen. An update statement is
// different — Update.Set takes a string and an any, so both the column name
// and the value type are unchecked. That hole is worth closing, and the
// statement has few enough methods to make wrapping cheap.
type PostUpdate struct {
	stmt *sqlb.Update[Post]
}

// UpdatePost starts a typed update.
func UpdatePost() *PostUpdate {
	return &PostUpdate{stmt: sqlb.UpdateRows[Post]()}
}

func (u *PostUpdate) SetTitle(v string) *PostUpdate {
	u.stmt.Set("title", v)
	return u
}

func (u *PostUpdate) SetBody(v string) *PostUpdate {
	u.stmt.Set("body", v)
	return u
}

func (u *PostUpdate) SetStatus(v PostStatus) *PostUpdate {
	u.stmt.Set("status", string(v))
	return u
}

// SetPublishedAt sets the column; pass nil to clear it.
func (u *PostUpdate) SetPublishedAt(v *time.Time) *PostUpdate {
	u.stmt.Set("published_at", v)
	return u
}

// AddViewCount increments the counter in the database rather than reading it
// first, so concurrent increments do not lose updates.
func (u *PostUpdate) AddViewCount(n int64) *PostUpdate {
	u.stmt.SetExpr("view_count", sqlb.Raw{SQL: "view_count + ?", Args: []any{n}})
	return u
}

// Where narrows the affected rows.
func (u *PostUpdate) Where(preds ...sqlb.Pred) *PostUpdate {
	u.stmt.Where(preds...)
	return u
}

// Stmt exposes the underlying statement for the operations the wrapper does
// not cover, such as Everything, Exec and One.
func (u *PostUpdate) Stmt() *sqlb.Update[Post] { return u.stmt }
