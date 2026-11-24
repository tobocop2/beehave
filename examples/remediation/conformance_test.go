package remediation_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/workflow/workflowtest"
)

var (
	conformanceStatuses = []string{"gap", "expressed", "partly", "proven", "n/a"}
	conformanceHomes    = []string{"core", "file", "skill", "drop"}
)

const (
	conformanceColumns = 7
	exampleDir         = "."
	repoRoot           = "../.."
	// whereWords is the form of Where: the file, the builtin and the name of the call.
	whereWords = 3
	// noTest is the Test cell of a row that names no test.
	noTest = "-"
	// tableHead is the number of lines of the table that are not rows: the header and the separator.
	tableHead = 2
)

type conformanceRow struct {
	id, home, status, where, test string
}

// testNames returns the name of every top-level function that starts with `Test` and that a `_test.go` file of the
// repository declares. It reads the syntax tree, so a name in a comment or a string does not count, and it skips
// `.git` and every `testdata` folder, whose files are fixtures.
func testNames(t *testing.T) map[string]bool {
	t.Helper()

	root := os.DirFS(repoRoot)
	names := map[string]bool{}
	fset := token.NewFileSet()

	err := fs.WalkDir(root, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() && (entry.Name() == ".git" || entry.Name() == "testdata") {
			return fs.SkipDir
		}

		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}

		source, err := fs.ReadFile(root, path)
		if err != nil {
			return err
		}

		return addTestFuncs(fset, path, source, names)
	})
	require.NoError(t, err)

	return names
}

// addTestFuncs adds to names each top-level function of the source whose name starts with `Test`.
func addTestFuncs(fset *token.FileSet, path string, source []byte, names map[string]bool) error {
	file, err := parser.ParseFile(fset, path, source, parser.SkipObjectResolution)
	if err != nil {
		return err
	}

	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Test") {
			names[fn.Name.Name] = true
		}
	}

	return nil
}

func readConformance(t *testing.T, text string) []conformanceRow {
	t.Helper()

	var rows []conformanceRow

	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "| ") || strings.HasPrefix(line, "| ID ") {
			continue
		}

		cells := strings.Split(strings.Trim(line, "| "), "|")
		require.Len(t, cells, conformanceColumns, line)

		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}

		rows = append(rows, conformanceRow{id: cells[0], home: cells[2], status: cells[3], where: cells[4], test: cells[5]})
	}

	// A line that starts with `|` and is not a row must not vanish without notice.
	table := 0

	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "|") {
			table++
		}
	}

	require.Len(t, rows, table-tableHead, "every line of the table is a row")

	return rows
}

// calls lists the calls of the example as "<file> <builtin> <name>": the main file, and the review loop that it loads.
// A call is found by its keyword `name = "<label>"`.
func calls(t *testing.T) map[string]bool {
	t.Helper()

	found := map[string]bool{}

	for file, path := range map[string]string{"workflow.star": filepath.Join(exampleDir, "workflow.star"), "review_fix.star": filepath.Join(repoRoot, "internal", "workflow", "lib", "review_fix.star")} {
		text, err := os.ReadFile(path) //nolint:gosec // the test names its own example files
		require.NoError(t, err)

		listed, err := workflowtest.Calls(file, text)
		require.NoError(t, err)

		for _, call := range listed {
			for _, label := range workflowtest.Literals(call.Args["name"]) {
				found[file+" "+call.Builtin+" "+label] = true
			}
		}
	}

	return found
}

func TestConformance_EachRowIsWellFormed(t *testing.T) {
	t.Parallel()

	text, err := os.ReadFile(filepath.Join(exampleDir, "CONFORMANCE.md"))
	require.NoError(t, err)

	rows := readConformance(t, string(text))
	require.NotEmpty(t, rows)

	have := calls(t)
	seen := map[string]bool{}

	for _, row := range rows {
		require.False(t, seen[row.id], "the ID %s repeats", row.id)

		seen[row.id] = true

		require.Contains(t, conformanceStatuses, row.status, "the status of %s", row.id)
		require.Contains(t, conformanceHomes, row.home, "the home of %s", row.id)

		// That a named test exists is checked by TestConformance_EveryTestNameThatAnyColumnCitesExists.
		if row.test == noTest {
			require.NotEqual(t, "proven", row.status, "%s is proven and names no test", row.id)
		}

		if row.status != "expressed" {
			continue
		}

		words := strings.Fields(row.where)
		require.Len(t, words, whereWords, "Where of %s is `<file> <builtin> <name>`", row.id)
		require.True(t, have[row.where], "%s names the call `%s`, which the example does not hold", row.id, row.where)
	}
}

var citedTest = regexp.MustCompile(`\bTest[A-Z]\w*`)

// missingTests lists each test name that a line of the conformance text cites and that no `_test.go` file declares,
// as "<row>: <name>". A line outside the table counts under the row "prose".
func missingTests(text string, tests map[string]bool) []string {
	var missing []string

	for _, line := range strings.Split(text, "\n") {
		row := "prose"

		if strings.HasPrefix(line, "| ") {
			row, _, _ = strings.Cut(strings.TrimPrefix(line, "| "), " |")
		}

		for _, name := range citedTest.FindAllString(line, -1) {
			if !tests[name] {
				missing = append(missing, row+": "+name)
			}
		}
	}

	return missing
}

func TestConformance_EveryTestNameThatAnyColumnCitesExists(t *testing.T) {
	t.Parallel()

	text, err := os.ReadFile(filepath.Join(exampleDir, "CONFORMANCE.md"))
	require.NoError(t, err)

	require.Empty(t, missingTests(string(text), testNames(t)), "each name is cited by a row and has no top-level `func <name>(` in a `_test.go` file; a citation `TestX/sub` checks `TestX` only, not the subtest")
}

func TestConformance_ACitedNameThatNoTestDeclaresIsNamedWithItsRow(t *testing.T) {
	t.Parallel()

	tests := map[string]bool{"TestReal": true}

	assert.Empty(t, missingTests("| S1-X-01 | rule | core | proven | - | TestReal | S1-X-01 |", tests))
	assert.Equal(t, []string{"S1-X-01: TestMadeUp_ItIsNotThere"}, missingTests("| S1-X-01 | a (TestReal, TestMadeUp_ItIsNotThere) | core | proven | - | TestReal | S1-X-01 |", tests))
	assert.Equal(t, []string{"prose: TestGone"}, missingTests("A line outside the table cites TestGone.", tests))
}
