package codegen

import (
	"regexp"
	"sort"
	"strings"
)

// Emitting the client runtime once, beside the per-schema client, rather than
// inside it.
//
// The runtime — the response envelopes, the problem document, the transport
// signature and the filter encoder — is derived from nothing schema-specific.
// One project never noticed: the file was self-contained and that was a
// feature. A second module in the same application is where it breaks, and it
// breaks differently per language.
//
// TypeScript is structurally typed, so two copies of Page interoperate and the
// cost is only that both ship. This is that fix (issue #110).
//
// Go reached this shape first and for a different reason — the transport-only
// client became its own package in #97, so a sync job could take the encoder
// without the command tree. TypeScript now agrees with it.
//
// # Dart is not done here, and the reason is not effort
//
// Dart is nominally typed, so its duplication is a defect rather than a cost:
// two Page<T> are two unrelated classes. But its runtime's contract with the
// generated client is largely *private* — Row's _str/_int/_one/_many protocol
// that every row view inherits, Cond._encode, the top-level _get/_row/_page
// helpers — and Dart privacy is per library, so none of it survives the file
// boundary. Splitting it needs that protocol made public and documented, which
// changes what a generated client exposes and is a decision rather than a
// refactor. Attempted and reverted; the finding is on #110.
//
// # Why the import list is computed rather than written down
//
// The generated client must import exactly what it uses. TypeScript is checked
// under `noUnusedLocals` and Dart under `--fatal-infos`, so an import the body
// does not reference is a build failure, and the set genuinely varies: a schema
// with no array column never mentions ArrayCond, and one with no list operation
// never mentions ListQuery. A hand-maintained list would be wrong for some
// schema and right for the fixture.
//
// So the runtime's exports are parsed out of the runtime source itself, and the
// emitted body is scanned for them. Nothing to keep in step, and a name added
// to the runtime is picked up by the next build rather than by whoever
// remembers.

// tsExportPattern matches an exported declaration in the TypeScript runtime,
// capturing whether it is a type — which `verbatimModuleSyntax` requires be
// imported with `import type` — and its name.
var tsExportPattern = regexp.MustCompile(`(?m)^export (type|interface|const|function|class) ([A-Za-z_][A-Za-z0-9_]*)`)

// runtimeSymbol is one name the runtime offers.
type runtimeSymbol struct {
	name   string
	isType bool
}

func tsRuntimeSymbols() []runtimeSymbol {
	var out []runtimeSymbol
	for _, m := range tsExportPattern.FindAllStringSubmatch(tsRuntime, -1) {
		kind, name := m[1], m[2]
		out = append(out, runtimeSymbol{
			name: name,
			// A class is a value and a type at once; imported as a value, it
			// serves both.
			isType: kind == "type" || kind == "interface",
		})
	}
	return out
}

// usesSymbol reports whether body references name as a whole identifier.
//
// Word-boundary rather than substring, so Page does not match PageParams and
// Cond does not match ArrayCond — both of which are real names in this runtime,
// and either false positive would emit an import the body does not use and fail
// the build it was meant to pass.
func usesSymbol(body, name string) bool {
	for i := 0; i+len(name) <= len(body); i++ {
		if body[i:i+len(name)] != name {
			continue
		}
		if i > 0 && isIdentRune(body[i-1]) {
			continue
		}
		if j := i + len(name); j < len(body) && isIdentRune(body[j]) {
			continue
		}
		return true
	}
	return false
}

func isIdentRune(c byte) bool {
	return c == '_' || c == '$' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// tsPrivatePattern matches a top-level declaration in the runtime that is *not*
// exported, which is what a client body must not reach for.
var tsPrivatePattern = regexp.MustCompile(`(?m)^(type|interface|const|function|class) ([A-Za-z_][A-Za-z0-9_]*)`)

// tsUnexportedUse reports a runtime helper the client body calls but the
// runtime keeps to itself.
//
// Before the split this could not happen — one file, so every helper was in
// scope — and the first thing the split broke was exactly this: itemPath was
// module-local, the client called it, and the failure arrived as ten identical
// "Cannot find name" errors from tsc rather than from the generator that caused
// them. Checked here so the next one is a sentence instead.
func tsUnexportedUse(body string) string {
	for _, m := range tsPrivatePattern.FindAllStringSubmatch(tsRuntime, -1) {
		if usesSymbol(body, m[2]) {
			return m[2]
		}
	}
	return ""
}

// tsRuntimeImports renders the import statements a client body needs, plus the
// re-export that keeps existing call sites working.
//
// The re-export is what makes this change invisible to a project that has one
// module: code doing `import type { Page } from './client.gen'` keeps
// compiling, because client.gen still offers Page — it just no longer declares
// it. Without that, splitting the file would break every consumer to fix a
// problem only multi-module consumers have.
func tsRuntimeImports(body, from string) string {
	var types, values []string
	for _, s := range tsRuntimeSymbols() {
		if !usesSymbol(body, s.name) {
			continue
		}
		if s.isType {
			types = append(types, s.name)
		} else {
			values = append(values, s.name)
		}
	}
	sort.Strings(types)
	sort.Strings(values)

	var b strings.Builder
	if len(types) > 0 {
		b.WriteString("import type { " + strings.Join(types, ", ") + " } from '" + from + "';\n")
	}
	if len(values) > 0 {
		b.WriteString("import { " + strings.Join(values, ", ") + " } from '" + from + "';\n")
	}
	// Unconditional, because it is the compatibility promise rather than a
	// consequence of what this schema happens to use.
	b.WriteString("export * from '" + from + "';\n")
	return b.String()
}
