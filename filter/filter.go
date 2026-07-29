// Package filter compiles URL query parameters into sqlb predicates.
//
// It is the second producer of the predicate AST: hand-written Go is the first.
// Both go through the same builder, so a filter arriving over HTTP is subject
// to the same compilation, the same bind-parameter discipline and the same
// query hooks as a query written by hand.
//
// Nothing is filterable, sortable or searchable unless the column declares that
// capability, and the parser reports the allowed columns when a request asks
// for one that does not. A request naming an unknown or uncapable column is a
// 400, never a leak and never a silently ignored parameter.
//
// Grammar:
//
//	?status=eq.active            operator form
//	?email=alice@example.com     shorthand, equivalent to eq
//	?age=gte.18&age=lt.65        repeated params conjoin
//	?tag=in.a,b,c                value lists
//	?deleted_at=isnull           null tests
//	?or=(status.eq.draft,age.lt.18)   explicit disjunction
//	?sort=-created_at,name       sorting, "-" for descending
//	?select=id,name              projection
//	?search=ada                  fan-out over searchable columns
//	?page=2&per_page=50          pagination
package filter

import (
	"encoding"
	"fmt"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/jryannel/sqlb"
)

// Defaults applied when Options leaves a limit unset. They are deliberately
// conservative: an unbounded list endpoint is a denial-of-service waiting for a
// client that forgets to paginate.
const (
	DefaultPageSize = 25
	MaxPageSize     = 200
	MaxFilters      = 24
	MaxSortTerms    = 4
	MaxGroupDepth   = 3
	// MaxListValues bounds one `in`/`nin` list. A list is a single condition
	// against MaxFilters however long it is, so without this the budget is
	// bypassed by writing ?id=in.1,2,3,… — one parameter, one predicate, and a
	// bind parameter per member until the driver's 65535 runs out.
	MaxListValues = 100
	// MaxValueLength bounds one filter value or search term. The pattern
	// operators pass their operand through unescaped on purpose, so a value is
	// a lever on how much work a scan does, and a long one is a cheap way to
	// pull that lever.
	MaxValueLength = 256
)

// Options configures parsing for one resource.
type Options struct {
	// Model supplies the columns and their capabilities. Required.
	Model *sqlb.Model

	DefaultPageSize int
	// MaxFilters bounds the number of leaf conditions a request may ask for,
	// counting the ones inside `or=`/`and=` groups. Counting top-level
	// parameters instead would leave the budget open to a single group holding
	// as many conditions as the client cared to write.
	MaxFilters   int
	MaxPageSize  int
	MaxSortTerms int
	// MaxListValues bounds one `in`/`nin` list; MaxValueLength bounds one
	// filter value or search term.
	MaxListValues  int
	MaxValueLength int

	// Expandable lists the relation names ?expand may name. Parsing validates
	// against it and Apply performs the join, so a parsed ?expand is never
	// silently dropped: a name that is not here is a 400 listing the ones that
	// are.
	//
	// The rest package validates these against the model at startup, so a
	// relation that cannot be expanded is a mounting error rather than a
	// request-time surprise.
	Expandable []string

	// DisableSearch rejects ?search even when columns are searchable.
	DisableSearch bool
}

func (o Options) defaultPageSize() int {
	if o.DefaultPageSize > 0 {
		return o.DefaultPageSize
	}
	return DefaultPageSize
}

func (o Options) maxPageSize() int {
	if o.MaxPageSize > 0 {
		return o.MaxPageSize
	}
	return MaxPageSize
}

func (o Options) maxFilters() int {
	if o.MaxFilters > 0 {
		return o.MaxFilters
	}
	return MaxFilters
}

func (o Options) maxSortTerms() int {
	if o.MaxSortTerms > 0 {
		return o.MaxSortTerms
	}
	return MaxSortTerms
}

func (o Options) maxListValues() int {
	if o.MaxListValues > 0 {
		return o.MaxListValues
	}
	return MaxListValues
}

func (o Options) maxValueLength() int {
	if o.MaxValueLength > 0 {
		return o.MaxValueLength
	}
	return MaxValueLength
}

