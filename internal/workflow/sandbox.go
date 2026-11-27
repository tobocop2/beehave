package workflow

import (
	"cmp"
	"slices"

	"go.starlark.net/syntax"

	"github.com/tobocop2/beehest/internal/model"
)

// sandboxKeyword is the keyword of an agent call and of a command call that sets the sandbox, and sandboxName the builtin that builds its value.
const (
	sandboxKeyword = "sandbox"
	sandboxName    = "sandbox"
	noneName       = "None"
)

// Notes returns the notes of the workflow file: facts that the author may want to see, which are no problem and change no exit code.
// A step with sandbox = None gets a note. It reads the file the way Check does and runs no workflow code.
func Notes(file string) []Diagnostic {
	g := scan(file)
	slices.SortStableFunc(g.notes, byPlace)

	return g.notes
}

// byPlace orders diagnostics by file, line and column.
func byPlace(a, b Diagnostic) int {
	return cmp.Or(cmp.Compare(a.File, b.File), cmp.Compare(a.Line, b.Line), cmp.Compare(a.Col, b.Col))
}

// CheckSandboxNone returns one diagnostic for each agent call with the literal sandbox = None in the workflow file and the files it loads.
// The project has its sandbox on, and the engine refuses such a step at its call. It returns nothing when the sandbox of the project is off.
// A value that the file computes is left to the engine. It runs no workflow code.
func CheckSandboxNone(file, project string, mode model.SandboxMode) []Diagnostic {
	if mode != model.SandboxOn {
		return nil
	}

	var found []Diagnostic

	for _, at := range scan(file).noneSandboxes {
		at.Text = "sandbox is None, and project " + project + " has the sandbox on, so the run refuses this step. Write sandbox = sandbox(network = [...]) on the step, or set sandbox = \"off\" in the project file. Then run beehest run for the item."
		found = append(found, at)
	}

	slices.SortStableFunc(found, byPlace)

	return found
}

// note keeps a note for Notes.
func (g *graph) note(at Diagnostic, text string) {
	at.Text = text
	g.notes = append(g.notes, at)
}

// sandboxes checks the sandbox keyword of each agent and command call in a parsed file. The value is sandbox(...), which the signature of sandbox checks,
// or None, which turns the sandbox off and gets a note. A literal of any other kind is a problem. A value that is not a literal is left to the run.
func (g *graph) sandboxes(parsed *syntax.File) {
	shadowed := definedNames(parsed)

	syntax.Walk(parsed, func(n syntax.Node) bool {
		call, ok := n.(*syntax.CallExpr)
		if !ok {
			return true
		}

		g.sandboxCall(call, shadowed)

		return true
	})
}

// sandboxCall checks the sandbox keyword of an agent or command call. The lists of a sandbox call are keywords like any other. A name that the file defines is not the builtin.
func (g *graph) sandboxCall(call *syntax.CallExpr, shadowed []string) {
	if name := callee(call); (name == agentName || name == commandName) && !slices.Contains(shadowed, name) {
		g.sandboxValue(call)
	}
}

// sandboxValue checks the value of the sandbox keyword of one agent or command call.
func (g *graph) sandboxValue(call *syntax.CallExpr) {
	value := argument(call, sandboxKeyword, len(call.Args))
	if value == nil {
		return
	}

	switch v := value.(type) {
	case *syntax.Ident:
		switch {
		case v.Name == noneName:
			g.noneSandboxes = append(g.noneSandboxes, placeOf(syntax.Start(value)))
			g.note(placeOf(syntax.Start(value)), "sandbox is None, so the shell of this step runs with no sandbox. It can read every file of the owner and use the network.")
		case isBoolName(v.Name):
			g.report(placeOf(syntax.Start(value)), "sandbox must be sandbox(...) or None, not "+v.Name+". Write sandbox = sandbox(network = [...]) to turn the sandbox on, or leave the keyword out to use the default of the project.")
		}
	case *syntax.Literal, *syntax.ListExpr, *syntax.TupleExpr, *syntax.DictExpr:
		g.report(placeOf(syntax.Start(value)), "sandbox must be sandbox(...) or None, not "+rawText(value)+". Write sandbox = sandbox(network = [...]) to turn the sandbox on, or leave the keyword out to use the default of the project.")
	}
}
