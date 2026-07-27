package rest

import (
	"context"
	"fmt"
	"net/http"
	"sort"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jryannel/sqlb"
	"github.com/jryannel/sqlb/filter"
)

// itemInput addresses a single row.
//
// The path template is always `{id}` whatever the primary key column is called,
// because the URL names the resource's identity rather than its storage. The
// column name is what the predicate uses.
type itemInput struct {
	ID string `path:"id" doc:"Primary key of the row"`
}

type itemOutput[T any] struct {
	Body row[T]
}

type createdOutput[T any] struct {
	Body row[T]
}

type createInput[C any] struct {
	Body C
}

type updateInput[U any] struct {
	ID   string `path:"id" doc:"Primary key of the row"`
	Body U
}

// key coerces a path segment into the primary key's Go type, so that a uuid
// binds as a uuid. Postgres will not compare a uuid column to text, so getting
// this wrong is an error from the driver rather than an empty result.
func (b *binding[T]) key(raw string) (any, error) {
	v, err := filter.Coerce(raw, b.model.PK.Type)
	if err != nil {
		return nil, &Problem{
			Title:  http.StatusText(http.StatusUnprocessableEntity),
			Status: http.StatusUnprocessableEntity,
			Detail: "the path does not name a valid " + b.opts.name(),
			Errors: []*ProblemDetail{{
				Message:  err.Error(),
				Location: "path.id",
				Value:    raw,
			}},
		}
	}
	return v, nil
}

// selection is the default projection, as Selectable items.
func (b *binding[T]) selection() []sqlb.Selectable {
	items := make([]sqlb.Selectable, len(b.selectable))
	for i, col := range b.selectable {
		items[i] = sqlb.F(col.Name)
	}
	return items
}

func registerRead[T any](api huma.API, db sqlb.Executor, b *binding[T]) {
	reg := api.OpenAPI().Components.Schemas
	opts := b.opts

	huma.Register(api, huma.Operation{
		OperationID: "get-" + opts.name(),
		Method:      http.MethodGet,
		Path:        opts.itemPath(),
		Summary:     "Fetch one " + opts.name(),
		Description: opts.Description,
		Tags:        []string{opts.tag()},
		// The operation declares no query parameters, so anything in the query
		// string is a mistake. Dropping it silently would answer a question the
		// client did not ask.
		RejectUnknownQueryParameters: true,
		Responses: errorResponses(reg,
			http.StatusNotFound, http.StatusUnprocessableEntity, http.StatusInternalServerError),
	}, func(ctx context.Context, in *itemInput) (*itemOutput[T], error) {
		key, err := b.key(in.ID)
		if err != nil {
			return nil, err
		}
		found, err := sqlb.Query[T]().
			Select(b.selection()...).
			Where(sqlb.F(b.model.PK.Name).Eq(key)).
			One(ctx, db)
		if err != nil {
			return nil, asHumaError(err, opts.name())
		}
		return &itemOutput[T]{Body: row[T]{value: found, cols: b.selectable, names: b.jsonName}}, nil
	})
}

func registerCreate[T any, C CreateBody[T]](api huma.API, db sqlb.Executor, b *binding[T]) {
	reg := api.OpenAPI().Components.Schemas
	opts := b.opts

	huma.Register(api, huma.Operation{
		OperationID:   "create-" + opts.name(),
		Method:        http.MethodPost,
		Path:          opts.Path,
		Summary:       "Create a " + opts.name(),
		Description:   createDescription(b),
		Tags:          []string{opts.tag()},
		DefaultStatus: statusCreated,
		Responses: errorResponses(reg,
			http.StatusUnprocessableEntity, http.StatusInternalServerError),
	}, func(ctx context.Context, in *createInput[C]) (*createdOutput[T], error) {
		value, err := in.Body.Row()
		if err != nil {
			return nil, unprocessable(err, "body")
		}
		if value == nil {
			return nil, unprocessable(fmt.Errorf("the request body produced no %s", opts.name()), "body")
		}

		// Read-only columns are omitted rather than rejected: the database or a
		// BeforeCreate hook owns them, and the body type has no field for them
		// anyway. Omit leaves the rest of Insert's behaviour intact, so a
		// defaulted column left at its zero value still comes from the database.
		created, err := sqlb.InsertRows(value).Omit(b.readOnly...).One(ctx, db)
		if err != nil {
			return nil, asHumaError(err, opts.name())
		}
		return &createdOutput[T]{Body: row[T]{value: created, cols: b.selectable, names: b.jsonName}}, nil
	})
}