// Query is a parsed request: predicates, ordering, projection and pagination,
// all already validated against the model.
type Query struct {
	Where  []sqlb.Pred
	Order  []sqlb.Order
	Select []string
	Expand []string
	Search string

	Page     int
	PageSize int
	Limit    int
	Offset   int

	// Cursor is the keyset position `?cursor=` asked to resume from, empty for
	// the first page. It is the alternative to Page and Offset rather than an
	// addition to them: a request carrying both is refused, since the two
	// answer the same question with different answers.
	Cursor sqlb.Cursor
}

// Apply writes the parsed query onto a builder.
//
// Apply owns the projection. Given ?select it uses those columns; otherwise it
// projects every non-hidden column. It does not fall back to the builder's
// default of "all mapped columns", because that would put a Hidden column into
// a REST response any time a handler forgot to project. A caller wanting a
// custom projection should apply Where, Order and the limits from the Query
// fields directly instead.
//
// An expansion is applied as a relation join. Apply does this rather than
// refusing, and the projection below is why it can: ?select names columns of T,
// and an expanded relation is not one — it arrives as its own JSON value in a
// column the scanner recognises, so the row stays exactly as wide as T.
func Apply[T any](b *sqlb.Builder[T], q *Query) *sqlb.Builder[T] {
	b.Where(q.Where...)
	b.Expand(q.Expand...)

	// Ordering is settled before the projection, because Stable may add a term
	// and the projection has to cover whatever the ordering ended up being.
	b.OrderBy(q.Order...)
	b.Stable()

	names := make([]string, 0, len(q.Select))
	if len(q.Select) > 0 {
		names = append(names, q.Select...)
	} else {
		for _, col := range b.Model().Selectable() {
			names = append(names, col.Name)
		}
	}
	names = append(names, unprojectedOrderColumns(b, names)...)

	items := make([]sqlb.Selectable, len(names))
	for i, name := range names {
		items[i] = sqlb.F(name)
	}
	b.ClearSelect().Select(items...)

	b.After(q.Cursor)
	return b.Limit(q.Limit).Offset(q.Offset)
}

// unprojectedOrderColumns names the ordering columns a projection would leave
// out.
//
// A cursor is built by reading the ordering columns off the last row, so
// `?select=id&sort=created_at` has to fetch created_at even though the response
// will not show it — otherwise the cursor would encode a zero time and the next
// page would start from the beginning. Selecting more than the response shows is
// safe here and nowhere else: rest marshals from the request's ?select, not
// from the columns the statement happened to read.
func unprojectedOrderColumns[T any](b *sqlb.Builder[T], projected []string) []string {
	have := make(map[string]bool, len(projected))
	for _, name := range projected {
		have[name] = true
	}
	var out []string
	for _, name := range b.OrderColumns() {
		if have[name] {
			continue
		}
		have[name] = true
		out = append(out, name)
	}
	return out
}

// reserved parameter names, which never name a column.
//
// "count" is reserved but unused here: it asks a list endpoint for a total row
// count, which costs a second query and so is the REST layer's decision rather
// than the parser's. It is listed anyway, because a column named `count` would
// otherwise shadow it and the collision would only surface once someone asked
// for a total.
var reserved = map[string]bool{
	"select": true, "sort": true, "order": true, "search": true,
	"expand": true, "limit": true, "offset": true, "page": true,
	"per_page": true, "or": true, "and": true, "count": true,
	"cursor": true,
}

