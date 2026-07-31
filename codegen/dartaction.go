package codegen

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/jryannel/sqlb/schema"
)

// A declared verb in the Dart client (ADR-0043).
//
// Same argument as the TypeScript side, with one difference that matters on a
// phone: Dart's named parameters mean an action's body class can grow an
// optional property without moving any call site, so the input class is emitted
// only when there is something to put in it.

// dartActionBase is the stem of every name one verb generates: CompleteTask.
func dartActionBase(base string, a schema.Action) string {
	return GoName(strings.ReplaceAll(a.Name, "-", "_")) + base
}

// dartActionInput is the request body class.
func dartActionInput(base string, a schema.Action) string {
	return dartActionBase(base, a) + "Input"
}

// dartActionMethod is the transport function: completeTask, beside createTask.
func dartActionMethod(base string, a schema.Action) string {
	return lowerFirstRune(dartActionBase(base, a))
}

// dartActionBodies emits one body class per verb that declares properties.
func dartActionBodies(b *bytes.Buffer, t *schema.TableDef, base string) {
	if t.Rest() == nil {
		return
	}
	for _, a := range t.Actions() {
		if len(a.Body) == 0 {
			continue
		}
		name := dartActionInput(base, a)
		fmt.Fprintln(b)
		dartDoc(b, "", fmt.Sprintf("The request body for POST %s.", a.FullPath(t.Rest().Path)))
		fmt.Fprintf(b, "class %s {\n", name)
		dartDoc(b, "  ", "Builds a request body. A property with no default here is one the\naction declares as required.")

		var params []string
		for _, f := range a.Body {
			d := f.Desc()
			required := ""
			if !optionalOnCreate(d) {
				required = "required "
			}
			params = append(params, required+"this."+dartMember(d.Name))
		}
		dartNamedCtor(b, name, params)

		for _, f := range a.Body {
			d := f.Desc()
			fmt.Fprintln(b)
			dartDoc(b, "  ", dartColumnDoc(d, fmt.Sprintf("The %s property of the request body.", d.Name)))
			fmt.Fprintf(b, "  final %s %s;\n", dartBodyType(base, d, true), dartMember(d.Name))
		}

		fmt.Fprintln(b)
		dartDoc(b, "  ", "The JSON body. Absent properties are the ones left unset.")
		var entries []string
		for _, f := range a.Body {
			d := f.Desc()
			member := dartMember(d.Name)
			entry := fmt.Sprintf("%s: _wire(%s)", dartString(d.Name), member)
			if optionalOnCreate(d) {
				entry = fmt.Sprintf("if (%s != null) %s", member, entry)
			}
			entries = append(entries, entry)
		}
		dartMapBody(b, "Map<String, dynamic> toJson()", entries)
		fmt.Fprintln(b, "}")
	}
}

// dartActionMethods emits the transport function for each declared verb.
func dartActionMethods(b *bytes.Buffer, r dartResource) {
	for _, a := range r.table.Actions() {
		summary := a.Summary
		if summary == "" {
			summary = actionSummary(a, r.table.LocalName())
		}
		fmt.Fprintln(b)
		dartDoc(b, "", fmt.Sprintf("POST %s — %s.", a.FullPath(r.path),
			strings.ToLower(summary[:1])+summary[1:]))

		method := dartActionMethod(r.base, a)
		hasBody := len(a.Body) > 0

		// A collection verb answers 204, so there is nothing to decode; an item
		// verb answers with the row the envelope left behind.
		if a.IsCollection() {
			fmt.Fprintf(b, "Future<void> %s(\n", method)
		} else {
			fmt.Fprintf(b, "Future<%s> %s(\n", r.row, method)
		}
		// The brace opening the named-parameter list goes on whichever
		// positional parameter turns out to be last, and a collection verb with
		// no body has none — so it opens on the transport itself.
		switch {
		case hasBody && !a.IsCollection():
			fmt.Fprintln(b, "  Transport request,")
			fmt.Fprintln(b, "  Object id,")
			fmt.Fprintf(b, "  %s body, {\n", dartActionInput(r.base, a))
		case hasBody:
			fmt.Fprintln(b, "  Transport request,")
			fmt.Fprintf(b, "  %s body, {\n", dartActionInput(r.base, a))
		case !a.IsCollection():
			fmt.Fprintln(b, "  Transport request,")
			fmt.Fprintln(b, "  Object id, {")
		default:
			fmt.Fprintln(b, "  Transport request, {")
		}
		fmt.Fprintln(b, "  Object? cancel,")
		fmt.Fprintln(b, "}) async {")

		if a.IsCollection() {
			fmt.Fprintf(b, "  const path = %s;\n", dartString(a.FullPath(r.path)))
		} else {
			// Through a local rather than concatenated inline: Dart's
			// prefer_interpolation_to_compose_strings reports the `+` form, and
			// `dart analyze --fatal-infos` is a gate this example runs.
			_, after, _ := strings.Cut(a.Path, "{id}")
			fmt.Fprintf(b, "  final item = _itemPath(%s, id);\n", dartString(r.path))
			if after == "" {
				fmt.Fprintln(b, "  final path = item;")
			} else {
				fmt.Fprintf(b, "  final path = '$item%s';\n", after)
			}
		}

		payload := "const <String, dynamic>{}"
		if hasBody {
			payload = "body.toJson()"
		}
		if a.IsCollection() {
			fmt.Fprintf(b, "  await request(_post(path, %s, cancel));\n}\n", payload)
			continue
		}
		fmt.Fprintf(b, "  final json = await request(_post(path, %s, cancel));\n", payload)
		fmt.Fprintf(b, "  return _row(json, %s.fromJson);\n}\n", r.row)
	}
}