func registerUpdate[T any, U UpdateBody](api huma.API, db sqlb.Executor, b *binding[T]) {
	reg := api.OpenAPI().Components.Schemas
	opts := b.opts

	huma.Register(api, huma.Operation{
		OperationID: "update-" + opts.name(),
		Method:      http.MethodPatch,
		Path:        opts.itemPath(),
		Summary:     "Update a " + opts.name(),
		Description: "Only the fields the request carries are written. " + opts.Description,
		Tags:        []string{opts.tag()},
		Responses: errorResponses(reg,
			http.StatusBadRequest, http.StatusNotFound,
			http.StatusUnprocessableEntity, http.StatusInternalServerError),
	}, func(ctx context.Context, in *updateInput[U]) (*itemOutput[T], error) {
		key, err := b.key(in.ID)
		if err != nil {
			return nil, err
		}
		changes, err := in.Body.Changes()
		if err != nil {
			return nil, unprocessable(err, "body")
		}
		if len(changes) == 0 {
			return nil, &Problem{
				Title:  http.StatusText(http.StatusBadRequest),
				Status: http.StatusBadRequest,
				Detail: "the request body named no writable column",
				Errors: []*ProblemDetail{{
					Message:  "at least one field must be given",
					Location: "body",
					Allowed:  b.updatableNames(),
				}},
			}
		}

		// Sorted, so that the same request compiles to the same SQL and a test
		// can assert on the statement.
		names := make([]string, 0, len(changes))
		for name := range changes {
			names = append(names, name)
		}
		sort.Strings(names)

		if problem := b.rejectUnwritable(names); problem != nil {
			return nil, problem
		}

		stmt := sqlb.UpdateRows[T]().Where(sqlb.F(b.model.PK.Name).Eq(key))
		for _, name := range names {
			stmt.Set(name, changes[name])
		}
		updated, err := stmt.One(ctx, db)
		if err != nil {
			return nil, asHumaError(err, opts.name())
		}
		return &itemOutput[T]{Body: row[T]{value: updated, cols: b.selectable, names: b.jsonName}}, nil
	})
}

func registerDelete[T any](api huma.API, db sqlb.Executor, b *binding[T]) {
	reg := api.OpenAPI().Components.Schemas
	opts := b.opts

	huma.Register(api, huma.Operation{
		OperationID:                  "delete-" + opts.name(),
		Method:                       http.MethodDelete,
		Path:                         opts.itemPath(),
		Summary:                      "Delete a " + opts.name(),
		Tags:                         []string{opts.tag()},
		DefaultStatus:                statusNoBody,
		RejectUnknownQueryParameters: true,
		Responses: errorResponses(reg,
			http.StatusNotFound, http.StatusUnprocessableEntity, http.StatusInternalServerError),
	}, func(ctx context.Context, in *itemInput) (*struct{}, error) {
		key, err := b.key(in.ID)
		if err != nil {
			return nil, err
		}
		n, err := sqlb.DeleteRows[T]().Where(sqlb.F(b.model.PK.Name).Eq(key)).Exec(ctx, db)
		if err != nil {
			return nil, asHumaError(err, opts.name())
		}
		if n == 0 {
			return nil, newError(http.StatusNotFound, "no "+opts.name()+" matched")
		}
		return nil, nil
	})
}

// rejectUnwritable reports the columns a PATCH may not set.
//
// A hidden column is reported as unknown rather than as unwritable, and never
// appears in the allow-list, so that the rejection cannot be used to enumerate
// what the resource is concealing.
func (b *binding[T]) rejectUnwritable(names []string) *Problem {
	var details []*ProblemDetail
	for _, name := range names {
		col := b.model.Column(name)
		switch {
		case col == nil || col.Hidden:
			details = append(details, &ProblemDetail{
				Message:  "unknown column",
				Location: "body." + name,
				Allowed:  b.updatableNames(),
			})
		case col.PrimaryKey || col.ReadOnly:
			details = append(details, &ProblemDetail{
				Message:  "column is read-only",
				Location: "body." + name,
				Allowed:  b.updatableNames(),
			})
		case col.Immutable:
			details = append(details, &ProblemDetail{
				Message:  "column cannot be changed after the row is created",
				Location: "body." + name,
				Allowed:  b.updatableNames(),
			})
		}
	}
	if details == nil {
		return nil
	}
	return &Problem{
		Title:  http.StatusText(http.StatusUnprocessableEntity),
		Status: http.StatusUnprocessableEntity,
		Detail: "one or more fields cannot be written",
		Errors: details,
	}
}

// updatableNames lists the columns a PATCH may set.
func (b *binding[T]) updatableNames() []string {
	var out []string
	for _, col := range b.writable {
		if !col.Immutable {
			out = append(out, col.Name)
		}
	}
	return out
}

func unprocessable(err error, location string) *Problem {
	return &Problem{
		Title:  http.StatusText(http.StatusUnprocessableEntity),
		Status: http.StatusUnprocessableEntity,
		Detail: "the request body was rejected",
		Errors: []*ProblemDetail{{Message: err.Error(), Location: location}},
	}
}

func createDescription[T any](b *binding[T]) string {
	desc := b.opts.Description
	if desc != "" {
		desc += "\n\n"
	}
	return desc + "Read-only columns are supplied by the database or by a hook and are " +
		"not accepted in the body. The stored row is returned, so generated identifiers " +
		"and defaults come back in the response."
}
