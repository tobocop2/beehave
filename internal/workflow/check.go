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

	"github.com/tobocop2/beehave/internal/model"
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
func reservedEnvPrefixes() []string { return []string{"DBOS__", "BEEHAVE_"} }

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

// The smallest and the largest value of max_new_processes in a command call.
const (
	MinProcessCap = 1
	MaxProcessCap = 100000
)

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
	// notes are the facts for Notes: no problem, and no effect on the exit code.
	notes []Diagnostic
	// entries are the literal entries of the spawn calls, and mainNames the functions that the workflow file binds at the top level.
	entries   []entryRef
	mainNames []string
	// mainFile is the parsed workflow file. Its results are checked after every file is read, because the entries of the spawn calls of the
	// loaded files name functions of this file.
	mainFile *syntax.File
	// programs are the paths that the command calls of every file name as the program to run.
	programs []string
	// dynamic counts the command calls on this machine whose run list is not a literal.
	dynamic int
	// repositoryCalls are the calls of the builtins that need a repository. The text of each is the name of the builtin.
	repositoryCalls []Diagnostic
	// gateCalls are the calls that run a gate on this machine. The text of each is the name of the builtin.
	gateCalls []hereGate
}

func predeclared(name string) bool { return slices.Contains(Builtins(), name) }

