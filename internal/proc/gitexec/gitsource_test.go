package gitexec_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// execPackage is the import path of the package whose Command and CommandContext functions start a program.
const execPackage = "os/exec"

// gitRunnerSites returns "file:line" for each place in the Go files of root that makes a runner or a command for the git program. The program is the literal
// "git", the name ToolGit, or a variable or constant that the same folder assigns from one of them. The type checker resolves each name to its own object,
// so a block, a closure parameter or another function does not mix up two names of the same spelling. A Command or CommandContext call counts when it
// resolves to os/exec, under any import name. The imports are stubs, so the walk does not see a program that comes from a function result, a field, a
// parameter, a slice element, a selector into another folder, a raw string, a concatenation or an unkeyed ExecRunner literal, or a package-level name
// that a function of another folder assigns.
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

// gitRunnerSitesIn returns the sites of the non-test Go files in one folder, one package at a time.
func gitRunnerSitesIn(t *testing.T, folder string) []string {
	t.Helper()

	fset := token.NewFileSet()

	var found []string

	for _, files := range parseFolder(t, fset, folder) {
		info := typeCheck(fset, files)
		assigned := map[types.Object][]ast.Expr{}

		for _, file := range files {
			collectAssigned(file, info, assigned)
		}

		for _, file := range files {
			ast.Inspect(file, func(node ast.Node) bool {
				for _, program := range programsOf(node, info) {
					if namesGit(program, info, assigned, map[types.Object]bool{}) {
						found = append(found, fset.Position(program.Pos()).String())
					}
				}

				return true
			})
		}
	}

	return found
}

// parseFolder parses the non-test Go files of a folder and groups them by package name.
func parseFolder(t *testing.T, fset *token.FileSet, folder string) map[string][]*ast.File {
	t.Helper()

	paths, err := filepath.Glob(filepath.Join(folder, "*.go"))
	require.NoError(t, err)

	packages := map[string][]*ast.File{}

	for _, name := range paths {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, err)

		packages[file.Name.Name] = append(packages[file.Name.Name], file)
	}

	return packages
}

// typeCheck resolves the names of the files of one package. Every import is a stub, so the checker reports each name it cannot find; the walk reads only
// the facts that the checker records in spite of those errors.
func typeCheck(fset *token.FileSet, files []*ast.File) *types.Info {
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}}
	config := types.Config{Importer: stubImporter{}, Error: ignoreError}

	config.Check(files[0].Name.Name, fset, files, info) //nolint:errcheck,gosec // the returned error is the first one that ignoreError drops

	return info
}

// ignoreError drops a type error; the stubbed imports cause them.
func ignoreError(error) {}

// stubImporter gives each import an empty package, and os/exec the two functions that the walk looks for.
type stubImporter struct{}

func (stubImporter) Import(importPath string) (*types.Package, error) {
	pkg := types.NewPackage(importPath, path.Base(importPath))

	if importPath == execPackage {
		for _, name := range []string{"Command", "CommandContext"} {
			signature := types.NewSignatureType(nil, nil, nil, nil, nil, false)
			pkg.Scope().Insert(types.NewFunc(token.NoPos, pkg, name, signature))
		}
	}

	pkg.MarkComplete()

	return pkg, nil
}

// collectAssigned records, for each variable or constant that the node assigns, the expressions that it gets.
func collectAssigned(root ast.Node, info *types.Info, assigned map[types.Object][]ast.Expr) {
	ast.Inspect(root, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.AssignStmt:
			assign(info, assigned, n.Lhs, n.Rhs)
		case *ast.ValueSpec:
			names := make([]ast.Expr, len(n.Names))
			for i, name := range n.Names {
				names[i] = name
			}

			assign(info, assigned, names, n.Values)
		}

		return true
	})
}

// assign records each right side under the object of the name on the left at the same place, when both sides have the same length.
func assign(info *types.Info, assigned map[types.Object][]ast.Expr, left, right []ast.Expr) {
	if len(left) != len(right) {
		return
	}

	for i, target := range left {
		if ident, ok := target.(*ast.Ident); ok && info.ObjectOf(ident) != nil {
			assigned[info.ObjectOf(ident)] = append(assigned[info.ObjectOf(ident)], right[i])
		}
	}
}

