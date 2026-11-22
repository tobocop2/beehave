package gitexec_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gitRunnerSites returns "file:line" for each place in the Go files of root that makes a runner or a command for the git program. The program is the literal
// "git", the name ToolGit, or a name that the same folder assigns from one of them, in the same function or at package level. It does not see a program
// that comes from a function result, a field, a parameter, a slice element, a selector into another folder, a raw string, a concatenation or an unkeyed
// ExecRunner literal, or from an assignment in one function to a variable that another function uses.
func gitRunnerSites(t *testing.T, root string) []string {
	t.Helper()

	var found []string

	folders := map[string]bool{}

	require.NoError(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() && entry.Name() == ".git" {
			return filepath.SkipDir
		}

		if entry.IsDir() {
			folders[path] = true
		}

		return nil
	}))

	for folder := range folders {
		found = append(found, gitRunnerSitesIn(t, folder)...)
	}

	return found
}

// gitRunnerSitesIn returns the sites of the non-test Go files in one folder.
func gitRunnerSitesIn(t *testing.T, folder string) []string {
	t.Helper()

	fset := token.NewFileSet()
	files, packageLevel := parseFolder(t, fset, folder)

	var found []string

	for _, file := range files {
		execNames := importNames(file, "os/exec")

		for _, decl := range file.Decls {
			assigned := scopeOf(decl, packageLevel)

			ast.Inspect(decl, func(node ast.Node) bool {
				for _, program := range programsOf(node, execNames) {
					if namesGit(program, assigned, map[string]bool{}) {
						found = append(found, fset.Position(program.Pos()).String())
					}
				}

				return true
			})
		}
	}

	return found
}

// parseFolder parses the non-test Go files of a folder and returns them with the assignments that sit outside every function.
func parseFolder(t *testing.T, fset *token.FileSet, folder string) ([]*ast.File, map[string][]ast.Expr) {
	t.Helper()

	paths, err := filepath.Glob(filepath.Join(folder, "*.go"))
	require.NoError(t, err)

	packageLevel := map[string][]ast.Expr{}

	var files []*ast.File

	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(fset, path, nil, 0)
		require.NoError(t, err)

		files = append(files, file)

		for _, decl := range file.Decls {
			if _, isFunc := decl.(*ast.FuncDecl); !isFunc {
				collectAssigned(decl, packageLevel)
			}
		}
	}

	return files, packageLevel
}

// scopeOf returns the assignments that a declaration sees: those of the package level, plus its own when it is a function.
func scopeOf(decl ast.Decl, packageLevel map[string][]ast.Expr) map[string][]ast.Expr {
	function, isFunc := decl.(*ast.FuncDecl)
	if !isFunc {
		return packageLevel
	}

	local := map[string][]ast.Expr{}
	collectAssigned(function, local)

	for name, values := range packageLevel {
		local[name] = append(local[name], values...)
	}

	return local
}

// importNames returns the names under which the file imports the package at path.
func importNames(file *ast.File, path string) map[string]bool {
	names := map[string]bool{}

	for _, spec := range file.Imports {
		if spec.Path.Value != `"`+path+`"` {
			continue
		}

		if spec.Name != nil {
			names[spec.Name.Name] = true
		} else {
			names[filepath.Base(path)] = true
		}
	}

	return names
}

// collectAssigned records, for each name that the node assigns, the expressions that it gets.
func collectAssigned(root ast.Node, assigned map[string][]ast.Expr) {
	ast.Inspect(root, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.AssignStmt:
			assign(assigned, n.Lhs, n.Rhs)
		case *ast.ValueSpec:
			names := make([]ast.Expr, len(n.Names))
			for i, name := range n.Names {
				names[i] = name
			}

			assign(assigned, names, n.Values)
		}

		return true
	})
}

// assign records each right side under the name on the left at the same place, when both sides have the same length.
func assign(assigned map[string][]ast.Expr, left, right []ast.Expr) {
	if len(left) != len(right) {
		return
	}

	for i, target := range left {
		if name := identName(target); name != "" {
			assigned[name] = append(assigned[name], right[i])
		}
	}
}

// programsOf returns the expressions that name the program in a node that makes a runner or a command: the Binary field of an ExecRunner, the first argument
// of NewExecRunner, the second of GroupCommand, the first of Command and the second of CommandContext when the call is on a name that imports os/exec.
func programsOf(node ast.Node, execNames map[string]bool) []ast.Expr {
	switch n := node.(type) {
	case *ast.CompositeLit:
		return binaryOf(n)
	case *ast.CallExpr:
		switch selectorName(n.Fun) {
		case "NewExecRunner":
			return argAt(n.Args, 0)
		case "GroupCommand":
			return argAt(n.Args, 1)
		}

		return execProgramOf(n, execNames)
	}

	return nil
}

// binaryOf returns the value of the Binary field when the literal is an ExecRunner.
func binaryOf(literal *ast.CompositeLit) []ast.Expr {
	if selectorName(literal.Type) != "ExecRunner" {
		return nil
	}

	var programs []ast.Expr

	for _, element := range literal.Elts {
		if pair, ok := element.(*ast.KeyValueExpr); ok && identName(pair.Key) == "Binary" {
			programs = append(programs, pair.Value)
		}
	}

	return programs
}