// Parse compiles URL query parameters into a Query.
//
// Every problem found is reported, not just the first, so a caller fixing a
// request sees the whole list at once.
func Parse(values url.Values, opts Options) (*Query, error) {
	if opts.Model == nil {
		return nil, fmt.Errorf("filter: Options.Model is required")
	}
	p := &parser{opts: opts, model: opts.Model}
	q := &Query{PageSize: opts.defaultPageSize()}

	// Filters, in sorted parameter order so the generated SQL is stable.
	for _, key := range sortedKeys(values) {
		if reserved[key] {
			continue
		}
		col := p.filterableColumn(key)
		if col == nil {
			continue
		}
		for _, raw := range values[key] {
			if pred, ok := p.parseCondition(col, raw, key); ok {
				q.Where = append(q.Where, pred)
			}
		}
	}

	for _, raw := range values["or"] {
		if pred, ok := p.parseGroup("or", raw, 0); ok {
			q.Where = append(q.Where, pred)
		}
	}
	for _, raw := range values["and"] {
		if pred, ok := p.parseGroup("and", raw, 0); ok {
			q.Where = append(q.Where, pred)
		}
	}

	// The budget is charged per leaf condition inside build, so a group full of
	// conditions costs what the same conditions cost written out.

	if s := firstValue(values, "search"); s != "" {
		q.Search = s
		if pred, ok := p.parseSearch(s); ok {
			q.Where = append(q.Where, pred)
		}
	}

	q.Order = p.parseSort(firstValue(values, "sort", "order"))
	q.Select = p.parseSelect(firstValue(values, "select"))
	q.Expand = p.parseExpand(firstValue(values, "expand"))
	p.parsePagination(values, q)

	if len(p.errs) > 0 {
		return nil, p.errs
	}
	return q, nil
}

type parser struct {
	opts  Options
	model *sqlb.Model
	errs  Errors
	// conditions counts every leaf condition the request asked for, wherever
	// it was written. A group is one entry in Query.Where and any number of
	// conditions, so counting entries would bound the wrong thing.
	conditions int
	// overBudget stops the count being reported once per condition after the
	// limit, which would answer a pathological request with a pathological
	// error document.
	overBudget bool
}

// withinLength bounds one operand, recording an error when it is over.
func (p *parser) withinLength(value, param, raw string) bool {
	if len(value) <= p.opts.maxValueLength() {
		return true
	}
	p.errf(param, raw, "value is %d bytes, the limit is %d",
		len(value), p.opts.maxValueLength())
	return false
}

// charge records one leaf condition against the budget, reporting the first
// time it is exceeded and refusing every condition after it.
//
// It charges before the condition is parsed rather than after it succeeds,
// because the work being bounded is the parsing: a request full of malformed
// conditions costs the same as one full of valid ones.
func (p *parser) charge() bool {
	p.conditions++
	if p.conditions <= p.opts.maxFilters() {
		return true
	}
	if !p.overBudget {
		p.overBudget = true
		p.errf("filter", "", "%d filter conditions requested, the limit is %d",
			p.conditions, p.opts.maxFilters())
	}
	return false
}

func (p *parser) errf(param, value, format string, args ...any) {
	p.errs = append(p.errs, &Error{
		Param:  param,
		Value:  value,
		Reason: fmt.Sprintf(format, args...),
	})
}

func (p *parser) errAllowed(param, value, reason string, allowed []string) {
	p.errs = append(p.errs, &Error{Param: param, Value: value, Reason: reason, Allowed: allowed})
}

// filterableColumn resolves a parameter name to a column the request is
// permitted to filter on, recording an error otherwise.
func (p *parser) filterableColumn(name string) *sqlb.ColumnInfo {
	col := p.model.Column(name)
	// A hidden column is reported as unknown rather than as un-filterable, so
	// that its existence cannot be probed by reading the rejection.
	if col == nil || col.Hidden {
		p.errAllowed(name, "", "unknown parameter", p.capable(capFilter))
		return nil
	}
	if !col.Filterable {
		p.errAllowed(name, "", "column is not filterable", p.capable(capFilter))
		return nil
	}
	return col
}

type capability int

const (
	capFilter capability = iota
	capSort
	capSearch
)

// capable lists the columns carrying a capability, for error messages. Telling
// a caller what it may ask for is what turns a 400 into a usable answer.
func (p *parser) capable(c capability) []string {
	var out []string
	for _, col := range p.model.Columns {
		if col.Hidden {
			continue
		}
		switch c {
		case capFilter:
			if col.Filterable {
				out = append(out, col.Name)
			}
		case capSort:
			if col.Sortable {
				out = append(out, col.Name)
			}
		case capSearch:
			if col.Searchable {
				out = append(out, col.Name)
			}
		}
	}
	return out
}

// parseCondition parses one `op.value` (or bare value) against a column.
func (p *parser) parseCondition(col *sqlb.ColumnInfo, raw, param string) (sqlb.Pred, bool) {
	op, value := splitOp(raw)
	return p.build(col, op, value, param, raw)
}

