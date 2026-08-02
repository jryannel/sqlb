// Package codegen renders a schema declaration into Go source.
//
// It is driven from a small program in the target project rather than by a CLI
// that compiles your schema behind your back, because the schema is ordinary Go
// and the simplest way to read it is to import it:
//
//	//go:generate go run ./gen
//
//	// gen/main.go
//	package main
//
//	import (
//	    _ "myapp/billing/schema"   // registers its tables
//	    "github.com/jryannel/sqlb/codegen"
//	    "github.com/jryannel/sqlb/schema"
//	)
//
//	func main() {
//	    codegen.Must(codegen.Generate(codegen.Options{
//	        Registry: schema.DefaultRegistry(),
//	        Dir:      "billing",
//	        Package:  "billing",
//	    }))
//	}
//
// Output is deterministic: tables are sorted, columns keep declaration order,
// and every file is run through go/format, so a generator bug that produces
// invalid Go fails here rather than at the consumer's next build.
//
// Three further artefacts are opt-in, and all are emitted into the repository
// that consumes them rather than published: TSDir writes a typed TypeScript
// client (ADR-0028), DartDir writes a typed Dart client for a Flutter app
// (ADR-0031), and CLIDir writes a cobra command-line client (ADR-0029). Each
// belongs to a toolchain this module does not have, which is why none is
// emitted unless asked for, and why asking costs the consuming repository a
// gate rather than this one.
package codegen

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/jryannel/sqlb/schema"
)

// Options configures a generation run.
type Options struct {
	// Registry supplies the tables. Required.
	Registry *schema.Registry
	// Dir is the output directory. Required.
	Dir string
	// Package is the package clause for generated Go. Required.
	Package string

	// ModelsFile, ColumnsFile, ManifestFile and RestFile override the default
	// names. Set one to "-" to skip that artefact.
	//
	// RestFile is written only when the schema exposes at least one table, so a
	// package with no REST surface does not acquire a dependency on huma.
	ModelsFile   string
	ColumnsFile  string
	ManifestFile string
	RestFile     string

	// TSDir emits the TypeScript client, into a directory relative to Dir —
	// "web/src/api" in a repository whose frontend lives beside its server.
	// Empty means no client is emitted at all, which is the right default for a
	// project that has no TypeScript consumer.
	//
	// Three files land there. The runtime and the client are dependency-free;
	// the queries file takes @tanstack/react-query as a peer dependency, so a
	// project that does not use it sets TSQueriesFile to "-" and keeps the
	// rest.
	TSDir         string
	TSClientFile  string
	TSQueriesFile string

	// TSRuntimeFile names the file holding the part of the client that does
	// not depend on the schema — the envelopes, the problem document, the
	// transport signature and the filter encoder. Defaults to runtime.gen.ts.
	//
	// It is a separate file because a second module in the same application
	// otherwise ships a second copy of all of it, and asks the application to
	// wire one Transport per module (#110). Point two projects at one path and
	// they share it: the content is derived from nothing schema-specific, so
	// the second writer produces the same bytes and `check` stays meaningful.
	TSRuntimeFile string

	// CLIDir emits a cobra command-line client, into a directory relative to
	// Dir — "cli" in a repository whose binary lives beside its server. Empty
	// means no CLI is emitted, which is the right default for a project that
	// has no use for one.
	//
	// The emitted package depends on github.com/spf13/cobra and nothing else
	// beyond the standard library. It does not import sqlb or the generated
	// models: it speaks to the API over HTTP, so it holds no database
	// credential and needs no build tag to keep one out.
	//
	// CLIName is the binary's name, which is what appears in usage lines and,
	// upper-cased, as the prefix of the environment variables the root command
	// reads: "taskctl" gives TASKCTL_BASE_URL and TASKCTL_TOKEN. It defaults to
	// Package.
	CLIDir     string
	CLIPackage string
	CLIName    string
	CLIFile    string

	// ClientDir emits the transport-only Go client — Request, Transport,
	// Client, Do, Run and the typed problem document — into a directory
	// relative to Dir. The emitted package imports the standard library and
	// nothing else.
	//
	// It is a separate package from the CLI because it is a separate artefact.
	// A sync job, a server-to-server caller, or an admin tool that already has
	// a command tree of its own wants the typed encoder and not a command-line
	// framework, and while the two shared a package it could not have one
	// without the other (#97).
	//
	// Setting CLIDir and leaving this empty emits the client into a "client"
	// subdirectory of CLIDir, because the command tree has to import it from
	// somewhere. Setting this and leaving CLIDir empty emits the client alone,
	// which is the server-to-server case.
	ClientDir     string
	ClientPackage string
	ClientFile    string

	// ClientImportPath is the path the generated CLI imports the generated
	// client under. Empty derives it: the module path out of the nearest
	// go.mod, joined with Dir and the client's directory.
	//
	// Deriving it is right for a repository generating into itself, which is
	// every project using sqlb generate. It cannot be right for a caller whose
	// Dir is an absolute path, or who generates into a module it is not inside
	// — so the derivation is a default rather than the mechanism.
	ClientImportPath string

	// DartDir emits a typed Dart client, into a directory relative to Dir —
	// "mobile/lib/api" in a repository whose Flutter app lives beside its
	// server. Empty means no client is emitted, which is the right default for
	// a project that has no Dart consumer.
	//
	// Two files land there — the client and the runtime library it exports —
	// and neither imports anything: not a pub package, not even dart:io. There
	// is no framework layer to make optional, because the mobile ecosystem has
	// no equivalent of TanStack Query to bind to — the cursor pager it emits
	// instead is plain Dart (ADR-0031).
	DartDir  string
	DartFile string

	// DartRuntimeFile names the shared Dart library, defaulting to
	// runtime.gen.dart. It holds the response envelopes, the problem document
	// and the transport signature — the types an application names when it
	// writes one pager or wires one transport across two modules (#110).
	DartRuntimeFile string

	// Types replaces the Go type emitted for the columns each override
	// matches — the sqlc `overrides:` equivalent, and the reason a codebase
	// whose ids are uuid.UUID rather than string can generate its models
	// rather than describing hand-written ones.
	//
	// An override reaches the models, the typed column facade, the REST bodies
	// and the manifest, and reaches nothing else. It does not change the SQL
	// type, and it does not change the wire: the TypeScript and Dart clients,
	// the CLI and the OpenAPI document all map from the schema type, so an
	// override is invisible to them. ADR-0035 records why that split is the
	// load-bearing part.
	Types []TypeOverride
}