// programsOf returns the expressions that name the program in a node that makes a runner or a command: the Binary field of an ExecRunner, the first argument
// of NewExecRunner, the second of GroupCommand, the first of Command and the second of CommandContext when the call resolves to os/exec.
func programsOf(node ast.Node, info *types.Info) []ast.Expr {
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

		return execProgramOf(n, info)
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

// execProgramOf returns the program argument of a call that the type checker resolves to Command or CommandContext of os/exec.
func execProgramOf(call *ast.CallExpr, info *types.Info) []ast.Expr {
	var callee *ast.Ident

	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		callee = fun.Sel
	case *ast.Ident:
		callee = fun
	}

	function, ok := info.Uses[callee].(*types.Func)
	if !ok || function.Pkg() == nil || function.Pkg().Path() != execPackage {
		return nil
	}

	switch function.Name() {
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

// namesGit reports whether the expression is the program git: the literal, ToolGit, or a name whose object is assigned one of them.
func namesGit(expr ast.Expr, info *types.Info, assigned map[types.Object][]ast.Expr, seen map[types.Object]bool) bool {
	switch e := expr.(type) {
	case *ast.BasicLit:
		return e.Value == `"git"`
	case *ast.SelectorExpr:
		return e.Sel.Name == "ToolGit"
	case *ast.Ident:
		return e.Name == "ToolGit" || objectNamesGit(info.ObjectOf(e), info, assigned, seen)
	}

	return false
}

// objectNamesGit reports whether one of the values that the folder assigns to the object is the program git.
func objectNamesGit(object types.Object, info *types.Info, assigned map[types.Object][]ast.Expr, seen map[types.Object]bool) bool {
	if object == nil || seen[object] {
		return false
	}

	seen[object] = true

	for _, value := range assigned[object] {
		if namesGit(value, info, assigned, seen) {
			return true
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

func TestTheGitSourceTestSeesAnImportAliasAndAPackageLevelName(t *testing.T) {
	t.Parallel()

	use := "func f() {\n_ = proc.NewExecRunner(name, dir)\n}\n"

	folders := map[string]map[string]string{
		"an aliased os/exec package": {"p.go": "package p\n\nimport osexec \"os/exec\"\n\nfunc f() {\n_ = osexec.Command(\"git\")\n}\n"},
		"a dot import of os/exec":    {"p.go": "package p\n\nimport . \"os/exec\"\n\nfunc f() {\n_ = Command(\"git\")\n}\n"},
		"a package level variable":   {"p.go": "package p\n\nvar name = \"git\"\n\n" + use},
		"a name that init sets":      {"p.go": "package p\n\nvar name string\n\nfunc init() {\nname = \"git\"\n}\n\n" + use},
		"a name that a function in another file sets": {
			"p.go": "package p\n\nvar name string\n\n" + use,
			"q.go": "package p\n\nfunc setup() {\nname = \"git\"\n}\n",
		},
		"a name that one function sets and another runs": {
			"p.go": "package p\n\nvar name = \"x\"\n\nfunc setup() {\nname = \"git\"\n}\n\n" + use,
		},
		"a package level name that an inner block hides before the call": {
			"p.go": "package p\n\nimport \"os/exec\"\n\nvar name = \"git\"\n\nfunc f() {\nif true {\nname := \"x\"\n_ = name\n}\n_ = exec.Command(name)\n}\n",
		},
		"a package level name that a closure parameter hides before the call": {
			"p.go": "package p\n\nimport \"os/exec\"\n\nvar name = \"git\"\n\nfunc f() {\ng := func(name string) {}\ng(\"x\")\n_ = exec.Command(name)\n}\n",
		},
	}

	for name, sources := range folders {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			for file, source := range sources {
				require.NoError(t, os.WriteFile(filepath.Join(root, file), []byte(source), 0o600))
			}

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
		"a Command call that is not os/exec":          "package p\n\nimport \"github.com/spf13/cobra\"\n\nfunc f() {\n_ = cobra.Command(\"git\")\n_ = c.Command(\"git\")\n}\n",
		"a Command function of the package itself":    "package p\n\nfunc Command(name string) {}\n\nfunc f() {\nCommand(\"git\")\n}\n",
		"one name for git in another function":        "package p\n\nfunc f() {\nname := \"git\"\n_ = name\n}\n\nfunc g() {\nname := \"gh\"\n_ = proc.NewExecRunner(name, dir)\n}\n",
		"a local that hides a package level name":     "package p\n\nvar name = \"git\"\n\nfunc g() {\nname := \"gh\"\n_ = proc.NewExecRunner(name, dir)\n}\n",
		"a parameter that hides a package level name": "package p\n\nvar name = \"git\"\n\nfunc g(name string) {\n_ = proc.NewExecRunner(name, dir)\n}\n",
		"a Command function without a dot import":     "package p\n\nfunc f() {\n_ = Command(\"git\")\n}\n",
		"a local git that another function does not see": "package p\n\nimport \"os/exec\"\n\nvar name = \"gh\"\n\nfunc f() {\nname := \"git\"\n_ = name\n}\n\n" +
			"func g() {\n_ = exec.Command(name)\n}\n",
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
