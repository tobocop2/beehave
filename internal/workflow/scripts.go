package workflow

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.starlark.net/syntax"
)

// scriptPrefix starts the name of a pinned script. The rest is the path from the root of the repository that holds the workflow file.
const scriptPrefix = "script//"

// repoRoot returns the folder with a .git entry that is the nearest to dir or above it. A workflow folder outside a repository is its own root.
func repoRoot(dir string) string {
	for at := dir; ; at = filepath.Dir(at) {
		if _, err := os.Stat(filepath.Join(at, ".git")); err == nil {
			return at
		}

		if filepath.Dir(at) == at {
			return dir
		}
	}
}

// programs returns the first word of the run list of each command call of the file that runs on this machine and names a path.
// A call with a host runs on another machine and is left out. A run list that is not a literal list of texts is left to the run.
// It also counts the calls on this machine whose run list is not a literal list with a literal first word: their script is not known, so it is not pinned.
func programs(parsed *syntax.File) (found []string, dynamic int) {
	shadowed := definedNames(parsed)

	syntax.Walk(parsed, func(n syntax.Node) bool {
		call, ok := n.(*syntax.CallExpr)
		if !ok || callee(call) != commandName || slices.Contains(shadowed, callee(call)) {
			return true
		}

		if first, known := firstWord(call); known {
			if strings.Contains(first, "/") {
				found = append(found, first)
			}
		} else if !hasHost(call) {
			dynamic++
		}

		return true
	})

	return found, dynamic
}

// hasHost reports whether a command call names a host, so its program runs on another machine.
func hasHost(call *syntax.CallExpr) bool {
	return slices.ContainsFunc(call.Args, func(arg syntax.Expr) bool {
		name, _ := keyword(arg)

		return name == "host"
	})
}

// firstWord returns the first text of the literal run list of a command call, or false for a call with a host or a run list that is not a literal.
func firstWord(call *syntax.CallExpr) (string, bool) {
	list := runList(call)
	if list == nil || len(list.List) == 0 {
		return "", false
	}

	word, isLiteral := list.List[0].(*syntax.Literal)
	if !isLiteral {
		return "", false
	}

	text, isText := word.Value.(string)

	return text, isText
}

// runList returns the run list of a command call when it is a list, and nil for a call with a host or a run list that is not a list.
func runList(call *syntax.CallExpr) *syntax.ListExpr {
	var run syntax.Expr

	for i, arg := range call.Args {
		name, pair := keyword(arg)

		switch {
		case name == "host":
			return nil
		case name == "run":
			run = pair.Y
		case pair == nil && i == 0:
			run = arg
		}
	}

	if list, isList := run.(*syntax.ListExpr); isList {
		return list
	}

	return nil
}

// keyword returns the name and the pair of a keyword argument. A positional argument has an empty name and no pair.
func keyword(arg syntax.Expr) (string, *syntax.BinaryExpr) {
	pair, isPair := arg.(*syntax.BinaryExpr)
	if !isPair || pair.Op != syntax.EQ {
		return "", nil
	}

	var name string

	if key, isName := pair.X.(*syntax.Ident); isName { // the parser rejects a keyword argument whose key is not a name
		name = key.Name
	}

	return name, pair
}

// scripts splits the programs of the graph into the scripts that sit in the repository, as paths from its root, and the paths outside it.
// A path that names no file is a program that the run makes or finds, and it is in neither list.
func (g *graph) scripts() (inside, outside []string) {
	root := repoRoot(g.dir)

	for _, program := range g.programs {
		rel, abs, regular := locate(root, program)

		switch {
		case !regular:
		case rel != "":
			inside = append(inside, rel)
		default:
			outside = append(outside, abs)
		}
	}

	slices.Sort(inside)
	slices.Sort(outside)

	return slices.Compact(inside), slices.Compact(outside)
}

// locate returns the absolute path of a program, and its path from the root when it sits in the repository (empty when it sits outside).
// The last result is false when the path names no regular file.
func locate(root, program string) (rel, abs string, regular bool) {
	abs = program
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, program)
	}

	if !isRegular(abs) {
		return "", abs, false
	}

	if rel, err := filepath.Rel(root, abs); err == nil && filepath.IsLocal(rel) {
		return filepath.ToSlash(rel), abs, true
	}

	return "", abs, true
}

func isRegular(path string) bool {
	info, err := os.Stat(path)

	return err == nil && info.Mode().IsRegular()
}

// OutsideScripts returns the scripts that the command calls of the workflow file and the files it loads name by a path outside the repository.
// A run does not pin them, so they can change under a run that is open. It runs no code, and it returns nothing for a file that does not read.
func OutsideScripts(file string) []string {
	_, outside := scan(file).scripts()

	return outside
}

// DynamicCommands returns how many command calls of the workflow file and the files it loads have a run list that is not a literal list of texts.
// The script of such a call is not known before the run, so a run does not pin it. A call with a host runs on another machine and is not counted.
func DynamicCommands(file string) int {
	return scan(file).dynamic
}
