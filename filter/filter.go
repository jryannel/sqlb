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
)

// Options configures parsing for one resource.
type Options struct {
	// Model supplies the columns and their capabilities. Required.
	Model *sqlb.Model

	DefaultPageSize int
	MaxPageSize     int
	MaxFilters      int
	MaxSortTerms    int

	// Expandable lists the relation names ?expand may name. Parsing validates
	// against it; performing the join is the caller's job, and Apply is not
	// that caller — it fails rather than dropping the parameter. Setting this
	// is therefore a commitment to reading Query.Expand and joining explicitly.
	//
	// The rest package cannot make that commitment yet, so it rejects a
	// non-empty Expandable at startup.
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
// Apply cannot perform an expansion, so it fails the builder rather than
// dropping one. Expansion is a join whose shape Apply does not know: the
// builder is generic over the row type T, and an expanded row is wider than T.
// A caller that means to expand reads Query.Expand and issues the Join itself.
func Apply[T any](b *sqlb.Builder[T], q *Query) *sqlb.Builder[T] {
	if len(q.Expand) > 0 {
		return b.Fail(fmt.Errorf(
			"filter: cannot apply ?expand=%s: Apply does not perform relation joins, "+
				"so applying it would drop the parameter; read Query.Expand and join explicitly",
			strings.Join(q.Expand, ",")))
	}

	b.Where(q.Where...)

	names := q.Select
	if len(names) == 0 {
		for _, col := range b.Model().Selectable() {
			names = append(names, col.Name)
		}
	}
	items := make([]sqlb.Selectable, len(names))
	for i, name := range names {
		items[i] = sqlb.F(name)
	}
	b.ClearSelect().Select(items...)

	b.OrderBy(q.Order...)
	return b.Limit(q.Limit).Offset(q.Offset)
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

	if n := len(q.Where); n > opts.maxFilters() {
		p.errf("filter", "", "%d filters requested, the limit is %d", n, opts.maxFilters())
	}

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
)

var operators = map[string]opKind{
	"eq": opBinary, "ne": opBinary, "neq": opBinary,
	"gt": opBinary, "gte": opBinary, "lt": opBinary, "lte": opBinary,
	"in": opList, "nin": opList,
	"isnull": opNullary, "notnull": opNullary,
	"between": opRange,
	"like":    opPattern, "ilike": opPattern,
	"contains": opPattern, "startswith": opPattern, "endswith": opPattern,
}

func (p *parser) build(col *sqlb.ColumnInfo, op, value, param, raw string) (sqlb.Pred, bool) {
	f := sqlb.F(col.Name)
	kind, known := operators[op]
	if !known {
		p.errAllowed(param, raw, fmt.Sprintf("unknown operator %q", op), operatorNames())
		return sqlb.Pred{}, false
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
		vals := make([]any, 0, len(parts))
		for _, part := range parts {
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
