// Package schema is the declarative schema DSL for sqlb.
//
// A schema is written as ordinary Go values, which makes it the single source
// of truth for migrations, models, REST handlers and the OpenAPI document:
//
//	var User = schema.Table("users",
//	    schema.UUIDv7("id").PrimaryKey(),
//	    schema.Text("email").Unique().Searchable(),
//	    schema.Int("age").Nullable().Filterable(),
//	    schema.Ref("org", Org).OnDelete(schema.Cascade),
//	    schema.Timestamps(),
//	).Expose(schema.REST{Path: "/users", Ops: schema.CRUD | schema.List})
//
// Capabilities such as Filterable and Sortable are opt-in per column. A column
// that does not declare a capability can never be reached through it from the
// REST layer, which is what separates sqlb from exposing the database directly.
package schema

// Type is the logical column type. Dialects map these onto concrete SQL types.
type Type string

const (
	TypeText     Type = "text"
	TypeVarchar  Type = "varchar"
	TypeSmallInt Type = "smallint"
	TypeInt      Type = "int"
	TypeBigInt   Type = "bigint"
	// TypeReal is the 4-byte float. It is a distinct type from TypeFloat for
	// the same reason TypeSmallInt is distinct from TypeInt: importing it as
	// the wider one would make every later diff propose widening a column the
	// database is content with (issues #114, #120).
	TypeReal    Type = "real"
	TypeFloat   Type = "float"
	TypeNumeric Type = "numeric"
	TypeBool      Type = "bool"
	TypeUUID      Type = "uuid"
	TypeTimestamp Type = "timestamptz"
	TypeDate      Type = "date"
	TypeTime      Type = "time"
	TypeJSON      Type = "jsonb"
	TypeBytes     Type = "bytea"
	TypeEnum      Type = "enum"
	// TypeVector is a pgvector embedding. Unlike every other type here it
	// carries a parameter that is part of the type rather than a constraint on
	// it — a vector(768) and a vector(1536) are different types to Postgres,
	// and the dimension is FieldDesc.Dim (ADR-0026).
	TypeVector Type = "vector"
)

// Types is every logical column type, in declaration order.
//
// It exists so that a consumer which must handle all of them can be checked
// against the list rather than against its author's memory. That is not
// hypothetical: `introspect` imported a vector column and `RenderSchema` could
// not write one back out, so the bootstrap that turns a 69-table database into
// 69 declarations to review failed on the one type the rest of the toolchain
// already handled (issue #53). A test walks this list now.
func Types() []Type {
	return []Type{
		TypeText, TypeVarchar, TypeSmallInt, TypeInt, TypeBigInt, TypeReal,
		TypeFloat, TypeNumeric, TypeBool, TypeUUID, TypeTimestamp, TypeDate,
		TypeTime, TypeJSON, TypeBytes, TypeEnum, TypeVector,
	}
}

// GoType returns the Go type that codegen emits for a non-null column of this
// type. Nullable columns are emitted as pointers to it.
func (t Type) GoType() string {
	switch t {
	case TypeText, TypeVarchar, TypeUUID, TypeEnum:
		return "string"
	case TypeSmallInt:
		// int16 rather than int32, so a Describe over existing sqlc output
		// matches without a type override: sqlc already emits int16 for
		// smallint (issue #114).
		return "int16"
	case TypeInt:
		return "int32"
	case TypeBigInt:
		return "int64"
	case TypeReal:
		return "float32"
	case TypeFloat, TypeNumeric:
		return "float64"
	case TypeBool:
		return "bool"
	case TypeTimestamp, TypeDate, TypeTime:
		return "time.Time"
	case TypeJSON:
		return "json.RawMessage"
	case TypeBytes:
		return "[]byte"
	case TypeVector:
		return "sqlb.Vector"
	}
	return "any"
}

// RefAction is a foreign key referential action.
//
// It was spelled Action until a table needed that noun for a domain verb
// ([TableDef.Action], ADR-0043). Two meanings of "action" in one package is
// the kind of ambiguity that outlives everyone who could explain it, and this
// is the side almost nobody writes by name — the constants below carry the
// meaning at every call site.
type RefAction string

const (
	NoAction   RefAction = "NO ACTION"
	Restrict   RefAction = "RESTRICT"
	Cascade    RefAction = "CASCADE"
	SetNull    RefAction = "SET NULL"
	SetDefault RefAction = "SET DEFAULT"
)

// Default describes a column default. Raw is emitted verbatim into DDL; Value
// is emitted as a literal.
type Default struct {
	Raw   string
	Value any
}

// Now defaults the column to the statement timestamp.
func Now() *Default { return &Default{Raw: "now()"} }

// GenUUIDv7 defaults the column to a freshly generated UUIDv7.
//
// How this renders depends on the Postgres it is generated for. By default it
// emits uuid_generate_v7(), which needs the pg_uuidv7 extension — so the
// generated DDL does not apply to a stock install. Postgres 18 has uuidv7()
// built in, and migrate.MinPostgres(18) emits that instead, which needs
// nothing. On an older server without the extension, use GenUUIDv4.
func GenUUIDv7() *Default { return &Default{Raw: "uuid_generate_v7()"} }

// GenUUIDv4 defaults the column to a random UUID using pgcrypto.
func GenUUIDv4() *Default { return &Default{Raw: "gen_random_uuid()"} }

// Expr defaults the column to an arbitrary SQL expression.
func Expr(sql string) *Default { return &Default{Raw: sql} }

// Value defaults the column to a literal.
func Value(v any) *Default { return &Default{Value: v} }
