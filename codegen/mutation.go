package codegen

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/jryannel/sqlb/schema"
)

// Emitting a declared mutation (ADR-0057).
//
// A mutation is Action's item form under its own name — same envelope, same
// generated shapes — so this file mirrors action.go's item-form path rather
// than sharing code with it: the two are declared through different schema
// types (Mutation, not Action), and keeping their renderers separate is what
// lets one evolve — Query.Reads already diverged from Action.Touches — without
// the other's shape leaking through a shared helper.

// mutationDef pairs a mutation with the table that declared it.
type mutationDef struct {
	table    *schema.TableDef
	mutation schema.Mutation
}

// mutationsOf collects the declared mutations of the exposed tables, in table
// order and then declaration order.
func mutationsOf(exposed []*schema.TableDef) []mutationDef {
	var out []mutationDef
	for _, t := range exposed {
		for _, m := range t.Mutations() {
			out = append(out, mutationDef{table: t, mutation: m})
		}
	}
	return out
}

// goName is the mutation's exported identifier: the verb, then the type it
// acts on. "complete" on tasks gives CompleteTask, the same convention
// actionDef.goName uses and for the same reason — one name a client, the
// Mutations field and the input type all share.
func (d mutationDef) goName() string {
	verb := GoName(strings.ReplaceAll(d.mutation.Name, "-", "_"))
	return verb + TypeName(d.table.LocalName())
}

// inputName is the generated request body type, emitted even when the
// mutation declares no properties — see actionDef.inputName.
func (d mutationDef) inputName() string { return d.goName() + "Input" }

// fullPath is the route: the resource path with the mutation's own appended.
func (d mutationDef) fullPath() string { return d.mutation.FullPath(d.table.Rest().Path) }

// summary defaults to "Complete a task" the way an item action's does — a
// mutation is always row-scoped, so there is no collection form to branch on.
func (d mutationDef) summary() string {
	if s := d.mutation.Summary; s != "" {
		return s
	}
	words := strings.ReplaceAll(d.mutation.Name, "-", " ")
	words = strings.ToUpper(words[:1]) + words[1:]
	return words + " a " + Singular(d.table.LocalName())
}

// renderMutationInput writes one mutation's request body type. Identical
// rules to an action's — see renderActionInput.
func renderMutationInput(b *bytes.Buffer, d mutationDef) {
	name := d.inputName()

	fmt.Fprintf(b, "\n// %s is the request body for %s.\n", name, d.fullPath())
	if len(d.mutation.Body) == 0 {
		fmt.Fprintf(b, "//\n// The mutation declares no properties. The type is emitted anyway, so that\n")
		fmt.Fprintf(b, "// declaring the first one later does not change the signature of %s.\n", d.goName())
		fmt.Fprintf(b, "type %s struct{}\n", name)
		return
	}
	fmt.Fprintf(b, "//\n// A property with a default or one that may be null is a pointer, so that\n")
	fmt.Fprintf(b, "// leaving it out is distinguishable from sending its zero value.\n")
	fmt.Fprintf(b, "type %s struct {\n", name)
	for _, f := range d.mutation.Body {
		desc := f.Desc()
		fmt.Fprintf(b, "\t%s %s `json:\"%s%s\"%s`", GoName(desc.Name), actionBodyType(desc), desc.Name,
			omitEmpty(optionalOnCreate(desc)), enumTag(desc))
		if c := desc.Comment; c != "" {
			fmt.Fprintf(b, " // %s", c)
		}
		fmt.Fprintln(b)
	}
	fmt.Fprintln(b, "}")
}

// mutationBodyImports records the packages a mutation's body names. Mirrors
// actionBodyImports.
func mutationBodyImports(imports map[string]bool, defs []mutationDef) {
	for _, d := range defs {
		for _, f := range d.mutation.Body {
			switch goType := f.Desc().GoType(); {
			case strings.Contains(goType, "time.Time"):
				imports["time"] = true
			case strings.Contains(goType, "json.RawMessage"):
				imports["encoding/json"] = true
			}
		}
	}
}

