package workflow_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/workflow"
)

var (
	conformanceStatuses = []string{"gap", "expressed", "partly", "proven", "n/a"}
	conformanceHomes    = []string{"core", "file", "skill", "drop"}
)

const (
	conformanceColumns = 7
	exampleDir         = "../../examples/remediation"
	repoRoot           = "../.."
	// whereWords is the form of Where: the file, the block kind and the block name.
	whereWords = 3
	// noTest is the Test cell of a row that names no test.
	noTest = "-"
	// tableHead is the number of lines of the table that are not rows: the header and the separator.
	tableHead = 2
)

type conformanceRow struct {
	id, home, status, where, test string
}

var testFunc = regexp.MustCompile(`(?m)^func (\w+)\(`)

// testNames returns the name of every function declared in a `_test.go` file of the repository.
func testNames(t *testing.T) map[string]bool {
	t.Helper()

	root := os.DirFS(repoRoot)
	names := map[string]bool{}

	err := fs.WalkDir(root, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() && entry.Name() == ".git" {
			return fs.SkipDir
		}

		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}

		source, err := fs.ReadFile(root, path)

		for _, match := range testFunc.FindAllSubmatch(source, -1) {
			names[string(match[1])] = true
		}

		return err
	})
	require.NoError(t, err)

	return names
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

// blocks lists the blocks of the example as "<kind> <name>".
func blocks(t *testing.T) map[string]bool {
	t.Helper()

	flow, err := workflow.Load(os.DirFS(exampleDir), "workflow.hcl")
	require.NoError(t, err)

	found := map[string]bool{"workflow " + flow.Name: true}

	flow.Walk(func(step workflow.Step, _ int) {
		found[string(step.Kind())+" "+step.Label()] = true
	})

	return found
}

func TestConformance_EachRowIsWellFormed(t *testing.T) {
	t.Parallel()

	text, err := os.ReadFile(filepath.Join(exampleDir, "CONFORMANCE.md"))
	require.NoError(t, err)

	rows := readConformance(t, string(text))
	require.NotEmpty(t, rows)

	have := blocks(t)
	tests := testNames(t)
	seen := map[string]bool{}

	for _, row := range rows {
		require.False(t, seen[row.id], "the ID %s repeats", row.id)

		seen[row.id] = true

		require.Contains(t, conformanceStatuses, row.status, "the status of %s", row.id)
		require.Contains(t, conformanceHomes, row.home, "the home of %s", row.id)

		if row.test == noTest {
			require.NotEqual(t, "proven", row.status, "%s is proven and names no test", row.id)
		} else {
			require.True(t, tests[row.test], "%s names the test %s, which no `_test.go` file holds", row.id, row.test)
		}

		if row.status != "expressed" {
			continue
		}

		words := strings.Fields(row.where)
		require.Len(t, words, whereWords, "Where of %s is `workflow.hcl <kind> <name>`", row.id)
		require.Equal(t, "workflow.hcl", words[0], "the file of %s", row.id)
		require.True(t, have[words[1]+" "+words[2]], "%s names the block `%s %s`, which the example does not hold", row.id, words[1], words[2])
	}
}
