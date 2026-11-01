package workflow

import (
	"cmp"
	"fmt"
	"slices"

	"go.starlark.net/syntax"

	"github.com/tobocop2/obee/internal/model"
)

// sandboxKeyword is the keyword of an agent call that sets the sandbox, and sandboxName the builtin that builds its value.
const (
	sandboxKeyword = "sandbox"
	sandboxName    = "sandbox"
	noneName       = "None"
)

// Notes returns the notes of the workflow file: facts that the author may want to see, which are no problem and change no exit code.
// A step with sandbox = None gets a note. It reads the file the way Check does and runs no workflow code.
func Notes(file string) []Diagnostic {
	g := scan(file)
	slices.SortStableFunc(g.notes, func(a, b Diagnostic) int {
		return cmp.Or(cmp.Compare(a.File, b.File), cmp.Compare(a.Line, b.Line), cmp.Compare(a.Col, b.Col))
	})

	return g.notes
}

// note keeps a note for Notes.
func (g *graph) note(at Diagnostic, text string) {
	at.Text = text
	g.notes = append(g.notes, at)
}

// sandboxes checks the sandbox keyword of each agent call in a parsed file. The value is sandbox(...), which the signature of sandbox checks,
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

// sandboxCall checks one call: the sandbox keyword of an agent call, or the lists of a sandbox call. A name that the file defines is not the builtin.
func (g *graph) sandboxCall(call *syntax.CallExpr, shadowed []string) {
	name := callee(call)
	if slices.Contains(shadowed, name) {
		return
	}

	switch name {
	case agentName:
		g.sandboxValue(call)
	case sandboxName:
		g.sandboxLists(call)
	}
}

// sandboxValue checks the value of the sandbox keyword of one agent call.
func (g *graph) sandboxValue(call *syntax.CallExpr) {
	value := argument(call, sandboxKeyword, len(call.Args))
	if value == nil {
		return
	}

	switch v := value.(type) {
	case *syntax.Ident:
		switch {
		case v.Name == noneName:
			g.note(placeOf(syntax.Start(value)), "sandbox is None, so the shell of this step runs with no sandbox. It can read every file of the owner and use the network.")
		case isBoolName(v.Name):
			g.report(placeOf(syntax.Start(value)), "sandbox must be sandbox(...) or None, not "+v.Name+". Write sandbox = sandbox(network = [...]) to turn the sandbox on, or leave the keyword out to use the default of the project.")
		}
	case *syntax.Literal, *syntax.ListExpr, *syntax.TupleExpr, *syntax.DictExpr:
		g.report(placeOf(syntax.Start(value)), "sandbox must be sandbox(...) or None, not "+rawText(value)+". Write sandbox = sandbox(network = [...]) to turn the sandbox on, or leave the keyword out to use the default of the project.")
	}
}

// sandboxLists checks the literal items of a sandbox call: a network entry must be a domain name, and an allow_write entry must be a path.
func (g *graph) sandboxLists(call *syntax.CallExpr) {
	if splat(call) {
		return
	}

	for i, key := range []string{"network", "allow_write"} {
		list, ok := argument(call, key, i).(*syntax.ListExpr)
		if !ok {
			continue
		}

		g.sandboxItems(key, list)
	}
}

// sandboxItems checks the literal items of one list of a sandbox call.
func (g *graph) sandboxItems(key string, list *syntax.ListExpr) {
	for _, item := range list.List {
		text, isText := literal(item)
		if !isText {
			continue
		}

		if err := checkSandboxItem(key, text); err != nil {
			g.report(placeOf(syntax.Start(item)), fmt.Sprintf("%v.", err))
		}
	}
}

func checkSandboxItem(key, text string) error {
	if key == "network" {
		return model.CheckNetwork([]string{text})
	}

	return model.CheckAllowWrite([]string{text})
}