// splitOp separates a leading operator from its operand. A prefix is only
// treated as an operator when it names one, so `email=alice@example.com` and
// `date=2024-01-02` are read as equality rather than as a malformed operator.
func splitOp(raw string) (op, value string) {
	if head, rest, found := strings.Cut(raw, "."); found {
		if _, known := operators[head]; known {
			return head, rest
		}
	}
	if _, known := operators[raw]; known {
		return raw, ""
	}
	return "eq", raw
}

type opKind int

const (
	opBinary opKind = iota
	opList
	opNullary
	opRange
	opPattern
	// opElem takes one element of an array column; opSet takes a list of them.
	opElem
	opSet
)

var operators = map[string]opKind{
	"eq": opBinary, "ne": opBinary, "neq": opBinary,
	"gt": opBinary, "gte": opBinary, "lt": opBinary, "lte": opBinary,
	"in": opList, "nin": opList,
	"isnull": opNullary, "notnull": opNullary,
	"between": opRange,
	"like":    opPattern, "ilike": opPattern,
	"contains": opPattern, "startswith": opPattern, "endswith": opPattern,

	// Array containment. `contains` is deliberately not reused: it is a text
	// pattern operator above, and one name meaning two things depending on the
	// column it is applied to is exactly the ambiguity the generated clients
	// exist to remove (ADR-0033).
	"has": opElem, "hasany": opSet, "hasall": opSet,
}

func (p *parser) build(col *sqlb.ColumnInfo, op, value, param, raw string) (sqlb.Pred, bool) {
	if !p.charge() {
		return sqlb.Pred{}, false
	}
	f := sqlb.F(col.Name)
	kind, known := operators[op]
	if !known {
		p.errAllowed(param, raw, fmt.Sprintf("unknown operator %q", op), operatorNames())
		return sqlb.Pred{}, false
	}
	// Lists and ranges hold several operands in one value, so they are measured
	// per member where they are split rather than in aggregate here.
	if kind != opList && kind != opRange && kind != opSet && !p.withinLength(value, param, raw) {
		return sqlb.Pred{}, false
	}

	// An array column and a scalar one accept disjoint operator sets, and the
	// refusal names the alternative rather than letting Postgres report a type
	// error from a statement the caller cannot see.
	elem, isArray := arrayElem(col)
	switch {
	case isArray && !arrayOperators[kind]:
		p.errAllowed(param, raw, fmt.Sprintf("operator %q does not apply to the array column %s", op, col.Name), arrayOperatorNames())
		return sqlb.Pred{}, false
	case !isArray && (kind == opElem || kind == opSet):
		p.errf(param, raw, "operator %q needs an array column, but %s is %s", op, col.Name, col.Type)
		return sqlb.Pred{}, false
	}
	if isArray {
		return p.buildArray(col, elem, f, op, kind, value, param, raw)
	}

	switch kind {
	case opNullary:
		if op == "isnull" {
			return f.IsNull(), true
		}
		return f.NotNull(), true

	case opPattern:
		if !isTextColumn(col) {
			p.errf(param, raw, "operator %q needs a text column, but %s is %s", op, col.Name, col.Type)
			return sqlb.Pred{}, false
		}
		switch op {
		case "contains":
			return f.Contains(value), true
		case "startswith":
			return f.StartsWith(value), true
		case "endswith":
			return f.EndsWith(value), true
		case "like":
			return f.Like(value), true
		default:
			return f.ILike(value), true
		}

	case opList:
		parts := splitTopLevel(value, ',')
		if len(parts) == 0 {
			p.errf(param, raw, "operator %q needs at least one value", op)
			return sqlb.Pred{}, false
		}
		// One list is one condition however long it is, so the filter budget
		// does not bound it and this has to.
		if len(parts) > p.opts.maxListValues() {
			p.errf(param, raw, "operator %q was given %d values, the limit is %d",
				op, len(parts), p.opts.maxListValues())
			return sqlb.Pred{}, false
		}
		vals := make([]any, 0, len(parts))
		for _, part := range parts {
			if !p.withinLength(part, param, raw) {
				return sqlb.Pred{}, false
			}
			v, err := Coerce(unquote(part), col.Type)
			if err != nil {
				p.errf(param, part, "%v", err)
				return sqlb.Pred{}, false
			}
			vals = append(vals, v)
		}
		if op == "in" {
			return f.OneOf(vals...), true
		}
		return f.NotOneOf(vals...), true

	case opRange:
		parts := splitTopLevel(value, ',')
		if len(parts) != 2 {
			p.errf(param, raw, "operator \"between\" needs exactly two values, got %d", len(parts))
			return sqlb.Pred{}, false
		}
		for _, part := range parts {
			if !p.withinLength(part, param, raw) {
				return sqlb.Pred{}, false
			}
		}
		lo, err := Coerce(unquote(parts[0]), col.Type)
		if err != nil {
			p.errf(param, parts[0], "%v", err)
			return sqlb.Pred{}, false
		}
		hi, err := Coerce(unquote(parts[1]), col.Type)
		if err != nil {
			p.errf(param, parts[1], "%v", err)
			return sqlb.Pred{}, false
		}
		return f.Between(lo, hi), true

	default:
		v, err := Coerce(unquote(value), col.Type)
		if err != nil {
			p.errf(param, value, "%v", err)
			return sqlb.Pred{}, false
		}
		switch op {
		case "eq":
			return f.Eq(v), true
		case "ne", "neq":
			return f.Neq(v), true
		case "gt":
			return f.Gt(v), true
		case "gte":
			return f.Gte(v), true
		case "lt":
			return f.Lt(v), true
		default:
			return f.Lte(v), true
		}
	}
}

