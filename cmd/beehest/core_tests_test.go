package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

// guardFile is this file. It names the examples folder to test for it, so the scan skips it.
const guardFile = "cmd/beehest/core_tests_test.go"

// coreTestRoots are the folders whose test files make up the core tests.
func coreTestRoots() []string { return []string{"internal", "cmd/beehest"} }

// allowedExampleReaders lists the core test files that read the examples folder. Each pull request that moves a test removes its entry.
func allowedExampleReaders() []string {
	return []string{
		"cmd/beehest/e2e_desk_test.go",
		"cmd/beehest/e2e_incident_test.go",
		"cmd/beehest/e2e_local_test.go",
		"cmd/beehest/e2e_maint_test.go",
		"cmd/beehest/e2e_pipeline_test.go",
		"cmd/beehest/e2e_researchdesk_test.go",
		"cmd/beehest/e2e_swarm_test.go",
		"cmd/beehest/e2e_test.go",
		"cmd/beehest/merge_test.go",
		"cmd/beehest/tree_names_test.go",
		"internal/cli/check_workflow_dryrun_test.go",
		"internal/flow/capguards_test.go",
		"internal/flow/dryrun_test.go",
		"internal/flow/dryrun_void_test.go",
		"internal/flow/example_batch_test.go",
		"internal/flow/example_maintenance_test.go",
		"internal/flow/example_pipeline_test.go",
		"internal/flow/example_planner_test.go",
		"internal/flow/example_round_test.go",
		"internal/flow/example_settings_test.go",
		"internal/flow/example_supervisor_test.go",
		"internal/flow/example_take_test.go",
		"internal/flow/example_test.go",
		"internal/flow/examples_use_test.go",
		"internal/flow/patched_test.go",
		"internal/flow/queueloop_test.go",
		"internal/flow/restart_text_test.go",
		"internal/flow/round_rig_test.go",
	}
}

// namesExamplesFolder is true when a string literal holds a path element equal to examples.
func namesExamplesFolder(literal string) bool {
	return slices.Contains(strings.Split(literal, "/"), "examples")
}

// fileReadsExamples is true when the Go file p of tree holds a string literal that names the examples folder.
func fileReadsExamples(tree fs.FS, p string) (bool, error) {
	src, err := fs.ReadFile(tree, p)
	if err != nil {
		return false, err
	}
	file, err := parser.ParseFile(token.NewFileSet(), p, src, parser.SkipObjectResolution)
	if err != nil {
		return false, err
	}
	reads := false
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if ok && lit.Kind == token.STRING {
			reads = reads || namesExamplesFolder(strings.Trim(lit.Value, "\"`"))
		}
		return true
	})
	return reads, nil
}

// examplesReaders returns the sorted paths of the core test files in tree that hold a string literal naming the examples folder.
// Comments and identifiers are not literals, so a file that names the folder only there is not a reader.
func examplesReaders(tree fs.FS) ([]string, error) {
	files, err := coreTestFiles(tree)
	if err != nil {
		return nil, err
	}
	var readers []string
	for _, p := range files {
		reads, err := fileReadsExamples(tree, p)
		if err != nil {
			return nil, err
		}
		if reads {
			readers = append(readers, p)
		}
	}
	slices.Sort(readers)
	return readers, nil
}

