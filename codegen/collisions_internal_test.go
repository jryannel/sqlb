package codegen

import "testing"

// The declaration-space rule, tested directly because no schema exercises it:
// the emitter happens never to name a type and a value the same, so a rule
// collapsing the two spaces passes the whole suite while refusing valid
// TypeScript the day some emitter change does.
func TestTSDuplicateDeclarationRespectsDeclarationSpaces(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string // the identifier reported, empty for none
	}{{
		name: "an interface and a type are one space",
		src:  "export interface BoardColumn {}\nexport type BoardColumn = 'id';\n",
		want: "BoardColumn",
	}, {
		name: "two interfaces are a redeclaration this generator should not write",
		src:  "export interface Board {}\nexport interface Board {}\n",
		want: "Board",
	}, {
		name: "a type and a value are not",
		src:  "export type Board = { id: string };\nexport const Board = { id: 'id' };\n",
		want: "",
	}, {
		name: "a function and a type are not",
		src:  "export type listBoards = never;\nexport function listBoards() {}\n",
		want: "",
	}, {
		name: "an indented member is not a declaration",
		src:  "export interface Board {\n  type: string;\n  interface: string;\n}\n",
		want: "",
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, first, second := tsDuplicateDeclaration(c.src)
			if got != c.want {
				t.Fatalf("tsDuplicateDeclaration = %q (%q, %q), want %q", got, first, second, c.want)
			}
		})
	}
}