// arrayOperators is the set an array column accepts.
//
// Ordering and BETWEEN are absent because Postgres's array ordering is not a
// thing an API should offer; `in` is absent because a list of arrays has no
// spelling in this grammar; the pattern operators are absent because search is
// a text operation. Each of those is additive to allow later and breaking to
// withdraw, so the refusal is the starting position (ADR-0033).
var arrayOperators = map[opKind]bool{
	opElem:    true,
	opSet:     true,
	opNullary: true,
	opBinary:  true, // narrowed to eq/ne below; the ordering four are refused there
}

// buildArray builds a condition against an array column.
//
// `has` binds the *element* — `$1 = ANY(tags)` — which is why the descriptor
// keeps naming the element type rather than fusing it into an array constant.
func (p *parser) buildArray(col *sqlb.ColumnInfo, elem reflect.Type, f sqlb.Field,
	op string, kind opKind, value, param, raw string) (sqlb.Pred, bool) {

	switch kind {
	case opNullary:
		// A NULL column and an empty array are different values, so this stays
		// meaningful on an array and means what it does everywhere else.
		if op == "isnull" {
			return f.IsNull(), true
		}
		return f.NotNull(), true

	case opElem:
		v, err := Coerce(unquote(value), elem)
		if err != nil {
			p.errf(param, value, "%v", err)
			return sqlb.Pred{}, false
		}
		return f.Has(v), true

	case opSet:
		vals, ok := p.arrayOperand(elem, value, param, raw, op)
		if !ok {
			return sqlb.Pred{}, false
		}
		if op == "hasany" {
			return f.HasAny(vals...), true
		}
		return f.HasAll(vals...), true

	default:
		// Whole-array comparison. The ordering operators are refused here
		// rather than in the table above, because they share opBinary with the
		// two that are allowed.
		if op != "eq" && op != "ne" && op != "neq" {
			p.errAllowed(param, raw, fmt.Sprintf("operator %q does not apply to the array column %s", op, col.Name), arrayOperatorNames())
			return sqlb.Pred{}, false
		}
		vals, ok := p.arrayOperand(elem, value, param, raw, op)
		if !ok {
			return sqlb.Pred{}, false
		}
		if op == "eq" {
			return f.Eq(sqlb.Array(vals...)), true
		}
		return f.Neq(sqlb.Array(vals...)), true
	}
}