// scan reads the workflow file and the files it loads, once each, with no run of code.
func scan(file string) *graph {
	g := &graph{dir: filepath.Dir(file), main: filepath.Base(file), units: map[string]*unit{}}
	g.visit(g.main, Diagnostic{File: file})
	g.entriesDefined()

	if g.mainFile != nil {
		g.results(g.mainFile, g.itemFunctions())
	}

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

// finishTakesNoItem reports whether the file defines finish with a number of parameters other than one.
func finishTakesNoItem(parsed *syntax.File) bool {
	return slices.ContainsFunc(parsed.Stmts, func(stmt syntax.Stmt) bool {
		def, ok := stmt.(*syntax.DefStmt)

		return ok && def.Name.Name == "finish" && len(def.Params) != 1
	})
}

// inspect checks the calls of a parsed file, and keeps the names that the workflow file binds.
func (g *graph) inspect(name string, parsed *syntax.File) {
	g.arguments(parsed)
	g.repositoryCalls, g.gateCalls = append(g.repositoryCalls, repositoryCalls(g.display(name), parsed)...), append(g.gateCalls, gateCalls(g.display(name), parsed)...)
	found, dynamic := programs(parsed)
	g.programs = append(g.programs, found...)
	g.dynamic += dynamic
	g.sandboxes(parsed)
	g.swarm(parsed)

	if name != g.main {
		g.results(parsed, nil)
	}

	if name == g.main {
		g.mainFile = parsed
		g.mainNames = definedNames(parsed)

		if finishTakesNoItem(parsed) {
			g.report(Diagnostic{File: g.display(name)}, "finish takes the item. Write def finish(item):")
		}
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

// repositoryBuiltins are the builtins that read git or a forge. A project with no repository cannot call them.
func repositoryBuiltins() []string {
	return []string{gateName, mergeName, batchMergeName, preflightName}
}

// repositoryCalls lists the calls of repositoryBuiltins in a parsed file. A name that the file defines is not the builtin.
func repositoryCalls(file string, parsed *syntax.File) []Diagnostic {
	shadowed := definedNames(parsed)

	var calls []Diagnostic

	syntax.Walk(parsed, func(n syntax.Node) bool {
		call, ok := n.(*syntax.CallExpr)
		if !ok || !needsRepository(call) || slices.Contains(shadowed, callee(call)) {
			return true
		}

		at := placeOf(call.Lparen)
		at.File = file
		at.Text = callee(call)
		calls = append(calls, at)

		return true
	})

	return calls
}

// needsRepository reports whether the call is one of repositoryBuiltins. A gate call that names run runs its own program and reads no git.
func needsRepository(call *syntax.CallExpr) bool {
	name := callee(call)

	return slices.Contains(repositoryBuiltins(), name) && (name != gateName || !namesKeyword(call, "run"))
}

// namesKeyword reports whether a call passes the keyword with a value that can hold a program. A literal empty list, empty tuple or None cannot.
// A gate that names its own program with run reads no git.
func namesKeyword(call *syntax.CallExpr, keyword string) bool {
	for _, arg := range call.Args {
		pair, isPair := arg.(*syntax.BinaryExpr)
		if !isPair || pair.Op != syntax.EQ {
			continue
		}

		if key, isName := pair.X.(*syntax.Ident); isName && key.Name == keyword {
			return !emptyLiteral(pair.Y)
		}
	}

	return false
}

// emptyLiteral reports whether the expression is the literal None, [] or ().
func emptyLiteral(expr syntax.Expr) bool {
	switch value := expr.(type) {
	case *syntax.Ident:
		return value.Name == "None"
	case *syntax.ListExpr:
		return len(value.List) == 0
	case *syntax.TupleExpr:
		return len(value.List) == 0
	case *syntax.ParenExpr:
		return emptyLiteral(value.X)
	}

	return false
}

// CheckNoRepository returns one diagnostic for each call of gate, merge, batch_merge or preflight in the workflow file and the files it loads.
// The project has no repository, and these builtins refuse it at run time. A gate call that names run is not one of them: it runs its own program.
// It runs no workflow code.
func CheckNoRepository(file, project string) []Diagnostic {
	var found []Diagnostic

	for _, call := range scan(file).repositoryCalls {
		call.Text = fmt.Sprintf("%s needs a repository, and project %s has no repository. Set `source` or `forge` in the project file, or remove the call.", call.Text, project)
		found = append(found, call)
	}

	return found
}

// Check reads the workflow file and the files it loads and returns one diagnostic for each problem: a syntax error, a name that is not defined,
// a load that cannot be read or loops, and a missing main. unparsed is true when a file cannot be read or parsed. It runs no workflow code.
func Check(file string) (found []Diagnostic, unparsed bool) {
	g := scan(file)

	return g.found, g.unparsed
}

// CheckSandboxedGates returns one diagnostic for each gate or batch_merge call in the workflow file and the files it loads that runs a gate on this machine.
// The project has its sandbox on and refusal says why such a gate does not run. A gate call that names run and a host runs there, so it is not listed.
// A call that uses the project gate is listed only when projectGate is true, because a project that builds on a host runs its project gate there.
// It runs no workflow code.
func CheckSandboxedGates(file, project, refusal string, projectGate bool) []Diagnostic {
	var found []Diagnostic

	for _, call := range scan(file).gateCalls {
		if call.program || projectGate {
			call.at.Text = fmt.Sprintf("%s runs a gate on this machine, and project %s has the sandbox on: %s", call.at.Text, project, refusal)
			found = append(found, call.at)
		}
	}

	return found
}

// hereGate is a call that runs a gate on this machine. program is true when the call names the program of the gate.
type hereGate struct {
	at      Diagnostic
	program bool
}

// gateCalls lists the calls in a parsed file that run a gate on this machine: each batch_merge, each gate without a program and each gate with a program and no host.
// A name that the file defines is not the builtin.
func gateCalls(file string, parsed *syntax.File) []hereGate {
	shadowed := definedNames(parsed)

	var calls []hereGate

	syntax.Walk(parsed, func(n syntax.Node) bool {
		call, ok := n.(*syntax.CallExpr)
		if !ok || slices.Contains(shadowed, callee(call)) {
			return true
		}

		if program, here := runsHere(call); here {
			at := placeOf(call.Lparen)
			at.File, at.Text = file, callee(call)
			calls = append(calls, hereGate{at: at, program: program})
		}

		return true
	})

	return calls
}

// runsHere reports whether the call runs a gate on this machine, and whether that gate is a program that the call names.
// A batch_merge always does. A gate does unless it names both a program and a host.
func runsHere(call *syntax.CallExpr) (program, here bool) {
	switch callee(call) {
	case batchMergeName:
		return namesKeyword(call, "gate_run"), true
	case gateName:
		program = namesKeyword(call, "run")

		return program, !program || !namesHost(call)
	}

	return false, false
}

// namesHost reports whether a call passes a host keyword that is not the literal empty text.
func namesHost(call *syntax.CallExpr) bool {
	host := argument(call, "host", len(call.Args))
	if host == nil {
		return false
	}

	text, isText := literal(host)

	return !isText || text != ""
}
