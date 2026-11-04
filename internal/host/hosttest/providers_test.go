package hosttest_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
)

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

	for _, provider := range model.HostProviders() {
		t.Run(string(provider), func(t *testing.T) {
			t.Parallel()

			dir := filepath.Join("..", string(provider))
			_, err := os.Stat(dir)
			require.NoError(t, err, "a provider package sits next to hosttest, named like the provider")

			assert.NotEmpty(t, suiteRuns(t, dir), "the package of provider %q has no test that calls hosttest.TestHost", provider)
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
