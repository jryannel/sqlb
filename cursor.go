package sqlb

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// Keyset pagination: page by where you got to, not by how far in you are.
//
// `LIMIT n OFFSET k` asks the database to produce k+n rows and discard k of
// them, so page 500 costs five hundred times page 1, and a row inserted while a
// client is paging shifts every later page by one — the client sees a row twice
// or never. Both problems come from addressing a page by its *distance* from
// the start.
//
// A cursor addresses it by *position* instead. The last row of a page carries
// the values its ordering sorted on; the next page asks for the rows after
// those values. The database seeks to that point in an index and reads forward,
// so every page costs the same, and a concurrent insert lands either before the
// cursor (already read, not re-read) or after it (read later, once).
//
// # What a total order buys
//
// The seek only works if the ordering is a total order — if two rows can tie on
// every ordering term, there is no "after those values" that includes one and
// excludes the other, and the page boundary is ambiguous in exactly the way
// offset paging is. Stable appends the primary key to guarantee it, and After
// requires it.
//
// # Forward only
//
// There is no Before. Paging backwards means reversing the ordering, seeking in
// the other direction and reversing the rows back, which is a second wire
// parameter, a second cursor in every response and a `has_more` whose meaning
// depends on which end you came from. A client that needs a back button keeps
// the cursors it has already been given, which costs it a slice. If that turns
// out to be wrong, `?before=` is additive — see ADR-0027.

// Cursor is an opaque position in an ordered result set.
//
// It is opaque by intent rather than by encryption: it decodes to the ordering
// columns and the values of the row it was taken from, and a client that
// decodes it learns nothing it could not read off the response. Tampering is
// equally uninteresting — After checks the columns and directions against the
// ordering the request actually asked for, so an edited cursor can only move
// the boundary along a column the caller was already permitted to sort by.
type Cursor string

// IsZero reports whether the cursor is empty, which means "start at the
// beginning". After ignores a zero cursor, so a first request and a subsequent
// one can run the same code.
func (c Cursor) IsZero() bool { return c == "" }

// ErrBadCursor is the class of every cursor a request cannot be answered with:
// malformed, or issued for a different ordering. It is a sentinel so the REST
// layer can map the whole class to 400 without inspecting text; the wrapped
// error says which case it was and what to do.
var ErrBadCursor = errors.New("sqlb: invalid cursor")

// cursorTerm is one ordering term and the boundary row's value for it.
//
// Value is held as raw JSON rather than as `any` so that decoding can target
// the column's own Go type: a timestamp arrives as the RFC 3339 string the API
// emitted for it and is decoded into time.Time, not into a string that Postgres
// would then have to cast. The consequence worth knowing is that a cursor's
// value for a column is exactly the JSON the response showed for it.
type cursorTerm struct {
	Column string          `json:"c"`
	Desc   bool            `json:"d,omitempty"`
	Value  json.RawMessage `json:"v"`
}

type cursorPayload struct {
	Terms []cursorTerm `json:"k"`
}

// Stable makes the ordering deterministic by appending the primary key, which
// is what lets a page boundary be named at all.
//
// Without it `ORDER BY status` leaves rows with equal status in whatever order
// the plan produced, so page 2 may repeat a row from page 1 or skip one, and no
// cursor can distinguish the two. This is the same defect schema.Lint reports
// as list-without-sort; Stable is the fix rather than the warning.
//
// It is idempotent, and a no-op when the ordering already contains the primary
// key — including when the caller sorted by it explicitly. The appended term
// takes the direction of the last existing term, so `?sort=-created_at` reads
// as "newest first, and newest id first among equal timestamps" rather than
// changing direction halfway through the ORDER BY.
//
// A model with no primary key is left alone rather than failed: such a model
// can still be listed and paged by offset, and only the cursor calls — After
// and CursorFor — genuinely cannot work without a key, so they are where the
// error belongs.
func (b *Builder[T]) Stable() *Builder[T] {
	key := b.model.PK
	if key == nil {
		return b
	}
	for _, o := range b.orders {
		if col, ok := o.expr.(Column); ok && col.Name == key.Name {
			return b
		}
	}
	f := F(key.Name)
	if len(b.orders) > 0 && b.orders[len(b.orders)-1].desc {
		b.orders = append(b.orders, f.Desc())
	} else {
		b.orders = append(b.orders, f.Asc())
	}
	return b
}

