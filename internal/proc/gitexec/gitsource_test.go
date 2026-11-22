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

// gitRunnerSites returns "file:line" for each place in the Go files of root, outside skip, that makes a runner or a command for the git program. The program
// is the literal "git", the name ToolGit, or a name that the same folder assigns from one of them.
func gitRunnerSites(t *testing.T, root, skip string) []string {
	t.Helper()

	var found []string

	folders := map[string]bool{}

	require.NoError(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() && (entry.Name() == ".git" || path == skip) {
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

	paths, err := filepath.Glob(filepath.Join(folder, "*.go"))
	require.NoError(t, err)

	fset := token.NewFileSet()
	assigned := map[string][]ast.Expr{}

	var files []*ast.File

	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(fset, path, nil, 0)
		require.NoError(t, err)

		files = append(files, file)
		collectAssigned(file, assigned)
	}

	var found []string

	for _, file := range files {
		ast.Inspect(file, func(node ast.Node) bool {
			for _, program := range programsOf(node) {
				if namesGit(program, assigned, map[string]bool{}) {
					found = append(found, fset.Position(program.Pos()).String())
				}
			}

			return true
		})
	}

	return found
}

// collectAssigned records, for each name that the file assigns, the expressions that it gets.
func collectAssigned(file *ast.File, assigned map[string][]ast.Expr) {
	ast.Inspect(file, func(node ast.Node) bool {
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
// of NewExecRunner, the second of GroupCommand and every argument of exec.Command and exec.CommandContext.
func programsOf(node ast.Node) []ast.Expr {
	switch n := node.(type) {
	case *ast.CompositeLit:
		if selectorName(n.Type) != "ExecRunner" {
			return nil
		}

		var programs []ast.Expr

		for _, element := range n.Elts {
			if pair, ok := element.(*ast.KeyValueExpr); ok && identName(pair.Key) == "Binary" {
				programs = append(programs, pair.Value)
			}
		}

		return programs
	case *ast.CallExpr:
		switch selectorName(n.Fun) {
		case "NewExecRunner":
			return n.Args[:min(1, len(n.Args))]
		case "GroupCommand":
			return n.Args[min(1, len(n.Args)):min(2, len(n.Args))]
		case "Command", "CommandContext":
			return n.Args
		}
	}

	return nil
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
	found := gitRunnerSites(t, root, filepath.Join(root, "internal", "proc", "gitexec"))

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
			source := "package p\n\nfunc f() {\n" + body + "\n}\n"
			require.NoError(t, os.WriteFile(filepath.Join(root, "p.go"), []byte(source), 0o600))

			assert.Len(t, gitRunnerSites(t, root, ""), 1)
		})
	}

	t.Run("a runner for another program is not reported", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		source := "package p\n\nfunc f() {\nname := \"gh\"\n_ = proc.ExecRunner{Binary: name}\n_ = proc.NewExecRunner(\"find\", dir)\n}\n"
		require.NoError(t, os.WriteFile(filepath.Join(root, "p.go"), []byte(source), 0o600))

		assert.Empty(t, gitRunnerSites(t, root, ""))
	})
}