func (o Options) modelsFile() string   { return orDefault(o.ModelsFile, "models_gen.go") }
func (o Options) columnsFile() string  { return orDefault(o.ColumnsFile, "columns_gen.go") }
func (o Options) manifestFile() string { return orDefault(o.ManifestFile, "sqlb.json") }
func (o Options) restFile() string     { return orDefault(o.RestFile, "rest_gen.go") }

func (o Options) tsClientFile() string  { return orDefault(o.TSClientFile, "client.gen.ts") }
func (o Options) tsQueriesFile() string { return orDefault(o.TSQueriesFile, "queries.gen.ts") }
func (o Options) tsRuntimeFile() string { return orDefault(o.TSRuntimeFile, "runtime.gen.ts") }

// tsRuntimeImport is how the client names the runtime in an import: the file,
// relative to itself, by its real name.
//
// Relative and inside the directory the generator already owns, which is what
// makes this need no configuration in the ordinary case — unlike Go, where the
// same split needed ClientImportPath because a Go import is a module path
// rather than a file path.
//
// # Why the extension is written
//
// The other generated import — queries.gen.ts naming client.gen — omits it,
// and gets away with it because that file is only ever typechecked. This one is
// in a file that *runs*: the client is imported directly by tests under
// `node --test` with type stripping, and Node's resolver needs a real path.
//
// Omitting it would be a bundler assumption, and "no bundler assumption" is a
// property this client claims in its own header. It needs
// `allowImportingTsExtensions` in tsconfig, which a project consuming the
// client as source rather than compiling it already has.
func (o Options) tsRuntimeImport() string {
	return "./" + o.tsRuntimeFile()
}

func (o Options) dartFile() string { return orDefault(o.DartFile, "client.gen.dart") }