// After restricts the query to the rows following the cursor's position in the
// query's own ordering. A zero cursor is a no-op, so the first page and every
// page after it are the same call.
//
// It calls Stable first, because the cursor it was handed was issued against a
// total order and has to be interpreted against the same one.
//
// The predicate is kept apart from Where rather than folded into it, so that
// Count still answers "how many rows match" rather than "how many are left" —
// a total that changed as a client paged would be a worse answer than no total.
func (b *Builder[T]) After(c Cursor) *Builder[T] {
	// Stable runs even for a zero cursor, and that is the whole point of doing
	// it here rather than after the early return: the *first* page is the one
	// whose ordering has to be total, because it is the page whose last row
	// becomes the cursor. Normalising only when a cursor arrives would issue a
	// cursor against an ordering the query did not use.
	b.Stable()
	if c.IsZero() {
		return b
	}
	terms, err := b.keysetTerms()
	if err != nil {
		return b.Fail(err)
	}
	values, err := decodeCursor(c, terms)
	if err != nil {
		return b.Fail(err)
	}
	b.seek = seekPredicate(terms, values)
	return b
}

// CursorFor returns the cursor naming row's position in this query's ordering,
// for a caller handing a client the start of the next page.
//
// The row must have been produced by this query, or by one ordered identically:
// the cursor is built by reading the ordering columns off it, so a row from a
// different projection that left one of them zero would encode a position that
// was never reached.
func (b *Builder[T]) CursorFor(row T) (Cursor, error) {
	b.Stable()
	terms, err := b.keysetTerms()
	if err != nil {
		return "", err
	}
	// Addressable, because a column reached through a nil embedded pointer —
	// a mixin the row never populated — has to be allocated before it can be
	// read, and fieldByIndex can only do that through a settable value.
	rv := reflect.ValueOf(&row).Elem()
	payload := cursorPayload{Terms: make([]cursorTerm, len(terms))}
	for i, t := range terms {
		fv, err := fieldByIndex(rv, t.col.Index)
		if err != nil {
			return "", fmt.Errorf("sqlb: cannot read %s for a cursor: %w", t.col.Name, err)
		}
		raw, err := json.Marshal(fv.Interface())
		if err != nil {
			return "", fmt.Errorf("sqlb: cannot encode %s into a cursor: %w", t.col.Name, err)
		}
		payload.Terms[i] = cursorTerm{Column: t.col.Name, Desc: t.desc, Value: raw}
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("sqlb: cannot encode cursor: %w", err)
	}
	return Cursor(base64.RawURLEncoding.EncodeToString(buf)), nil
}

// keysetTerm is one ordering term resolved back to the model column it names.
type keysetTerm struct {
	col  *ColumnInfo
	expr Expr
	desc bool
	// nullsAfter reports whether NULLs sort after non-NULLs under this term.
	// Postgres defaults to NULLS LAST for ASC and NULLS FIRST for DESC, so the
	// default is not a single placement — it follows the direction.
	nullsAfter bool
}

// keysetTerms resolves the ORDER BY into the terms a cursor can be built from,
// or explains why it cannot.
//
// Every failure here is a programming error rather than a bad request: an
// ordering a cursor cannot describe is one the caller assembled, so the message
// names the term and what would work instead.
func (b *Builder[T]) keysetTerms() ([]keysetTerm, error) {
	if len(b.orders) == 0 {
		return nil, fmt.Errorf("sqlb: cursor pagination needs an ORDER BY; %s has none", b.model.Table)
	}
	if b.model.PK == nil {
		return nil, fmt.Errorf(
			"sqlb: cursor pagination needs a primary key to break ties, and %s declares none; "+
				"tag one column `sqlb:\"pk\"`, or page with Limit and Offset",
			b.model.Type.Name())
	}
	out := make([]keysetTerm, 0, len(b.orders))
	for _, o := range b.orders {
		col, ok := o.expr.(Column)
		if !ok {
			return nil, fmt.Errorf(
				"sqlb: cursor pagination orders by columns only, and this query orders by an expression; " +
					"order by a column, or page with Limit and Offset")
		}
		info := b.model.Column(col.Name)
		if info == nil {
			return nil, fmt.Errorf("sqlb: cannot build a cursor over %q: %s has no such column%s",
				col.Name, b.model.Type.Name(), didYouMean(b.model.ColumnNames()))
		}
		out = append(out, keysetTerm{
			col:        info,
			expr:       o.expr,
			desc:       o.desc,
			nullsAfter: o.nullsAfterValues(),
		})
	}
	return out, nil
}