// coreTestFiles returns the paths of the test files under the core roots of tree, without testdata folders and without the guard file.
func coreTestFiles(tree fs.FS) ([]string, error) {
	var files []string
	for _, root := range coreTestRoots() {
		err := fs.WalkDir(tree, root, func(p string, d fs.DirEntry, err error) error {
			switch {
			case err != nil:
				return err
			case d.IsDir() && d.Name() == "testdata":
				return fs.SkipDir
			case !d.IsDir() && strings.HasSuffix(p, "_test.go") && p != guardFile:
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}

// readerDrift returns the files that read the examples folder but are not allowed, and the allowed files that no longer read it.
func readerDrift(found, allowed []string) (unlisted, stale []string) {
	for _, f := range found {
		if !slices.Contains(allowed, f) {
			unlisted = append(unlisted, f)
		}
	}
	for _, a := range allowed {
		if !slices.Contains(found, a) {
			stale = append(stale, a)
		}
	}
	return unlisted, stale
}

func TestCore_NoCoreTestReadsTheExamplesFolder(t *testing.T) {
	t.Parallel()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	found, err := examplesReaders(os.DirFS(root))
	require.NoError(t, err)
	unlisted, stale := readerDrift(found, allowedExampleReaders())
	require.Empty(t, unlisted, "a core test reads the examples folder: put the test under examples/ or use a fixture in testdata")
	require.Empty(t, stale, "the file no longer reads the examples folder: remove its entry from allowedExampleReaders")
}

func TestExamplesReaders_FindsAFileWithAnExamplesElementInAStringLiteral(t *testing.T) {
	t.Parallel()
	tree := fstest.MapFS{
		"internal/a/joined_test.go":  {Data: []byte("package a\nvar _ = filepath.Join(\"..\", \"examples\", \"x\")\n")},
		"internal/a/slashed_test.go": {Data: []byte("package a\nvar _ = \"../../examples/local/project.star\"\n")},
		"internal/b/raw_test.go":     {Data: []byte("package b\nvar _ = `examples/desk`\n")},
		"cmd/beehest/c_test.go":      {Data: []byte("package main\nvar _ = \"examples\"\n")},
	}
	got, err := examplesReaders(tree)
	require.NoError(t, err)
	require.Equal(t, []string{"cmd/beehest/c_test.go", "internal/a/joined_test.go", "internal/a/slashed_test.go", "internal/b/raw_test.go"}, got)
}

func TestExamplesReaders_SkipsCommentsIdentifiersNeighbourWordsTestdataAndTheGuard(t *testing.T) {
	t.Parallel()
	tree := fstest.MapFS{
		"internal/a/comment_test.go":         {Data: []byte("package a\n// reads examples/local\nvar examples = 1\n")},
		"internal/a/word_test.go":            {Data: []byte("package a\nvar _ = \"counterexamples\"\nvar _ = \"see examples here\"\nvar _ = 7\n")},
		"internal/a/testdata/x_test.go":      {Data: []byte("package a\nvar _ = \"examples\"\n")},
		"internal/a/notest.go":               {Data: []byte("package a\nvar _ = \"examples\"\n")},
		"cmd/beehest/core_tests_test.go":     {Data: []byte("package main\nvar _ = \"examples\"\n")},
		"cmd/beehest/testdata/e2e/x_test.go": {Data: []byte("package e2e\nvar _ = \"examples\"\n")},
	}
	got, err := examplesReaders(tree)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestExamplesReaders_ReturnsTheParseErrorOfATestFile(t *testing.T) {
	t.Parallel()
	tree := fstest.MapFS{
		"internal/a/broken_test.go": {Data: []byte("package a\nvar = \n")},
		"cmd/beehest/ok_test.go":    {Data: []byte("package main\n")},
	}
	_, err := examplesReaders(tree)
	require.ErrorContains(t, err, path.Join("internal", "a", "broken_test.go"))
}

func TestExamplesReaders_ReturnsTheErrorOfAMissingRoot(t *testing.T) {
	t.Parallel()
	_, err := examplesReaders(fstest.MapFS{"internal/a/ok_test.go": {Data: []byte("package a\n")}})
	require.ErrorIs(t, err, fs.ErrNotExist)
}

func TestReaderDrift_NamesAReadOutsideTheListAndAStaleEntry(t *testing.T) {
	t.Parallel()
	unlisted, stale := readerDrift([]string{"a_test.go", "new_test.go"}, []string{"a_test.go", "gone_test.go"})
	require.Equal(t, []string{"new_test.go"}, unlisted)
	require.Equal(t, []string{"gone_test.go"}, stale)

	unlisted, stale = readerDrift([]string{"a_test.go"}, []string{"a_test.go"})
	require.Empty(t, unlisted)
	require.Empty(t, stale)
}

func TestAllowedExampleReaders_ListsEachFileOnceInOrder(t *testing.T) {
	t.Parallel()
	list := allowedExampleReaders()
	require.True(t, slices.IsSorted(list), "keep the list sorted")
	require.Len(t, slices.Compact(slices.Clone(list)), len(list), "a file is listed twice")
}