// renderMutations writes the struct that carries the application's row-scoped
// verbs — Actions' sibling, one field per declared mutation, always in the
// row-scoped shape since a Mutation has no collection form.
func renderMutations(b *bytes.Buffer, defs []mutationDef) {
	fmt.Fprintf(b, "\n// Mutations carries the domain funcs the declared mutations call.\n")
	fmt.Fprintf(b, "//\n// Each field is the verb of one mutation, and the envelope around it —\n")
	fmt.Fprintf(b, "// the id, the scoped fetch, the lock, the transaction, the write set and\n")
	fmt.Fprintf(b, "// the response — is generated, byte for byte what an item Action's is.\n")
	fmt.Fprintf(b, "//\n// A field left nil is refused when Register mounts the resource, not by\n")
	fmt.Fprintf(b, "// the request that would have called it.\n")
	fmt.Fprintln(b, "type Mutations struct {")
	for i, d := range defs {
		if i > 0 {
			fmt.Fprintln(b)
		}
		fmt.Fprintf(b, "\t// %s runs POST %s.\n", d.goName(), d.fullPath())
		if desc := d.mutation.Description; desc != "" {
			fmt.Fprintf(b, "\t//\n\t// %s\n", desc)
		}
		if w := d.mutation.Writes; len(w) > 0 {
			fmt.Fprintf(b, "\t//\n\t// The envelope persists %s off this row afterwards, and nothing\n"+
				"\t// else — which bounds the envelope and not the func: the transaction\n"+
				"\t// is yours through sqlb.TxFrom, and statements issued there take\n"+
				"\t// their own locks, in an order this code owns.\n", quoteList(w))
		}
		if tt := d.mutation.Touches; len(tt) > 0 {
			fmt.Fprintf(b, "\t//\n\t// Declared reach beyond that row: %s. Nothing checks it; it is\n"+
				"\t// what the route tells `sqlb impact`, the OpenAPI document and the\n"+
				"\t// CLI's --help, so a change here belongs in the schema.\n", quoteList(tt))
		}
		fmt.Fprintf(b, "\t%s func(context.Context, *%s, %s) error\n",
			d.goName(), TypeName(d.table.LocalName()), d.inputName())
	}
	fmt.Fprintln(b, "}")
}

// renderMutationCalls writes the registrations for one table's mutations.
func renderMutationCalls(b *bytes.Buffer, optsVar string, defs []mutationDef) {
	for _, d := range defs {
		typeName := TypeName(d.table.LocalName())
		fmt.Fprintf(b, "\tif err := rest.Mutation[%s, %s](api, db, %s, rest.MutationSpec{\n",
			typeName, d.inputName(), optsVar)
		fmt.Fprintf(b, "\t\tName:  %q,\n", d.mutation.Name)
		fmt.Fprintf(b, "\t\tPath:  %q,\n", d.fullPath())
		fmt.Fprintf(b, "\t\tField: %q,\n", d.goName())
		fmt.Fprintf(b, "\t\tSummary: %q,\n", d.summary())
		if s := d.mutation.Description; s != "" {
			fmt.Fprintf(b, "\t\tDescription: %q,\n", s)
		}
		if w := d.mutation.Writes; len(w) > 0 {
			fmt.Fprintf(b, "\t\tWrites: []string{%s},\n", quotedList(w))
		}
		if tt := d.mutation.Touches; len(tt) > 0 {
			fmt.Fprintf(b, "\t\tTouches: []string{%s},\n", quotedList(tt))
		}
		if len(d.mutation.Body) > 0 {
			fmt.Fprintf(b, "\t\tHasBody: true,\n")
		}
		fmt.Fprintf(b, "\t}, mutations.%s); err != nil {\n\t\treturn err\n\t}\n", d.goName())
	}
}
