package hosttest_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hostImportRoot is the import path of the folder that holds the host packages.
const hostImportRoot = "github.com/tobocop2/beehave/internal/host/"

// suiteRuns lists the test functions in the test files of one package directory that call hosttest.TestHost.
func suiteRuns(t *testing.T, dir string) []string {
	t.Helper()

	files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	require.NoError(t, err)

	var runs []string

	for _, file := range files {
		tree, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		require.NoError(t, err)

		for _, decl := range tree.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && strings.HasPrefix(fn.Name.Name, "Test") && callsSuite(fn) {
				runs = append(runs, fn.Name.Name)
			}
		}
	}

	return runs
}

// importerSuiteRuns lists the test functions that call hosttest.TestHost in the test files of other host folders that import the provider package in dir.
// A provider that needs another provider to run its jobs (SkyPilot over Slurm) runs the suite there.
func importerSuiteRuns(t *testing.T, dir string) []string {
	t.Helper()

	files, err := filepath.Glob(filepath.Join("..", "*", "*_test.go"))
	require.NoError(t, err)

	path := strconv.Quote(hostImportRoot + filepath.Base(dir))

	var runs []string

	for _, file := range files {
		if filepath.Dir(file) == dir {
			continue
		}

		tree, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		require.NoError(t, err)

		if !importsPath(tree, path) {
			continue
		}

		for _, decl := range tree.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && strings.HasPrefix(fn.Name.Name, "Test") && callsSuite(fn) {
				runs = append(runs, fn.Name.Name)
			}
		}
	}

	return runs
}

// importsPath reports whether the file imports the quoted path.
func importsPath(tree *ast.File, quoted string) bool {
	for _, spec := range tree.Imports {
		if spec.Path.Value == quoted {
			return true
		}
	}

	return false
}

// callsSuite reports whether the body of fn calls hosttest.TestHost.
func callsSuite(fn *ast.FuncDecl) bool {
	found := false

	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "TestHost" {
			return true
		}

		if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "hosttest" {
			found = true
		}

		return true
	})

	return found
}

// providerDirs lists the package folders next to hosttest that declare a host provider: a constant Name of the type model.HostProvider.
func providerDirs(t *testing.T) []string {
	t.Helper()

	files, err := filepath.Glob(filepath.Join("..", "*", "*.go"))
	require.NoError(t, err)

	var dirs []string

	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}

		tree, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		require.NoError(t, err)

		if declaresProvider(tree) && !slices.Contains(dirs, filepath.Dir(file)) {
			dirs = append(dirs, filepath.Dir(file))
		}
	}

	return dirs
}

// declaresProvider reports whether the file declares a constant Name of the type model.HostProvider.
func declaresProvider(tree *ast.File) bool {
	found := false

	ast.Inspect(tree, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok || len(spec.Names) != 1 || spec.Names[0].Name != "Name" {
			return true
		}

		if typ, ok := spec.Type.(*ast.SelectorExpr); ok && typ.Sel.Name == "HostProvider" {
			found = true
		}

		return true
	})

	return found
}

func TestEveryHostProviderRunsTheConformanceSuite(t *testing.T) {
	t.Parallel()

	dirs := providerDirs(t)
	require.NotEmpty(t, dirs, "the provider packages sit next to hosttest")

	for _, dir := range dirs {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			t.Parallel()

			runs := append(suiteRuns(t, dir), importerSuiteRuns(t, dir)...)
			assert.NotEmpty(t, runs, "the package %s has no test that calls hosttest.TestHost, here or in a folder that imports it", dir)
		})
	}
}

func TestDeclaresProvider_FindsOnlyAHostProviderName(t *testing.T) {
	t.Parallel()

	parse := func(src string) *ast.File {
		tree, err := parser.ParseFile(token.NewFileSet(), "x.go", src, 0)
		require.NoError(t, err)

		return tree
	}

	assert.True(t, declaresProvider(parse("package x\n\nconst Name model.HostProvider = \"x\"\n")))
	assert.False(t, declaresProvider(parse("package x\n\nconst Name model.Agent = \"x\"\n")))
	assert.False(t, declaresProvider(parse("package x\n\nconst Other model.HostProvider = \"x\"\n")))
}

func TestSuiteRuns_FindsOnlyATestThatCallsTheSuite(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	src := "package x\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) { hosttest.TestHost(t, nil) }\n\nfunc TestB(t *testing.T) {}\n\nfunc helper(t *testing.T) { hosttest.TestHost(t, nil) }\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "x_test.go"), []byte(src), 0o600))

	assert.Equal(t, []string{"TestA"}, suiteRuns(t, dir))
}

func TestImporterSuiteRuns_FindsTheSuiteOfAFolderThatImportsTheProvider(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"TestHost_ConformsToHost"}, importerSuiteRuns(t, filepath.Join("..", "skypilot")))
	assert.Empty(t, importerSuiteRuns(t, filepath.Join("..", "nowhere")))
}
