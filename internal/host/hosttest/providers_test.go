package hosttest_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
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

func TestEveryHostProviderRunsTheConformanceSuite(t *testing.T) {
	t.Parallel()

	for _, provider := range []model.HostProvider{model.HostSkyPilot, model.HostSSH, model.HostSlurm} {
		t.Run(string(provider), func(t *testing.T) {
			t.Parallel()

			dir := filepath.Join("..", string(provider))
			_, err := os.Stat(dir)
			require.NoError(t, err, "a provider package sits next to hosttest, named like the provider")

			runs := append(suiteRuns(t, dir), importerSuiteRuns(t, dir)...)
			assert.NotEmpty(t, runs, "the package of provider %q has no test that calls hosttest.TestHost, here or in a folder that imports it", provider)
		})
	}
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
