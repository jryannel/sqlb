package codegen

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/jryannel/sqlb/schema"
)

// A declared verb on the command line (ADR-0043).
//
// `taskctl tasks complete <id> --note shipped`, next to `tasks update` and
// spelled the same way. The CLI is the surface where a missing verb is least
// forgiving: there is no escape hatch short of curl, so a generated tool that
// serves CRUD and nothing else is one an operator abandons the first time they
// have to complete a task.
//
// The body properties become flags, on the create command's rules — a required
// property is a required flag, since the command can refuse before the round
// trip rather than relaying a 422.

// cliActionCommand is the constructor name for one verb's subcommand.
func cliActionCommand(r cliResource, a schema.Action) string {
	return "new" + r.goPlural + GoName(strings.ReplaceAll(a.Name, "-", "_")) + "Command"
}

// cliActionCommands emits every declared verb of one resource.
func cliActionCommands(b *bytes.Buffer, r cliResource) {
	for _, a := range r.table.Actions() {
		cliActionCommand1(b, r, a)
	}
}

// cliActionAdds emits the AddCommand lines that hang the verbs off the
// resource's command group.
func cliActionAdds(b *bytes.Buffer, r cliResource) {
	for _, a := range r.table.Actions() {
		fmt.Fprintf(b, "\tcmd.AddCommand(%s(c))\n", cliActionCommand(r, a))
	}
}

func cliActionCommand1(b *bytes.Buffer, r cliResource, a schema.Action) {
	path := a.FullPath(r.path)
	fields := a.Body

	fmt.Fprintf(b, "\n// %s is POST %s.\n", cliActionCommand(r, a), path)
	fmt.Fprintf(b, "func %s(c *Client) *cobra.Command {\n", cliActionCommand(r, a))

	if len(fields) > 0 {
		fmt.Fprintln(b, "\tvar (")
		for _, f := range fields {
			fmt.Fprintf(b, "\t\t%s %s\n", cliValueVar(f.Desc()), cliFlagType(f.Desc()))
		}
		fmt.Fprintln(b, "\t)")
	}

	use := a.Name
	if !a.IsCollection() {
		use += " <id>"
	}
	fmt.Fprintln(b, "\tcmd := &cobra.Command{")
	fmt.Fprintf(b, "\t\tUse:   %q,\n", use)
	fmt.Fprintf(b, "\t\tShort: %q,\n", actionSummary(a, r.table.LocalName()))
	fmt.Fprintf(b, "\t\tLong:  %s,\n", goRawString(cliActionLong(r, a)))
	fmt.Fprintf(b, "\t\tExample: %s,\n", goRawString(cliActionExample(r, a)))
	if a.IsCollection() {
		fmt.Fprintln(b, "\t\tArgs:  cobra.NoArgs,")
		fmt.Fprintln(b, "\t\tRunE: func(cmd *cobra.Command, _ []string) error {")
	} else {
		fmt.Fprintln(b, "\t\tArgs:  cobra.ExactArgs(1),")
		fmt.Fprintln(b, "\t\tRunE: func(cmd *cobra.Command, args []string) error {")
	}

	// A verb that declares no body sends none. The operation does not read one
	// — that is what ActionSpec.HasBody decides — and posting `{}` at it would
	// be a request shape the document does not describe.
	body := ""
	if len(fields) > 0 {
		fmt.Fprintln(b, "\t\t\tbody := map[string]any{}")
		for _, f := range fields {
			cliBodyAssignment(b, f.Desc())
		}
		body = ", Body: body"
	}

	// A collection verb answers 204, so there is nothing to print; an item verb
	// answers with the row, the same as create and update do.
	switch {
	case a.IsCollection():
		fmt.Fprintf(b, "\t\t\treturn c.run(cmd, Request{Method: http.MethodPost, Path: %q%s}, false)\n", path, body)
	default:
		_, after, _ := strings.Cut(a.Path, "{id}")
		fmt.Fprintf(b, "\t\t\treturn c.run(cmd, Request{Method: http.MethodPost, Path: itemPath(%q, args[0]) + %q%s}, false)\n",
			r.path, after, body)
	}
	fmt.Fprintln(b, "\t\t},\n\t}")

	if len(fields) > 0 {
		fmt.Fprintln(b, "\tflags := cmd.Flags()")
		for _, f := range fields {
			d := f.Desc()
			fmt.Fprintf(b, "\tflags.%sVar(&%s, %q, %s,\n\t\t%s)\n",
				cliFlagKind(d), cliValueVar(d), cliFlagName(d.Name), cliFlagZero(d),
				goRawString(cliActionUsage(d)))
			if d.Type == schema.TypeEnum && len(d.EnumValues) > 0 {
				fmt.Fprintf(b, "\tregisterCompletion(cmd, %q, %s)\n",
					cliFlagName(d.Name), goSliceLiteral(d.EnumValues))
			}
			// A property the action declares as required is a required flag:
			// refusing here costs a round trip less than relaying the 422.
			if !optionalOnCreate(d) {
				fmt.Fprintf(b, "\t_ = cmd.MarkFlagRequired(%q)\n", cliFlagName(d.Name))
			}
		}
	}
	fmt.Fprintln(b, "\treturn cmd\n}")
}

func cliActionLong(r cliResource, a schema.Action) string {
	var b strings.Builder
	fmt.Fprintf(&b, "POST %s\n\n", a.FullPath(r.path))
	if a.Description != "" {
		b.WriteString(a.Description + "\n\n")
	}
	if a.IsCollection() {
		b.WriteString("A verb on the collection: it addresses no single row, and a successful call\nwrites nothing to print.")
		return b.String()
	}
	b.WriteString("A verb on one row. The server fetches it, runs the transition, and answers\nwith the row as it now stands.")
	if len(a.Writes) > 0 {
		fmt.Fprintf(&b, "\n\nThis writes %s, and no other column.", strings.Join(a.Writes, ", "))
	}
	return b.String()
}

// cliActionExample writes one runnable invocation, filling every required flag.
func cliActionExample(r cliResource, a schema.Action) string {
	var args []string
	if !a.IsCollection() {
		args = append(args, cliIDPlaceholder(r))
	}
	for _, f := range a.Body {
		d := f.Desc()
		if optionalOnCreate(d) {
			continue
		}
		args = append(args, "--"+cliFlagName(d.Name)+" "+cliValueExample(d))
	}
	return "  " + r.line(strings.TrimSpace(a.Name+" "+strings.Join(args, " ")))
}

// cliActionUsage is one body property's flag help.
func cliActionUsage(d *schema.FieldDesc) string {
	usage := d.Comment
	if usage == "" {
		usage = "The " + d.Name + " property of the request body."
	}
	if d.Type == schema.TypeEnum && len(d.EnumValues) > 0 {
		usage += " One of: " + strings.Join(d.EnumValues, ", ") + "."
	}
	if optionalOnCreate(d) {
		usage += " Optional."
	}
	return usage
}
