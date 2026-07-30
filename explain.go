package sqlb

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Compilable is anything that renders to SQL: every builder and every mutation
// statement in this package.
type Compilable interface {
	SQL() (string, []any, error)
}

// Explain asks Postgres to plan a query without running it.
//
// It answers two questions that `SQL()` alone cannot. First, whether the
// statement is actually valid against the live database — a column that no
// longer exists, or a type that no longer matches, fails here rather than in
// production. Second, whether the plan is still the one you expect: an index
// scan that silently became a sequential scan is invisible in the SQL text and
// obvious in the plan.
//
// Both make it usable as a test assertion, which is the point. A query whose
// plan regresses can fail a build:
//
//	plan, err := sqlb.Explain(ctx, db, q)
//	if err != nil {
//	    t.Fatal(err)
//	}
//	if d := plan.Diagnostics(); len(d) > 0 {
//	    t.Errorf("query plan regressed:\n%s", sqlb.Diagnostics(d))
//	}
//
// Explain does not execute the statement, so it is safe on mutations. Use
// ExplainAnalyze only when you mean to run it.
func Explain(ctx context.Context, db Executor, q Compilable) (*Plan, error) {
	return explain(ctx, db, q, false)
}

// ExplainAnalyze plans and *executes* the statement, returning real timings and
// row counts rather than estimates.
//
// On an INSERT, UPDATE or DELETE this writes to the database. Run it inside a
// transaction you roll back, or not at all.
func ExplainAnalyze(ctx context.Context, db Executor, q Compilable) (*Plan, error) {
	return explain(ctx, db, q, true)
}