func (o Options) dartRuntimeFile() string {
	return orDefault(o.DartRuntimeFile, "runtime.gen.dart")
}

func (o Options) cliFile() string    { return orDefault(o.CLIFile, "cli_gen.go") }
func (o Options) clientFile() string { return orDefault(o.ClientFile, "client_gen.go") }

// clientDir is where the client package lands: ClientDir when set, and a
// "client" subdirectory of the CLI otherwise, so that setting CLIDir alone
// still produces something the command tree can import.
func (o Options) clientDir() string {
	if o.ClientDir != "" {
		return o.ClientDir
	}
	if o.CLIDir != "" {
		return filepath.Join(o.CLIDir, "client")
	}
	return ""
}

// clientPackage is the package clause of the emitted client, defaulting to the
// last element of its directory, as cliPackage does.
func (o Options) clientPackage() string {
	if o.ClientPackage != "" {
		return o.ClientPackage
	}
	return filepath.Base(o.clientDir())
}

// clientImportPath is the path the CLI imports the client package under.
//
// It needs the module path, which is the one thing about a generated import
// that cannot be derived from the schema. Reading go.mod is what keeps it off
// the Options struct for the common case; Module is the override for a caller
// generating outside a module root.
func (o Options) clientImportPath() (string, error) {
	if o.ClientImportPath != "" {
		return o.ClientImportPath, nil
	}
	if filepath.IsAbs(o.Dir) {
		return "", fmt.Errorf(
			"codegen: Options.Dir %q is absolute, so the generated CLI's import of the generated client "+
				"cannot be derived from the module path; set Options.ClientImportPath", o.Dir)
	}
	mod, err := moduleFromGoMod()
	if err != nil {
		return "", fmt.Errorf(
			"codegen: the generated CLI imports the generated client, which needs the module path: %w; "+
				"set Options.ClientImportPath", err)
	}
	return path.Join(mod, filepath.ToSlash(o.Dir), filepath.ToSlash(o.clientDir())), nil
}

// moduleFromGoMod reads the module path out of the nearest go.mod, walking up
// from the working directory.
//
// Walking rather than reading "./go.mod" because a caller writing its own
// generator runs it from wherever it likes, and the module root is the one
// place the answer is. No dependency on golang.org/x/mod for one line of it.
func moduleFromGoMod() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		name := filepath.Join(dir, "go.mod")
		if _, err := os.Stat(name); err == nil {
			return readModulePath(name)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod found above %s", mustGetwd())
		}
		dir = parent
	}
}

func mustGetwd() string {
	dir, err := os.Getwd()
	if err != nil {
		return "the working directory"
	}
	return dir
}

func readModulePath(name string) (string, error) {
	src, err := os.ReadFile(name)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(src), "\n") {
		if rest, found := strings.CutPrefix(strings.TrimSpace(line), "module"); found {
			if mod := strings.TrimSpace(rest); mod != "" {
				return mod, nil
			}
		}
	}
	return "", fmt.Errorf("%s declares no module path", name)
}
func (o Options) cliName() string { return orDefault(o.CLIName, o.Package) }

// cliPackage is the package clause of the emitted CLI. It defaults to the last
// element of CLIDir, which is what a reader would guess from the import path.
//
// A directory name that is not an identifier is refused rather than repaired.
// Quietly turning "api-client" into "apiclient" would emit a package under a
// name nothing in the project mentions, and the import that failed to compile
// would be the first anyone heard of it.
func (o Options) cliPackage() string {
	if o.CLIPackage != "" {
		return o.CLIPackage
	}
	return filepath.Base(o.CLIDir)
}