// arrayOperand parses the comma-separated element list an array-valued operator
// takes, under the same per-member limits a value list is held to.
func (p *parser) arrayOperand(elem reflect.Type, value, param, raw, op string) ([]any, bool) {
	parts := splitTopLevel(value, ',')
	if len(parts) > p.opts.maxListValues() {
		p.errf(param, raw, "operator %q was given %d values, the limit is %d",
			op, len(parts), p.opts.maxListValues())
		return nil, false
	}
	// Unlike `in`, an empty list is meaningful: it is the empty array, which
	// every array contains and none overlaps.
	if len(parts) == 1 && strings.TrimSpace(parts[0]) == "" {
		return nil, true
	}
	vals := make([]any, 0, len(parts))
	for _, part := range parts {
		if !p.withinLength(part, param, raw) {
			return nil, false
		}
		v, err := Coerce(unquote(part), elem)
		if err != nil {
			p.errf(param, part, "%v", err)
			return nil, false
		}
		vals = append(vals, v)
	}
	return vals, true
}

// arrayElem reports whether the column is a Postgres array, and its element
// type. bytea and json.RawMessage are []byte and are not arrays.
func arrayElem(col *sqlb.ColumnInfo) (reflect.Type, bool) {
	t := col.Type
	if t == nil || t.Kind() != reflect.Slice || t.Elem().Kind() == reflect.Uint8 {
		return nil, false
	}
	return t.Elem(), true
}

func arrayOperatorNames() []string {
	return []string{"eq", "has", "hasall", "hasany", "isnull", "ne", "neq", "notnull"}
}

// parseGroup parses `(cond,cond,...)` where each condition is
// `column.op.value` or a nested `or(...)` / `and(...)`.
func (p *parser) parseGroup(param, raw string, depth int) (sqlb.Pred, bool) {
	if depth > MaxGroupDepth {
		p.errf(param, raw, "filter groups nested deeper than %d levels", MaxGroupDepth)
		return sqlb.Pred{}, false
	}
	body, ok := strings.CutPrefix(strings.TrimSpace(raw), "(")
	if !ok || !strings.HasSuffix(body, ")") {
		p.errf(param, raw, "expected a parenthesised group such as (status.eq.active,age.gt.18)")
		return sqlb.Pred{}, false
	}
	body = strings.TrimSuffix(body, ")")

	var preds []sqlb.Pred
	for _, item := range splitTopLevel(body, ',') {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if inner, isNested := strings.CutPrefix(item, "or"); isNested && strings.HasPrefix(inner, "(") {
			if sub, ok := p.parseGroup("or", inner, depth+1); ok {
				preds = append(preds, sub)
			}
			continue
		}
		if inner, isNested := strings.CutPrefix(item, "and"); isNested && strings.HasPrefix(inner, "(") {
			if sub, ok := p.parseGroup("and", inner, depth+1); ok {
				preds = append(preds, sub)
			}
			continue
		}

		name, rest, found := strings.Cut(item, ".")
		if !found {
			p.errf(param, item, "expected column.operator.value")
			continue
		}
		col := p.filterableColumn(name)
		if col == nil {
			continue
		}
		op, value, found := strings.Cut(rest, ".")
		if !found {
			// Allows the nullary forms, e.g. deleted_at.isnull.
			op, value = rest, ""
		}
		if pred, ok := p.build(col, op, value, param, item); ok {
			preds = append(preds, pred)
		}
	}

	if len(preds) == 0 {
		return sqlb.Pred{}, false
	}
	if param == "or" {
		return sqlb.Or(preds...), true
	}
	return sqlb.And(preds...), true
}

// parseSearch fans a term out across every searchable column.
func (p *parser) parseSearch(term string) (sqlb.Pred, bool) {
	if p.opts.DisableSearch {
		p.errf("search", term, "search is not enabled for this resource")
		return sqlb.Pred{}, false
	}
	// A search term is substituted into one LIKE per searchable column, so its
	// length is multiplied by the width of the fan-out before it reaches the
	// database.
	if !p.withinLength(term, "search", term) {
		return sqlb.Pred{}, false
	}
	var preds []sqlb.Pred
	for _, col := range p.model.Columns {
		if col.Searchable && !col.Hidden {
			preds = append(preds, sqlb.F(col.Name).Contains(term))
		}
	}
	if len(preds) == 0 {
		p.errf("search", term, "no column of this resource is searchable")
		return sqlb.Pred{}, false
	}
	return sqlb.Or(preds...), true
}

