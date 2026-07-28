package rest

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jryannel/sqlb"
	"github.com/jryannel/sqlb/filter"
)

// Page is the body of a list response.
//
// Total is absent unless the request asked for it with `?count=exact`, because
// counting is a second query over the same predicate and most clients only need
// to know whether to fetch again. HasMore answers that for the price of reading
// one extra row.
// NextCursor is the position to resume from, and is the paging a client should
// prefer: it costs the same at any depth and does not skip or repeat rows when
// the table is written to mid-walk. It is present whenever there is a next page
// and the model has a primary key to break ties with, including on a request
// that paged by offset — so a client can switch to cursors without a flag.
type Page[T any] struct {
	Items      []row[T] `json:"items" doc:"The rows on this page"`
	Page       int      `json:"page" doc:"1-based page number"`
	PerPage    int      `json:"per_page" doc:"Rows requested per page, after the resource's ceiling was applied"`
	HasMore    bool     `json:"has_more" doc:"Whether a further page exists"`
	NextCursor *string  `json:"next_cursor,omitempty" doc:"Position to resume from; pass it back as ?cursor="`
	Total      *int64   `json:"total,omitempty" doc:"Total matching rows; present only when ?count=exact was given"`
}

// listInput carries the raw query string.
//
// The filter grammar is compositional, so there is no Go struct that could
// declare its parameters; the resolver hands the untouched url.Values to
// filter.Parse, which validates them against the model's capabilities. The
// OpenAPI document is written separately, by listParams.
type listInput struct {
	query url.Values
}

// Resolve captures the query parameters before the handler runs.
func (in *listInput) Resolve(ctx huma.Context) []error {
	u := ctx.URL()
	in.query = u.Query()
	return nil
}

type listOutput[T any] struct {
	Body Page[T]
}

func registerList[T any](api huma.API, db sqlb.Executor, b *binding[T]) {
	reg := api.OpenAPI().Components.Schemas
	opts := b.opts

	huma.Register(api, huma.Operation{
		OperationID: "list-" + opts.name(),
		Method:      http.MethodGet,
		Path:        opts.Path,
		Summary:     "List " + opts.name(),
		Description: listDescription(b),
		Tags:        []string{opts.tag()},
		Parameters:  listParams(b),
		Responses:   errorResponses(reg, http.StatusBadRequest, http.StatusInternalServerError),
	}, func(ctx context.Context, in *listInput) (*listOutput[T], error) {
		q, err := filter.Parse(in.query, filter.Options{
			Model:           b.model,
			DefaultPageSize: opts.DefaultPageSize,
			MaxPageSize:     opts.MaxPageSize,
			MaxFilters:      opts.MaxFilters,
			MaxSortTerms:    opts.MaxSortTerms,
			Expandable:      opts.Expandable,
			DisableSearch:   opts.DisableSearch,
		})
		if err != nil {
			return nil, asHumaError(ctx, err, opts.name())
		}

		query := filter.Apply(sqlb.Query[T](), q)

		// One row beyond the page tells the client whether to ask again,
		// without the count query that would otherwise be the only way to know.
		rows, err := query.Limit(q.Limit+1).All(ctx, db)
		if err != nil {
			return nil, asHumaError(ctx, err, opts.name())
		}
		hasMore := len(rows) > q.Limit
		if hasMore {
			rows = rows[:q.Limit]
		}

		body := Page[T]{
			Items:   b.rowsOf(rows, b.columnsFor(q.Select), q.Expand),
			Page:    q.Page,
			PerPage: q.PageSize,
			HasMore: hasMore,
		}

		// The cursor names the last row of *this* page, so it is only built
		// when there is a page after it. filter.Apply has already made the
		// ordering total and projected whatever it orders by, which is what
		// makes the last row enough to name a position.
		if hasMore && b.model.PK != nil {
			cursor, err := query.CursorFor(rows[len(rows)-1])
			if err != nil {
				return nil, asHumaError(ctx, err, opts.name())
			}
			body.NextCursor = ptr(string(cursor))
		}
		// Items is marshalled as [] rather than null when empty: a client
		// iterating the result should not have to test for it.
		if body.Items == nil {
			body.Items = []row[T]{}
		}

		if in.query.Get("count") == "exact" {
			total, err := query.Count(ctx, db)
			if err != nil {
				return nil, asHumaError(ctx, err, opts.name())
			}
			body.Total = &total
		}

		return &listOutput[T]{Body: body}, nil
	})
}

func listDescription[T any](b *binding[T]) string {
	desc := b.opts.Description
	if desc != "" {
		desc += "\n\n"
	}
	return desc + fmt.Sprintf(
		"Filtering, sorting and searching are restricted to the columns that declare "+
			"the capability; a request naming any other column is rejected with the list "+
			"of columns that would have been accepted. At most %d filter conditions may be "+
			"combined in one request.",
		orDefault(b.opts.MaxFilters, filter.MaxFilters))
}