// tsClientImport is the specifier the queries file imports the client by: a
// sibling module, named without its extension, which is what a bundler
// resolver expects.
func (o Options) tsClientImport() string {
	return "./" + strings.TrimSuffix(o.tsClientFile(), ".ts")
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func (o Options) validate() error {
	switch {
	case o.Registry == nil:
		return fmt.Errorf("codegen: Options.Registry is required")
	case o.Dir == "":
		return fmt.Errorf("codegen: Options.Dir is required")
	case o.Package == "":
		return fmt.Errorf("codegen: Options.Package is required")
	}
	// The CLI lands in a package of its own, so its clause is derived from a
	// directory name and can fail to be an identifier — "web/api-client" reads
	// fine as a path and does not compile as a package. Caught here, naming the
	// option to set, rather than by go/format, which parses without checking
	// that a package name is one.
	if dir := o.clientDir(); dir != "" && !isGoIdent(o.clientPackage()) {
		return fmt.Errorf(
			"codegen: the generated client lands in %q, giving the package name %q, which is not a Go identifier; set Options.ClientPackage",
			dir, o.clientPackage())
	}
	if o.CLIDir != "" && !isGoIdent(o.cliPackage()) {
		return fmt.Errorf(
			"codegen: Options.CLIDir %q gives the package name %q, which is not a Go identifier; set Options.CLIPackage",
			o.CLIDir, o.cliPackage())
	}
	return nil
}

// Generate writes the generated files and returns their paths.
//
// The schema is validated first. Generating from a schema with a known
// authoring error would produce plausible-looking Go that encodes the mistake,
// which is harder to debug than refusing.
func Generate(opts Options) ([]string, error) {
	files, err := render(opts)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
		return nil, err
	}
	var written []string
	for _, name := range sortedKeys(files) {
		path := filepath.Join(opts.Dir, name)
		// A name may carry a subdirectory — the TypeScript client is emitted
		// into one — so the parent is created per file rather than once above.
		if dir := filepath.Dir(path); dir != opts.Dir {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, err
			}
		}
		if err := os.WriteFile(path, files[name], 0o644); err != nil {
			return nil, err
		}
		written = append(written, path)
	}
	return written, nil
}

// Check reports which generated files are missing or out of date, without
// writing anything.
//
// Generated code is committed, so it drifts: someone edits the schema, forgets
// to regenerate, and the committed models silently describe a table that no
// longer exists. Run it as a CI gate — an empty result means the tree is
// current.
func Check(opts Options) ([]string, error) {
	files, err := render(opts)
	if err != nil {
		return nil, err
	}
	var stale []string
	for _, name := range sortedKeys(files) {
		path := filepath.Join(opts.Dir, name)
		existing, err := os.ReadFile(path)
		switch {
		case os.IsNotExist(err):
			stale = append(stale, path+" (missing)")
		case err != nil:
			return nil, err
		case !bytes.Equal(existing, files[name]):
			stale = append(stale, path+" (out of date)")
		}
	}
	return stale, nil
}