func explain(ctx context.Context, db Executor, q Compilable, analyze bool) (*Plan, error) {
	query, args, err := q.SQL()
	if err != nil {
		return nil, err
	}

	opts := "FORMAT JSON, VERBOSE, COSTS"
	if analyze {
		opts += ", ANALYZE, BUFFERS"
	}
	rows, err := db.Query(ctx, "EXPLAIN ("+opts+") "+query, args...)
	if err != nil {
		// A failure here is the useful signal: the statement is not valid
		// against this database. Keep the SQL attached so the caller can see
		// what was rejected.
		return nil, fmt.Errorf("sqlb: explaining %s: %w", truncate(query, 400), err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("sqlb: EXPLAIN returned no rows")
	}
	var raw []byte
	if err := rows.Scan(&raw); err != nil {
		return nil, fmt.Errorf("sqlb: reading EXPLAIN output: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	plan := &Plan{SQL: query, Args: args, Analyzed: analyze, Raw: raw}
	if err := plan.parse(raw); err != nil {
		return nil, err
	}
	return plan, nil
}

// Plan is a parsed Postgres query plan.
type Plan struct {
	SQL      string
	Args     []any
	Analyzed bool
	Raw      json.RawMessage

	// TotalCost is the planner's estimate for the whole statement, in its
	// arbitrary cost units. Useful for comparison against itself over time,
	// not as an absolute.
	TotalCost float64
	// PlanRows is the estimated row count at the root.
	PlanRows int64
	// ActualRows and ActualMS are populated only by ExplainAnalyze.
	ActualRows int64
	ActualMS   float64

	// Nodes is the plan tree flattened depth-first, which is the convenient
	// shape for scanning rather than rendering.
	Nodes []PlanNode
}

// PlanNode is one step of the plan.
type PlanNode struct {
	Depth      int
	Type       string
	Relation   string
	Index      string
	Filter     string
	TotalCost  float64
	PlanRows   int64
	ActualRows int64
	ActualMS   float64
	SortMethod string
	SortSpace  string
}

type rawPlan struct {
	Plan          rawNode `json:"Plan"`
	ExecutionTime float64 `json:"Execution Time"`
}

type rawNode struct {
	NodeType     string    `json:"Node Type"`
	RelationName string    `json:"Relation Name"`
	IndexName    string    `json:"Index Name"`
	Filter       string    `json:"Filter"`
	IndexCond    string    `json:"Index Cond"`
	TotalCost    float64   `json:"Total Cost"`
	PlanRows     int64     `json:"Plan Rows"`
	ActualRows   int64     `json:"Actual Rows"`
	ActualTotal  float64   `json:"Actual Total Time"`
	SortMethod   string    `json:"Sort Method"`
	SortSpace    string    `json:"Sort Space Type"`
	Plans        []rawNode `json:"Plans"`
}

func (p *Plan) parse(raw []byte) error {
	var plans []rawPlan
	if err := json.Unmarshal(raw, &plans); err != nil {
		return fmt.Errorf("sqlb: parsing EXPLAIN output: %w", err)
	}
	if len(plans) == 0 {
		return fmt.Errorf("sqlb: EXPLAIN output contained no plan")
	}

	root := plans[0].Plan
	p.TotalCost = root.TotalCost
	p.PlanRows = root.PlanRows
	p.ActualRows = root.ActualRows
	p.ActualMS = plans[0].ExecutionTime
	p.walk(root, 0)
	return nil
}

func (p *Plan) walk(n rawNode, depth int) {
	cond := n.Filter
	if cond == "" {
		cond = n.IndexCond
	}
	p.Nodes = append(p.Nodes, PlanNode{
		Depth:      depth,
		Type:       n.NodeType,
		Relation:   n.RelationName,
		Index:      n.IndexName,
		Filter:     cond,
		TotalCost:  n.TotalCost,
		PlanRows:   n.PlanRows,
		ActualRows: n.ActualRows,
		ActualMS:   n.ActualTotal,
		SortMethod: n.SortMethod,
		SortSpace:  n.SortSpace,
	})
	for _, child := range n.Plans {
		p.walk(child, depth+1)
	}
}

// UsesSeqScan reports whether any node sequentially scans the named relation.
// Pass an empty string to ask about any relation.
func (p *Plan) UsesSeqScan(relation string) bool {
	for _, n := range p.Nodes {
		if strings.Contains(n.Type, "Seq Scan") && (relation == "" || n.Relation == relation) {
			return true
		}
	}
	return false
}

// UsesIndex reports whether the named index appears anywhere in the plan.
func (p *Plan) UsesIndex(name string) bool {
	for _, n := range p.Nodes {
		if n.Index == name {
			return true
		}
	}
	return false
}

// PlanDiagnostic is an observation about a plan that is worth acting on.
type PlanDiagnostic struct {
	Rule    string
	Node    string
	Message string
	Fix     string
}

func (d PlanDiagnostic) String() string {
	s := fmt.Sprintf("[%s] %s: %s", d.Rule, d.Node, d.Message)
	if d.Fix != "" {
		s += "\n    fix: " + d.Fix
	}
	return s
}

// Diagnostics renders a slice of plan diagnostics as text.
func Diagnostics(ds []PlanDiagnostic) string {
	parts := make([]string, len(ds))
	for i, d := range ds {
		parts[i] = d.String()
	}
	return strings.Join(parts, "\n")
}

// SeqScanRowThreshold is the estimated row count above which a sequential scan
// is reported. Small tables are legitimately scanned, so flagging every one
// would be noise that trains readers to ignore the output.
var SeqScanRowThreshold int64 = 1000

// Diagnostics reports plan shapes that usually mean a missing index or a query
// that will not scale. They are advisory: a sequential scan over a lookup table
// is correct, and so is a sort of twenty rows.
func (p *Plan) Diagnostics() []PlanDiagnostic {
	var out []PlanDiagnostic

	for _, n := range p.Nodes {
		rows := n.PlanRows
		if p.Analyzed && n.ActualRows > 0 {
			rows = n.ActualRows
		}

		switch {
		case strings.Contains(n.Type, "Seq Scan") && rows >= SeqScanRowThreshold:
			out = append(out, PlanDiagnostic{
				Rule: "seq-scan", Node: nodeName(n),
				Message: fmt.Sprintf("sequential scan over ~%d rows%s", rows, filterSuffix(n)),
				Fix:     fmt.Sprintf("add an index covering the filtered columns on %q", n.Relation),
			})

		// An external sort has spilled to disk, which is far slower than one
		// that fits in work_mem and usually means the sort should be served
		// by an index instead.
		case n.SortMethod != "" && strings.Contains(strings.ToLower(n.SortMethod), "external"):
			out = append(out, PlanDiagnostic{
				Rule: "external-sort", Node: nodeName(n),
				Message: fmt.Sprintf("sort spilled to disk (%s, %s)", n.SortMethod, n.SortSpace),
				Fix:     "add an index matching the ORDER BY, or reduce the rows sorted before ordering",
			})

		// A nested loop whose inner side rescans a table is the classic
		// accidental N+1 in SQL form.
		case strings.Contains(n.Type, "Nested Loop") && rows >= SeqScanRowThreshold:
			out = append(out, PlanDiagnostic{
				Rule: "nested-loop", Node: nodeName(n),
				Message: fmt.Sprintf("nested loop over ~%d rows, which rescans the inner side per outer row", rows),
				Fix:     "index the join column so the planner can use a hash or merge join",
			})
		}
	}
	return out
}

func nodeName(n PlanNode) string {
	if n.Relation != "" {
		return n.Type + " on " + n.Relation
	}
	return n.Type
}

func filterSuffix(n PlanNode) string {
	if n.Filter == "" {
		return ""
	}
	return " filtering on " + n.Filter
}

// String renders the plan as an indented tree, in the shape a reader — or an
// agent comparing two runs — can scan quickly.
func (p *Plan) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "cost=%.2f rows=%d", p.TotalCost, p.PlanRows)
	if p.Analyzed {
		fmt.Fprintf(&b, " actual_rows=%d time=%.2fms", p.ActualRows, p.ActualMS)
	}
	b.WriteString("\n")
	for _, n := range p.Nodes {
		fmt.Fprintf(&b, "%s-> %s", strings.Repeat("  ", n.Depth+1), n.Type)
		if n.Relation != "" {
			fmt.Fprintf(&b, " on %s", n.Relation)
		}
		if n.Index != "" {
			fmt.Fprintf(&b, " using %s", n.Index)
		}
		fmt.Fprintf(&b, " (cost=%.2f rows=%d)", n.TotalCost, n.PlanRows)
		if n.Filter != "" {
			fmt.Fprintf(&b, " filter=%s", n.Filter)
		}
		b.WriteString("\n")
	}
	return b.String()
}