// nullsAfterValues reports where NULLs sit relative to real values under this
// term, resolving the default against the direction the way Postgres does.
func (o Order) nullsAfterValues() bool {
	switch o.nulls {
	case nullsFirst:
		return false
	case nullsLast:
		return true
	default:
		return !o.desc
	}
}

// decodeCursor reads a cursor and checks it against the ordering it is about to
// be interpreted under.
//
// The check is what makes an opaque-by-convention cursor safe to accept from a
// client. A cursor naming different columns is not a tampering attempt to be
// scolded for — it is what a client gets by changing ?sort= and keeping the
// cursor it already had — so the error says which ordering it was issued for
// and which one is being asked for now.
func decodeCursor(c Cursor, terms []keysetTerm) ([]any, error) {
	buf, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(string(c), "="))
	if err != nil {
		return nil, fmt.Errorf("%w: not decodable; drop the cursor to start from the beginning", ErrBadCursor)
	}
	var payload cursorPayload
	if err := json.Unmarshal(buf, &payload); err != nil {
		return nil, fmt.Errorf("%w: not decodable; drop the cursor to start from the beginning", ErrBadCursor)
	}
	if len(payload.Terms) != len(terms) {
		return nil, orderingMismatch(payload, terms)
	}
	values := make([]any, len(terms))
	for i, t := range terms {
		got := payload.Terms[i]
		if got.Column != t.col.Name || got.Desc != t.desc {
			return nil, orderingMismatch(payload, terms)
		}
		v, err := decodeValue(got.Value, t.col)
		if err != nil {
			return nil, err
		}
		values[i] = v
	}
	return values, nil
}

// decodeValue turns a cursor's JSON value into the Go value the driver should
// bind for that column.
//
// Decoding into the column's own type rather than into `any` is what keeps a
// timestamp a timestamp: bound as a string it would compare against a
// timestamptz only if Postgres could infer the cast, and it would compare
// textually if it could not.
func decodeValue(raw json.RawMessage, col *ColumnInfo) (any, error) {
	if string(raw) == "null" {
		if !col.Nullable {
			return nil, fmt.Errorf("%w: null position for %s, which is not nullable", ErrBadCursor, col.Name)
		}
		return nil, nil
	}
	t := col.Type
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	ptr := reflect.New(t)
	if err := json.Unmarshal(raw, ptr.Interface()); err != nil {
		return nil, fmt.Errorf("%w: position for %s is not a %s", ErrBadCursor, col.Name, t)
	}
	return ptr.Elem().Interface(), nil
}

func orderingMismatch(payload cursorPayload, terms []keysetTerm) error {
	return fmt.Errorf("%w: it was issued for an ordering of %s, and this request orders by %s; "+
		"drop the cursor when the sort changes",
		ErrBadCursor, describePayload(payload), describeTerms(terms))
}

func describePayload(p cursorPayload) string {
	if len(p.Terms) == 0 {
		return "nothing"
	}
	parts := make([]string, len(p.Terms))
	for i, t := range p.Terms {
		parts[i] = t.Column + " " + direction(t.Desc)
	}
	return strings.Join(parts, ", ")
}

func describeTerms(terms []keysetTerm) string {
	parts := make([]string, len(terms))
	for i, t := range terms {
		parts[i] = t.col.Name + " " + direction(t.desc)
	}
	return strings.Join(parts, ", ")
}