func (p *parser) parseSort(raw string) []sqlb.Order {
	if raw == "" {
		return nil
	}
	terms := strings.Split(raw, ",")
	if len(terms) > p.opts.maxSortTerms() {
		p.errf("sort", raw, "%d sort terms requested, the limit is %d", len(terms), p.opts.maxSortTerms())
		return nil
	}

	var out []sqlb.Order
	for _, term := range terms {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		desc := false
		if rest, found := strings.CutPrefix(term, "-"); found {
			desc, term = true, rest
		} else if name, dir, found := strings.Cut(term, "."); found {
			// The `created_at.desc` spelling, for PostgREST familiarity.
			switch strings.ToLower(dir) {
			case "desc":
				desc, term = true, name
			case "asc":
				term = name
			default:
				p.errf("sort", term, "unknown sort direction %q, expected asc or desc", dir)
				continue
			}
		}

		col := p.model.Column(term)
		switch {
		case col == nil || col.Hidden:
			p.errAllowed("sort", term, "unknown column", p.capable(capSort))
			continue
		case !col.Sortable:
			p.errAllowed("sort", term, "column is not sortable", p.capable(capSort))
			continue
		}

		f := sqlb.F(col.Name)
		if desc {
			out = append(out, f.Desc())
			continue
		}
		out = append(out, f.Asc())
	}
	return out
}

func (p *parser) parseSelect(raw string) []string {
	if raw == "" {
		return nil
	}
	var out []string
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		col := p.model.Column(name)
		if col == nil || col.Hidden {
			p.errAllowed("select", name, "unknown column", columnNames(p.model.Selectable()))
			continue
		}
		out = append(out, col.Name)
	}
	// A projection that dropped the primary key cannot address its own rows,
	// so it is added back rather than surprising the client later.
	if len(out) > 0 && p.model.PK != nil && !contains(out, p.model.PK.Name) {
		out = append([]string{p.model.PK.Name}, out...)
	}
	return out
}

func (p *parser) parseExpand(raw string) []string {
	if raw == "" {
		return nil
	}
	var out []string
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if !contains(p.opts.Expandable, name) {
			p.errAllowed("expand", name, "relation is not expandable", p.opts.Expandable)
			continue
		}
		out = append(out, name)
	}
	return out
}

func (p *parser) parsePagination(values url.Values, q *Query) {
	size := q.PageSize
	if raw := firstValue(values, "per_page"); raw != "" {
		n, err := strconv.Atoi(raw)
		switch {
		case err != nil:
			p.errf("per_page", raw, "not a number")
		case n < 1:
			p.errf("per_page", raw, "must be at least 1")
		default:
			size = n
		}
	}
	if raw := firstValue(values, "limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		switch {
		case err != nil:
			p.errf("limit", raw, "not a number")
		case n < 1:
			p.errf("limit", raw, "must be at least 1")
		default:
			size = n
		}
	}
	// The cap is enforced rather than reported, so that a client asking for
	// more simply gets the maximum instead of an error.
	if max := p.opts.maxPageSize(); size > max {
		size = max
	}
	q.PageSize = size
	q.Limit = size

	// A cursor and an offset are two answers to "where does this page start",
	// and honouring one silently would make the other's presence a no-op the
	// client could not see. Naming both and saying which to drop is the only
	// answer that lets a caller fix it in one step.
	if raw := firstValue(values, "cursor"); raw != "" {
		q.Cursor = sqlb.Cursor(raw)
		for _, conflict := range []string{"page", "offset"} {
			if firstValue(values, conflict) != "" {
				p.errf("cursor", raw,
					"a cursor and %s both say where the page starts; send one or the other", conflict)
			}
		}
		// Page numbers are meaningless under keyset paging: the client's
		// position is the cursor, and there is no count of pages behind it.
		q.Page = 1
		return
	}

	if raw := firstValue(values, "page"); raw != "" {
		n, err := strconv.Atoi(raw)
		switch {
		case err != nil:
			p.errf("page", raw, "not a number")
		case n < 1:
			p.errf("page", raw, "must be at least 1")
		default:
			q.Page = n
			q.Offset = (n - 1) * size
		}
		return
	}
	if raw := firstValue(values, "offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		switch {
		case err != nil:
			p.errf("offset", raw, "not a number")
		case n < 0:
			p.errf("offset", raw, "must not be negative")
		default:
			q.Offset = n
		}
	}
	if q.Page == 0 {
		q.Page = q.Offset/size + 1
	}
}

