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
	TypeText      Type = "text"
	TypeVarchar   Type = "varchar"
	TypeInt       Type = "int"
	TypeBigInt    Type = "bigint"
	TypeFloat     Type = "float"
	TypeNumeric   Type = "numeric"
	TypeBool      Type = "bool"
	TypeUUID      Type = "uuid"
	TypeTimestamp Type = "timestamptz"
	TypeDate      Type = "date"
	TypeTime      Type = "time"
	TypeJSON      Type = "jsonb"
	TypeBytes     Type = "bytea"
	TypeEnum      Type = "enum"
)

// GoType returns the Go type that codegen emits for a non-null column of this
// type. Nullable columns are emitted as pointers to it.
func (t Type) GoType() string {
	switch t {
	case TypeText, TypeVarchar, TypeUUID, TypeEnum:
		return "string"
	case TypeInt:
		return "int32"
	case TypeBigInt:
		return "int64"
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
	}
	return "any"
}

// Action is a foreign key referential action.
type Action string

const (
	NoAction   Action = "NO ACTION"
	Restrict   Action = "RESTRICT"
	Cascade    Action = "CASCADE"
	SetNull    Action = "SET NULL"
	SetDefault Action = "SET DEFAULT"
)

// Default describes a column default. Raw is emitted verbatim into DDL; Value
// is emitted as a literal.
type Default struct {
	Raw   string
	Value any
}

// Now defaults the column to the statement timestamp.
func Now() *Default { return &Default{Raw: "now()"} }

// GenUUIDv7 defaults the column to a freshly generated UUIDv7. Requires the
// pg_uuidv7 extension, or use GenUUIDv4 for a stock Postgres install.
func GenUUIDv7() *Default { return &Default{Raw: "uuid_generate_v7()"} }

// GenUUIDv4 defaults the column to a random UUID using pgcrypto.
func GenUUIDv4() *Default { return &Default{Raw: "gen_random_uuid()"} }

// Expr defaults the column to an arbitrary SQL expression.
func Expr(sql string) *Default { return &Default{Raw: sql} }

// Value defaults the column to a literal.
func Value(v any) *Default { return &Default{Value: v} }