func direction(desc bool) string {
	if desc {
		return "desc"
	}
	return "asc"
}

// seekPredicate builds "the rows strictly after this position".
//
// The general form is the lexicographic expansion: a row is after the boundary
// if it is greater on the first term, or equal there and greater on the second,
// and so on.
//
//	(a > $1) OR (a = $1 AND b < $2) OR (a = $1 AND b = $2 AND id > $3)
//
// It is quadratic in the number of terms, which is why the number of sort terms
// is capped upstream and why four is already an unusual ordering.
func seekPredicate(terms []keysetTerm, values []any) Pred {
	if p, ok := rowValueSeek(terms, values); ok {
		return p
	}
	var disjuncts []Pred
	for i, t := range terms {
		after := afterTerm(t, values[i])
		if after.IsZero() {
			// Nothing sorts after this term's boundary — a NULL under NULLS
			// LAST. The disjunct is unsatisfiable, so it is dropped rather
			// than emitted as a false the planner has to reason about.
			continue
		}
		conj := make([]Pred, 0, i+1)
		for j := 0; j < i; j++ {
			conj = append(conj, equalTerm(terms[j], values[j]))
		}
		conj = append(conj, after)
		disjuncts = append(disjuncts, And(conj...))
	}
	if len(disjuncts) == 0 {
		// Every term's boundary is the end of its ordering, so the page after
		// this one is empty. Saying so explicitly matters: the zero Pred is a
		// no-op that Where would skip, which would return the whole table.
		return pred(Raw{SQL: "false"})
	}
	return Or(disjuncts...)
}

// rowValueSeek emits the row constructor form when it is exactly equivalent.
//
//	(a, b, id) > ($1, $2, $3)
//
// Postgres turns this into a single index seek on a matching multi-column
// index, where the expanded OR form is more often a bitmap of several scans.
// That difference is the entire point of keyset pagination, so it is worth the
// second code path.
//
// It is only equivalent when nothing about NULLs can come up — a row comparison
// yields NULL, not false, as soon as either side holds one — and when every
// term runs the same direction, since a row comparison has one operator for the
// whole tuple. Both conditions are read off the model rather than assumed:
// a nullable ordering column disqualifies it even if this particular boundary
// value is non-null, because the *rows being compared* may still hold NULLs.
func rowValueSeek(terms []keysetTerm, values []any) (Pred, bool) {
	if len(terms) < 2 {
		return Pred{}, false
	}
	desc := terms[0].desc
	for i, t := range terms {
		if t.desc != desc || t.col.Nullable || values[i] == nil {
			return Pred{}, false
		}
	}
	cols := make([]Expr, len(terms))
	params := make([]Expr, len(terms))
	for i, t := range terms {
		cols[i] = t.expr
		params[i] = Param{Value: values[i]}
	}
	op := ">"
	if desc {
		op = "<"
	}
	return pred(Binary{Op: op, Left: List{Items: cols}, Right: List{Items: params}}), true
}

// afterTerm is "strictly after v" for one ordering term, or the zero Pred when
// nothing is.
func afterTerm(t keysetTerm, v any) Pred {
	if v == nil {
		if t.nullsAfter {
			// NULL is the last position in this term's ordering.
			return Pred{}
		}
		// NULLs sort first, so every real value is after them.
		return pred(Unary{Op: "IS NOT NULL", Operand: t.expr, Postfix: true})
	}
	op := ">"
	if t.desc {
		op = "<"
	}
	base := pred(Binary{Op: op, Left: t.expr, Right: Param{Value: v}})
	if t.nullsAfter && t.col.Nullable {
		// The boundary is a real value and NULLs sort after every real value,
		// so the NULL rows are part of what follows it.
		return Or(base, pred(Unary{Op: "IS NULL", Operand: t.expr, Postfix: true}))
	}
	return base
}

// equalTerm is the tie test that carries the comparison to the next term.
func equalTerm(t keysetTerm, v any) Pred {
	if v == nil {
		return pred(Unary{Op: "IS NULL", Operand: t.expr, Postfix: true})
	}
	return pred(Binary{Op: "=", Left: t.expr, Right: Param{Value: v}})
}