// render produces the generated files in memory.
func render(opts Options) (map[string][]byte, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	if err := opts.Registry.Validate(); err != nil {
		return nil, fmt.Errorf("codegen: schema does not validate, refusing to generate:\n%w", err)
	}
	if len(opts.Registry.Tables()) == 0 {
		return nil, fmt.Errorf("codegen: registry has no tables (is the schema package imported for its side effects?)")
	}

	files := map[string][]byte{}

	if name := opts.modelsFile(); name != "-" {
		src, err := renderModels(opts)
		if err != nil {
			return nil, err
		}
		files[name] = src
	}
	if name := opts.columnsFile(); name != "-" {
		src, err := renderColumns(opts)
		if err != nil {
			return nil, err
		}
		files[name] = src
	}
	if name := opts.restFile(); name != "-" {
		src, err := renderREST(opts)
		if err != nil {
			return nil, err
		}
		// A nil result means nothing is exposed, which is not an error and
		// should not leave an empty file behind.
		if src != nil {
			files[name] = src
		}
	}
	if name := opts.manifestFile(); name != "-" {
		m := opts.Registry.BuildManifest()
		// The manifest reports what was generated rather than what the default
		// mapping would have produced: its goType field exists for a reader
		// deciding how to call the generated code, and an override changed
		// that answer (ADR-0035).
		if err := applyOverridesToManifest(m, opts); err != nil {
			return nil, err
		}
		src, err := m.JSON()
		if err != nil {
			return nil, err
		}
		files[name] = src
	}
	if opts.TSDir != "" {
		// The runtime first, for the reason the Go client is emitted before the
		// CLI: a reader of the file list should meet the thing being imported
		// before the thing importing it.
		if name := opts.tsRuntimeFile(); name != "-" {
			files[filepath.Join(opts.TSDir, name)] = renderTSRuntime()
		}
		if name := opts.tsClientFile(); name != "-" {
			src, err := renderTSClient(opts)
			if err != nil {
				return nil, err
			}
			files[filepath.Join(opts.TSDir, name)] = src
		}
		if name := opts.tsQueriesFile(); name != "-" {
			src, err := renderTSQueries(opts)
			if err != nil {
				return nil, err
			}
			// A schema that exposes nothing has no queries to emit, which is
			// not an error and should not leave an empty file behind.
			if src != nil {
				files[filepath.Join(opts.TSDir, name)] = src
			}
		}
	}
	if opts.DartDir != "" {
		if name := opts.dartRuntimeFile(); name != "-" {
			files[filepath.Join(opts.DartDir, name)] = renderDartRuntime()
		}
		if name := opts.dartFile(); name != "-" {
			src, err := renderDartClient(opts)
			if err != nil {
				return nil, err
			}
			files[filepath.Join(opts.DartDir, name)] = src
		}
	}
	// The client first, because the CLI imports it and a reader of the file
	// list should see the thing being imported before the thing importing it.
	if dir := opts.clientDir(); dir != "" {
		if name := opts.clientFile(); name != "-" {
			src, err := renderGoClient(opts)
			if err != nil {
				return nil, err
			}
			if src != nil {
				files[filepath.Join(dir, name)] = src
			}
		}
	}
	if opts.CLIDir != "" {
		if name := opts.cliFile(); name != "-" {
			src, err := renderGoCLI(opts)
			if err != nil {
				return nil, err
			}
			// A schema that exposes nothing has no commands to offer, which is
			// not an error and should not leave behind a file that imports
			// cobra for the sake of an empty tree.
			if src != nil {
				files[filepath.Join(opts.CLIDir, name)] = src
			}
		}
	}
	return files, nil
}

// Must panics if generation failed, for use in a generator main where there is
// nothing useful to do with the error.
func Must(files []string, err error) []string {
	if err != nil {
		panic(err)
	}
	for _, f := range files {
		fmt.Fprintln(os.Stderr, "generated", f)
	}
	return files
}

// header is emitted at the top of every generated Go file. The exact first line
// is what `go test`, build tooling and code review conventions look for to
// recognise generated code.
func header(pkg string, imports []string) *bytes.Buffer {
	var b bytes.Buffer
	fmt.Fprintln(&b, "// Code generated by github.com/jryannel/sqlb. DO NOT EDIT.")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "package %s\n", pkg)
	if len(imports) > 0 {
		fmt.Fprintln(&b)
		if len(imports) == 1 {
			fmt.Fprintf(&b, "import %q\n", imports[0])
		} else {
			// Standard library first, then everything else, separated by a
			// blank line. gofmt sorts within a group but will not split one,
			// so the grouping has to be written here or the output looks
			// unlike hand-written Go.
			fmt.Fprintln(&b, "import (")
			var external []string
			for _, imp := range imports {
				if strings.Contains(strings.SplitN(imp, "/", 2)[0], ".") {
					external = append(external, imp)
					continue
				}
				fmt.Fprintf(&b, "\t%q\n", imp)
			}
			if len(external) > 0 && len(external) < len(imports) {
				fmt.Fprintln(&b)
			}
			for _, imp := range external {
				fmt.Fprintf(&b, "\t%q\n", imp)
			}
			fmt.Fprintln(&b, ")")
		}
	}
	return &b
}

// gofmt formats generated source. A generator bug that produces invalid Go
// fails here, naming the file, rather than at the consumer's next build.
func gofmt(name string, src []byte) ([]byte, error) {
	out, err := format.Source(src)
	if err != nil {
		return nil, fmt.Errorf("codegen: generated %s is not valid Go: %w\n%s", name, err, numbered(src))
	}
	return out, nil
}

// numbered renders source with line numbers, so the parse error above points
// at something a reader can find.
func numbered(src []byte) string {
	var b strings.Builder
	for i, line := range strings.Split(string(src), "\n") {
		fmt.Fprintf(&b, "%4d | %s\n", i+1, line)
	}
	return b.String()
}

func sortedKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
