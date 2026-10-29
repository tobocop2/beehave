package workflow

import (
	"cmp"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"go.starlark.net/resolve"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"github.com/tobocop2/obee/internal/model"
)

// Diagnostic is one problem in a file. It renders as file:line:col: text. A problem with the whole file has line 0 and renders as file: text.
type Diagnostic struct {
	File string
	Line int
	Col  int
	Text string
}

// Error renders the position and the text.
func (d Diagnostic) Error() string {
	if d.Line == 0 {
		return fmt.Sprintf("%s: %s", d.File, d.Text)
	}

	return fmt.Sprintf("%s:%d:%d: %s", d.File, d.Line, d.Col, d.Text)
}

const (
	minStopCode = 1
	maxStopCode = 255
)

// reservedEnvPrefixes lists the prefixes of the variable names that a workflow never sets or reads. The engine and DBOS own these names.
func reservedEnvPrefixes() []string { return []string{"DBOS__", "OBEE_"} }

// secretWords lists the words that mark the name of a secret. The env builtin refuses such a name.
func secretWords() []string { return []string{"TOKEN", "SECRET", "PASSWORD", "CREDENTIAL"} }

// ReservedEnvName reports whether name starts with a prefix that a workflow never sets or reads.
func ReservedEnvName(name string) bool {
	return slices.ContainsFunc(reservedEnvPrefixes(), func(prefix string) bool { return strings.HasPrefix(name, prefix) })
}

// ReservedEnvAdvice is the end of the message for a reserved variable name.
func ReservedEnvAdvice() string {
	return "Names that start with " + strings.Join(reservedEnvPrefixes(), " or ") + " belong to the engine. Use another name."
}

// SecretEnvName reports whether name looks like the name of a secret, in any case.
func SecretEnvName(name string) bool {
	upper := strings.ToUpper(name)

	return strings.HasSuffix(upper, "_KEY") || slices.ContainsFunc(secretWords(), func(word string) bool { return strings.Contains(upper, word) })
}

// StopCode reports whether code is an exit code that stop_on_rc can list: a whole number from 1 to 255. Code 0 is success.
func StopCode(code int) bool { return code >= minStopCode && code <= maxStopCode }

// unit is one file of the graph.
type unit struct{ data []byte }

// graph is the workflow file and every file it loads.
type graph struct {
	dir      string
	main     string
	units    map[string]*unit
	stack    []string
	found    []Diagnostic
	unparsed bool
	// entries are the literal entries of the spawn calls, and mainNames the functions that the workflow file binds at the top level.
	entries   []entryRef
	mainNames []string
}

func predeclared(name string) bool { return slices.Contains(Builtins(), name) }

// scan reads the workflow file and the files it loads, once each, with no run of code.
func scan(file string) *graph {
	g := &graph{dir: filepath.Dir(file), main: filepath.Base(file), units: map[string]*unit{}}
	g.visit(g.main, Diagnostic{File: file})
	g.entriesDefined()

	slices.SortStableFunc(g.found, func(a, b Diagnostic) int {
		return cmp.Or(cmp.Compare(a.File, b.File), cmp.Compare(a.Line, b.Line), cmp.Compare(a.Col, b.Col))
	})

	return g
}

// display is the name of a file in a message: the path from the folder of the run for a local file.
func (g *graph) display(name string) string {
	if strings.HasPrefix(name, embeddedPrefix) {
		return name
	}

	return filepath.Join(g.dir, name)
}

func (g *graph) report(at Diagnostic, text string) {
	at.Text = text
	g.found = append(g.found, at)
}

func (g *graph) visit(name string, at Diagnostic) {
	if i := slices.Index(g.stack, name); i >= 0 {
		g.report(at, "files load each other: "+strings.Join(append(slices.Clone(g.stack[i:]), name), " -> ")+". Remove one load.")

		return
	}

	if _, done := g.units[name]; done {
		return
	}

	data, err := readFile(g.dir, name)
	if err != nil {
		g.unparsed = true
		g.report(at, fmt.Sprintf("cannot read %s: %v. Check the path.", name, err))

		return
	}

	g.units[name] = &unit{data: data}
	g.parse(name, data)
}

// parse reads the text of a file, then visits the files it loads. A file that does not parse is a problem.
func (g *graph) parse(name string, data []byte) {
	parsed, _, err := starlark.SourceProgramOptions(model.StarlarkFileOptions(), g.display(name), data, predeclared)
	if err != nil {
		g.unparsed = true
		g.problems(err)

		return
	}

	g.inspect(name, parsed)

	g.stack = append(g.stack, name)
	g.loads(parsed)
	g.stack = g.stack[:len(g.stack)-1]

	if name == g.main && !definesMain(parsed) {
		g.report(Diagnostic{File: g.display(name)}, "the file defines no function main. Add def main(item):")
	}
}

// inspect checks the calls of a parsed file, and keeps the names that the workflow file binds.
func (g *graph) inspect(name string, parsed *syntax.File) {
	g.arguments(parsed)
	g.swarm(parsed)

	if name == g.main {
		g.mainNames = definedNames(parsed)
	}
}

// loads visits the file of each load statement.
func (g *graph) loads(parsed *syntax.File) {
	for _, stmt := range parsed.Stmts {
		load, ok := stmt.(*syntax.LoadStmt)
		if !ok {
			continue
		}

		at := Diagnostic{File: load.Load.Filename(), Line: int(load.Load.Line), Col: int(load.Load.Col)}

		name, err := resolveModule(load.ModuleName())
		if err != nil {
			g.report(at, err.Error())

			continue
		}

		g.visit(name, at)
	}
}

func definesMain(parsed *syntax.File) bool {
	return slices.ContainsFunc(parsed.Stmts, func(stmt syntax.Stmt) bool {
		def, ok := stmt.(*syntax.DefStmt)

		return ok && def.Name.Name == "main"
	})
}

// problems turns a syntax error or a list of resolve errors into diagnostics.
func (g *graph) problems(err error) {
	var (
		syntaxed syntax.Error
		resolved resolve.ErrorList
	)

	switch {
	case errors.As(err, &syntaxed):
		g.report(placeOf(syntaxed.Pos), sentence(syntaxed.Msg))
	case errors.As(err, &resolved):
		for _, one := range resolved {
			g.report(placeOf(one.Pos), sentence(one.Msg))
		}
	default:
		g.report(Diagnostic{}, err.Error())
	}
}

func placeOf(pos syntax.Position) Diagnostic {
	return Diagnostic{File: pos.Filename(), Line: int(pos.Line), Col: int(pos.Col)}
}

func sentence(text string) string { return strings.TrimRight(text, ".") + "." }

// Check reads the workflow file and the files it loads and returns one diagnostic for each problem: a syntax error, a name that is not defined,
// a load that cannot be read or loops, and a missing main. unparsed is true when a file cannot be read or parsed. It runs no workflow code.
func Check(file string) (found []Diagnostic, unparsed bool) {
	g := scan(file)

	return g.found, g.unparsed
}
