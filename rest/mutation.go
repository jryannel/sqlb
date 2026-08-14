package rest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jryannel/sqlb"
)

// A declared mutation is the row-scoped half of what [Action] currently
// covers (see schema.Mutation's doc comment for why the split is additive
// rather than a replacement).
//
// The envelope is identical to Action's item form: fetch the row under
// BeforeQuery, lock it when Writes is declared, decode the body, run do
// inside the transaction, persist Writes, answer with the row. This function
// is that logic under its own name rather than a call to Action, so that
// Mutation never silently inherits a collection branch it has no use for.

// MutationSpec describes one mutation to the runtime. It restates what
// schema.Mutation declared — see [ActionSpec], which it otherwise mirrors
// field for field.
type MutationSpec struct {
	Name        string
	Path        string
	Field       string
	Summary     string
	Description string
	Writes      []string
	Touches     []string
	HasBody     bool
}

func (s MutationSpec) describe() string {
	if len(s.Touches) == 0 {
		return s.Description
	}
	reach := fmt.Sprintf(
		"Beyond the row in the response, this operation writes: %s. "+
			"That set is declared rather than enforced — see the schema for what it claims.",
		strings.Join(s.Touches, ", "))
	if s.Description == "" {
		return reach
	}
	return s.Description + "\n\n" + reach
}

func (s MutationSpec) validate(resource string) error {
	switch {
	case s.Name == "":
		return fmt.Errorf("rest: %s declares a mutation with no Name", resource)
	case s.Path == "":
		return fmt.Errorf("rest: %s mutation %q has no Path", resource, s.Name)
	}
	return nil
}

func (s MutationSpec) operationID(opts Options) string { return s.Name + "-" + opts.name() }

func missingMutationDo(resource string, spec MutationSpec) error {
	field := spec.Field
	if field == "" {
		field = spec.Name
	}
	return fmt.Errorf(
		"rest: %s declares the mutation %q, and Mutations.%s is nil;\n"+
			"  pass it to Register, e.g. Register(api, db, Mutations{%s: %s})\n"+
			"  or drop the mutation from the schema, which is the honest way to say the verb does not exist",
		resource, spec.Name, field, field, lowerFirst(field))
}

// Mutation registers a row-scoped verb on one row of T.
//
// The envelope fetches the row, hands it to do inside a transaction, persists
// spec.Writes, and answers 200 with the row — byte-for-byte [Action]'s item
// form. do reports failure by returning an error; a *Problem is answered with
// its own status.
func Mutation[T, In any](api huma.API, db sqlb.Executor, opts Options, spec MutationSpec, do func(context.Context, *T, In) error) error {
	// Not opts.validate(): see rest.Query's identical note. bind[T] does not
	// read opts.Ops either — it is unused by anything Mutation does — so
	// requiring a value that means nothing here would only be a caller
	// obligation with no correctness behind it.
	if opts.Path == "" {
		return errors.New("rest: Options.Path is required")
	} else if !strings.HasPrefix(opts.Path, "/") {
		return fmt.Errorf("rest: Options.Path %q must start with a slash", opts.Path)
	}
	if err := spec.validate(opts.Path); err != nil {
		return err
	}
	if db == nil {
		return fmt.Errorf("rest: %s has no Executor", spec.Path)
	}
	if do == nil {
		return missingMutationDo(opts.Path, spec)
	}

	b, err := bind[T](opts)
	if err != nil {
		return err
	}
	if b.model.PK == nil {
		return fmt.Errorf("rest: %s addresses a row by id but %s declares no primary key",
			spec.Path, b.model.Type)
	}

	writes, err := b.mutationWriteSet(spec)
	if err != nil {
		return err
	}

	readOnly := opts
	readOnly.Path = spec.Path
	readOnly.Ops = OpRead
	if err := checkObligations[T](b.model, db, readOnly); err != nil {
		return err
	}

	w, err := newWriter(db, opts)
	if err != nil {
		return err
	}

	fetch := b.actionSelection(writes)

	run := func(ctx context.Context, id string, body In) (*itemOutput[T], error) {
		key, err := b.key(id)
		if err != nil {
			return nil, err
		}
		out, err := write(ctx, w, func(ctx context.Context, db sqlb.Executor) (T, error) {
			q := sqlb.Query[T]().
				Select(fetch...).
				Where(sqlb.F(b.model.PK.Name).Eq(key))
			if len(writes) > 0 {
				q = q.ForUpdate()
			}
			found, err := q.One(ctx, db)
			if err != nil {
				return found, err
			}
			if err := do(ctx, &found, body); err != nil {
				var zero T
				return zero, err
			}
			if len(writes) == 0 {
				return found, nil
			}
			stmt := sqlb.UpdateRows[T]().Where(sqlb.F(b.model.PK.Name).Eq(key))
			rv := reflect.ValueOf(found)
			for _, col := range writes {
				fv, ok := valueByIndex(rv, col.Index)
				if !ok {
					continue
				}
				stmt.Set(col.Name, fv.Interface())
			}
			return stmt.One(ctx, db)
		})
		if err != nil {
			return nil, asHumaError(ctx, err, opts.name())
		}
		return &itemOutput[T]{Body: row[T]{value: out, cols: b.selectable, keys: b.jsonKey}}, nil
	}

	id := spec.operationID(opts)
	if err := refuseDuplicateID(api, opts.Path, spec.Name, id); err != nil {
		return err
	}

	op := huma.Operation{
		OperationID:                  id,
		Method:                       http.MethodPost,
		Path:                         spec.Path,
		Summary:                      spec.Summary,
		Description:                  spec.describe(),
		Tags:                         []string{opts.tag()},
		Security:                     opts.Security,
		RejectUnknownQueryParameters: true,
		Responses: errorResponses(api.OpenAPI().Components.Schemas,
			http.StatusBadRequest, http.StatusNotFound, http.StatusConflict,
			http.StatusUnprocessableEntity, http.StatusInternalServerError),
	}

	if spec.HasBody {
		huma.Register(api, op, func(ctx context.Context, in *actionInput[In]) (*itemOutput[T], error) {
			return run(ctx, in.ID, in.Body)
		})
		return nil
	}
	huma.Register(api, op, func(ctx context.Context, in *itemInput) (*itemOutput[T], error) {
		var body In
		return run(ctx, in.ID, body)
	})
	return nil
}

// mutationWriteSet is writeSet for a MutationSpec. See binding.writeSet.
func (b *binding[T]) mutationWriteSet(spec MutationSpec) ([]*sqlb.ColumnInfo, error) {
	out := make([]*sqlb.ColumnInfo, 0, len(spec.Writes))
	for _, name := range spec.Writes {
		col := b.model.Column(name)
		switch {
		case col == nil:
			return nil, fmt.Errorf("rest: %s writes %q, which is not a column of %s",
				spec.Path, name, b.model.Type)
		case col.Expr != "":
			return nil, fmt.Errorf("rest: %s writes %q, which is computed and has no storage",
				spec.Path, name)
		}
		out = append(out, col)
	}
	return out, nil
}
