package main

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

// The engine names no method. This test walks the files git lists outside examples/ and fails on a name that belongs to the method of one owner.
// Each name is spelled from fragments, so this file does not match itself.
var forbiddenNames = []*regexp.Regexp{
	regexp.MustCompile("(?i)blast" + "-radius-" + "review"),
	regexp.MustCompile("(?i)diff-" + "against-" + "oracle"),
	regexp.MustCompile("(?i)orchestrated-" + "remedia" + "tion"),
	regexp.MustCompile("(?i)cross-" + "cutting-" + "api-change"),
	regexp.MustCompile("(?i)systems-" + "au" + "dit"),
	regexp.MustCompile("(?i)pr-" + "history-" + "cleanup"),
	regexp.MustCompile("(?i)plan-" + "vs-" + "conventions"),
	regexp.MustCompile("(?i)cloud-" + "ho" + "st"),
	regexp.MustCompile("(?i)verify-" + "backlog-" + "against-merge"),
	regexp.MustCompile("(?i)documentation-" + "polish" + "ing"),
	regexp.MustCompile("(?i)multi-" + "la" + "ng"),
	regexp.MustCompile("(?i)ci-" + "fi" + "x"),
	regexp.MustCompile("(?i)brood" + "bee"),
	regexp.MustCompile("(?i)lang" + "graph"),
	regexp.MustCompile(`(?i)\bpy` + `thon\b`),
	regexp.MustCompile(`[A-Za-z0-9_]\.p` + `y\b`),
}

// namesIn returns one line for each forbidden name that the file holds. A binary file holds none.
func namesIn(tree fs.FS, rel string) ([]string, error) {
	data, err := fs.ReadFile(tree, rel)
	if err != nil {
		return nil, err
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return nil, nil
	}
	var hits []string
	for _, name := range forbiddenNames {
		if name.Match(data) {
			hits = append(hits, rel+" matches "+name.String())
		}
	}
	return hits, nil
}

func TestNamesIn_MatchesEveryNameInAnyCase(t *testing.T) {
	t.Parallel()

	for _, word := range []string{"Brood" + "bee", "BROOD" + "BEE", "Py" + "thon", "PY" + "THON", "Ci-" + "Fix", "Blast-" + "Radius-Review"} {
		tree := fstest.MapFS{"note.md": {Data: []byte("written in " + word + " here")}}
		hits, err := namesIn(tree, "note.md")
		require.NoError(t, err)
		require.NotEmpty(t, hits, word)
	}
}

func TestNamesIn_LeavesPlainWordsAlone(t *testing.T) {
	t.Parallel()

	tree := fstest.MapFS{"note.md": {Data: []byte("a pythonic style, no names")}}
	hits, err := namesIn(tree, "note.md")
	require.NoError(t, err)
	require.Empty(t, hits)
}

// trackedFiles lists the files git tracks, plus the files it would not ignore, outside examples/.
func trackedFiles(t *testing.T, root string) []string {
	t.Helper()
	list := exec.CommandContext(t.Context(), "git", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	list.Dir = root
	out, err := list.Output()
	require.NoError(t, err)
	var files []string
	for _, rel := range strings.Split(string(out), "\x00") {
		if rel == "" || rel == "examples" || strings.HasPrefix(rel, "examples/") {
			continue
		}
		files = append(files, rel)
	}
	return files
}

func TestTree_HoldsNoNameOfTheMethod(t *testing.T) {
	t.Parallel()

	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	tree := os.DirFS(root)

	var hits []string
	for _, rel := range trackedFiles(t, root) {
		info, statErr := fs.Stat(tree, rel)
		if errors.Is(statErr, fs.ErrNotExist) || (statErr == nil && !info.Mode().IsRegular()) {
			continue
		}
		require.NoError(t, statErr)
		found, readErr := namesIn(tree, rel)
		require.NoError(t, readErr)
		hits = append(hits, found...)
	}
	require.Empty(t, hits, "the engine names the method of one owner:\n%s", strings.Join(hits, "\n"))
}