// execProgramOf returns the program argument of a Command or CommandContext call on a name that imports os/exec.
func execProgramOf(call *ast.CallExpr, execNames map[string]bool) []ast.Expr {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !execNames[identName(selector.X)] {
		return nil
	}

	switch selector.Sel.Name {
	case "Command":
		return argAt(call.Args, 0)
	case "CommandContext":
		return argAt(call.Args, 1)
	}

	return nil
}

// argAt returns the argument at the index as a slice of one, or nothing when the call has fewer arguments.
func argAt(args []ast.Expr, index int) []ast.Expr {
	if index >= len(args) {
		return nil
	}

	return args[index : index+1]
}

// namesGit reports whether the expression is the program git: the literal, ToolGit, or a name that is assigned one of them.
func namesGit(expr ast.Expr, assigned map[string][]ast.Expr, seen map[string]bool) bool {
	switch e := expr.(type) {
	case *ast.BasicLit:
		return e.Value == `"git"`
	case *ast.SelectorExpr:
		return e.Sel.Name == "ToolGit"
	case *ast.Ident:
		if e.Name == "ToolGit" {
			return true
		}

		if seen[e.Name] {
			return false
		}

		seen[e.Name] = true

		for _, value := range assigned[e.Name] {
			if namesGit(value, assigned, seen) {
				return true
			}
		}
	}

	return false
}

// selectorName returns the last name of a plain name or a selector, or an empty string.
func selectorName(expr ast.Expr) string {
	if selector, ok := expr.(*ast.SelectorExpr); ok {
		return selector.Sel.Name
	}

	return identName(expr)
}

func identName(expr ast.Expr) string {
	if name, ok := expr.(*ast.Ident); ok {
		return name.Name
	}

	return ""
}

// TestTheGitProgramIsNamedOnlyHere fails when code outside this package makes its own runner for git, because that call would run the hooks of the tree.
func TestTheGitProgramIsNamedOnlyHere(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..", "..")
	found := gitRunnerSites(t, root)

	assert.Empty(t, found, "make the runner with gitexec.New, so the hooks stay off")
}

func TestTheGitSourceTestSeesAVariableOrConstantThatHoldsTheGitProgram(t *testing.T) {
	t.Parallel()

	routes := map[string]string{
		"a literal as the binary":             `proc.ExecRunner{Binary: "git"}`,
		"the tool name as the binary":         `proc.ExecRunner{Binary: model.ToolGit}`,
		"a variable that holds the tool name": "name := model.ToolGit\n_ = proc.ExecRunner{Binary: name}",
		"a variable that holds the literal":   "name := \"git\"\n_ = proc.NewExecRunner(name, dir)",
		"a constant that holds the literal":   "const name = \"git\"\n_ = proc.NewExecRunner(name, dir)",
		"a variable that copies a variable":   "first := \"git\"\nsecond := first\n_ = proc.NewExecRunner(second, dir)",
		"a variable in a group command":       "name := model.ToolGit\n_ = proc.GroupCommand(ctx, name, nil, dir)",
		"a variable in exec.Command":          "name := \"git\"\n_ = exec.CommandContext(ctx, name, \"status\")",
	}

	for name, body := range routes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			source := "package p\n\nimport \"os/exec\"\n\nfunc f() {\n" + body + "\n}\n"
			require.NoError(t, os.WriteFile(filepath.Join(root, "p.go"), []byte(source), 0o600))

			assert.Len(t, gitRunnerSites(t, root), 1)
		})
	}
}

func TestTheGitSourceTestSeesAnAliasedExecPackageAndAPackageLevelName(t *testing.T) {
	t.Parallel()

	sources := map[string]string{
		"an aliased os/exec package": "package p\n\nimport osexec \"os/exec\"\n\nfunc f() {\n_ = osexec.Command(\"git\")\n}\n",
		"a package level variable":   "package p\n\nvar name = \"git\"\n\nfunc f() {\n_ = proc.NewExecRunner(name, dir)\n}\n",
	}

	for name, source := range sources {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(root, "p.go"), []byte(source), 0o600))

			assert.Len(t, gitRunnerSites(t, root), 1)
		})
	}
}

func TestTheGitSourceTestLeavesOtherProgramsAlone(t *testing.T) {
	t.Parallel()

	sources := map[string]string{
		"a runner for another program": "package p\n\nfunc f() {\nname := \"gh\"\n_ = proc.ExecRunner{Binary: name}\n_ = proc.NewExecRunner(\"find\", dir)\n}\n",
		"git as an argument of another program": "package p\n\nimport \"os/exec\"\n\nfunc f() {\n_ = exec.Command(\"which\", \"git\")\n" +
			"_ = exec.CommandContext(ctx, \"which\", \"git\")\n}\n",
		"a Command call that is not os/exec":   "package p\n\nimport \"github.com/spf13/cobra\"\n\nfunc f() {\n_ = cobra.Command(\"git\")\n_ = c.Command(\"git\")\n}\n",
		"one name for git in another function": "package p\n\nfunc f() {\nname := \"git\"\n_ = name\n}\n\nfunc g() {\nname := \"gh\"\n_ = proc.NewExecRunner(name, dir)\n}\n",
	}

	for name, source := range sources {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(root, "p.go"), []byte(source), 0o600))

			assert.Empty(t, gitRunnerSites(t, root))
		})
	}
}