// Coerce converts a URL token into the Go type of its column, so that the
// driver binds an int as an int rather than as text.
//
// It is exported because a path segment needs the same treatment as a query
// parameter: `GET /posts/{id}` has to bind a uuid as a uuid, since Postgres
// will not compare one to text. Parse uses it for every filter value.
func Coerce(s string, t reflect.Type) (any, error) {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	// time.Time is checked before the TextUnmarshaler branch: its own
	// UnmarshalText accepts RFC 3339 only, which would reject the plain dates
	// that a date-range filter is usually written with.
	if t == timeType {
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
			if v, err := time.Parse(layout, s); err == nil {
				return v, nil
			}
		}
		return nil, fmt.Errorf("expected an RFC 3339 timestamp or a date, got %q", s)
	}

	// Types that know how to parse themselves take precedence, which covers
	// uuid.UUID and similar wrappers used by generated models.
	if reflect.PointerTo(t).Implements(textUnmarshalerType) {
		v := reflect.New(t)
		// Guaranteed by the Implements check above, but asserted with the
		// comma-ok form so a future change to that condition fails loudly.
		u, ok := v.Interface().(encoding.TextUnmarshaler)
		if !ok {
			return nil, fmt.Errorf("filter: %s does not implement encoding.TextUnmarshaler", t)
		}
		if err := u.UnmarshalText([]byte(s)); err != nil {
			return nil, fmt.Errorf("invalid %s value %q: %w", t, s, err)
		}
		return v.Elem().Interface(), nil
	}

	switch t.Kind() {
	case reflect.String:
		return s, nil
	case reflect.Bool:
		v, err := strconv.ParseBool(s)
		if err != nil {
			return nil, fmt.Errorf("expected a boolean, got %q", s)
		}
		return v, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("expected an integer, got %q", s)
		}
		return v, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("expected a non-negative integer, got %q", s)
		}
		return v, nil
	case reflect.Float32, reflect.Float64:
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, fmt.Errorf("expected a number, got %q", s)
		}
		return v, nil
	}

	return nil, fmt.Errorf("values of type %s cannot be used in a filter", t)
}

var (
	timeType            = reflect.TypeOf(time.Time{})
	textUnmarshalerType = reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem()
)

func isTextColumn(col *sqlb.ColumnInfo) bool {
	t := col.Type
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Kind() == reflect.String
}

// splitTopLevel splits on sep, ignoring separators inside parentheses or
// double quotes so that grouped and quoted values survive.
func splitTopLevel(s string, sep byte) []string {
	var (
		out   []string
		depth int
		quote bool
		start int
	)
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case quote:
			if c == '\\' && i+1 < len(s) {
				i++
			} else if c == '"' {
				quote = false
			}
		case c == '"':
			quote = true
		case c == '(':
			depth++
		case c == ')':
			if depth > 0 {
				depth--
			}
		case c == sep && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start <= len(s) {
		out = append(out, s[start:])
	}
	// Drop a single trailing empty field from a trailing separator.
	if n := len(out); n > 1 && out[n-1] == "" {
		out = out[:n-1]
	}
	return out
}

// unquote removes surrounding double quotes, which let a value contain commas.
func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return strings.ReplaceAll(s[1:len(s)-1], `\"`, `"`)
	}
	return s
}

func operatorNames() []string {
	out := make([]string, 0, len(operators))
	for name := range operators {
		out = append(out, name)
	}
	sortStrings(out)
	return out
}

func columnNames(cols []*sqlb.ColumnInfo) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c.Name
	}
	return out
}

func firstValue(values url.Values, keys ...string) string {
	for _, k := range keys {
		if v := values.Get(k); v != "" {
			return v
		}
	}
	return ""
}

func sortedKeys(values url.Values) []string {
	out := make([]string, 0, len(values))
	for k := range values {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}
