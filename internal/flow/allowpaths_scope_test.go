package flow

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"

	"github.com/tobocop2/beehave/internal/agent"
)

// scopeOf reads a scope from globs.
func scopeOf(t *testing.T, globs ...string) scope {
	t.Helper()

	values := make([]starlark.Value, 0, len(globs))
	for _, g := range globs {
		values = append(values, starlark.String(g))
	}

	s, err := readScope(starlark.NewList(values))
	require.NoError(t, err)

	return s
}

// putUnder writes a file with the given body under dir, with its folders.
func putUnder(t *testing.T, dir, name, body string) {
	t.Helper()

	path := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
}

func TestScope_ARenameIntoTheGlobsStillNamesTheSourceItRemoved(t *testing.T) {
	t.Parallel()

	dir, _ := gitTree(t)
	putUnder(t, dir, "src/a.go", "x")
	gitIn(t, dir, "add", "src/a.go")
	gitIn(t, dir, "commit", "-q", "-m", "two")
	start := markOf(t, dir)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "papers"), 0o750))
	gitIn(t, dir, "mv", "src/a.go", "papers/a.go")

	bad, err := scopeOf(t, "papers/**").outside(t.Context(), dir, start)

	require.NoError(t, err)
	assert.Equal(t, []string{"src/a.go"}, bad)
}

func TestAgentStep_AllowPathsUsesTheRecordedStartWhenTheRunResumesAfterHeadMoved(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)

	var arm func()

	rig.h.agents.result = func(AgentRequest) agent.Result {
		rig.put(filepath.Join(rig.dir, "src/x.go"), "x")
		gitIn(t, rig.dir, "add", "src/x.go")
		gitIn(t, rig.dir, "commit", "-q", "-m", "two")
		arm()

		return exited
	}

	file := writeFlow(t, agentFlow(`allow_paths = ["papers/**"], `))
	require.NoError(t, os.MkdirAll(filepath.Join(filepath.Dir(file), "prompts"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(file), "prompts", "a.md"), []byte("x"), 0o600))

	rig.crashItemAfterThePhaseEndedArmedBy(t, testItem, file, func(a func()) { arm = a })

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.ErrorContains(t, err, "src/x.go", "the resume measures from the recorded start, not from the moved HEAD")
}

func TestScope_AGlobFollowsTheDoublestarDialect(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		glob, name string
		want       bool
	}{
		{"**/*.md", "a.md", true},
		{"**/*.md", "x/y/a.md", true},
		{"docs/**", "docs/a", true},
		{"docs/**", "docs/a/b", true},
		{"./papers/**", "papers/a.md", true},
		{"papers/*", "papers/a/b", false},
		{"papers/*.md", "papers/a.md", true},
		{"papers/**", "src/a.md", false},
	} {
		assert.Equal(t, tc.want, scopeOf(t, tc.glob).allows(tc.name), "%s against %s", tc.glob, tc.name)
	}
}

func TestAgentStep_AllowPathsRefusesAnEmptyListAndSaysToLeaveTheKeywordOut(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	_, err := rig.run(t, agentFlow(`allow_paths = [], `), "x")

	require.ErrorContains(t, err, "allow_paths is an empty list")
	require.ErrorContains(t, err, "leave the keyword out")
}

func TestScope_AnIgnoredNewFileOutsideTheGlobsIsNamed(t *testing.T) {
	t.Parallel()

	dir, _ := gitTree(t)
	putUnder(t, dir, ".gitignore", "*.log\nbuild/\n")
	gitIn(t, dir, "add", ".gitignore")
	gitIn(t, dir, "commit", "-q", "-m", "two")
	start := markOf(t, dir)
	putUnder(t, dir, "x.log", "x")
	putUnder(t, dir, "build/o", "x")
	putUnder(t, dir, "papers/a.md", "x")

	bad, err := scopeOf(t, "papers/**").outside(t.Context(), dir, start)

	require.NoError(t, err)
	assert.Equal(t, []string{"build/", "x.log"}, bad)
}

func TestScope_EveryPathIsRelativeToTheRootWhenTheItemFolderIsASubdirectory(t *testing.T) {
	t.Parallel()

	root, _ := gitTree(t)
	putUnder(t, root, "papers/old.md", "x")
	gitIn(t, root, "add", "papers/old.md")
	gitIn(t, root, "commit", "-q", "-m", "two")
	start := markOf(t, root)
	putUnder(t, root, "papers/old.md", "changed")
	putUnder(t, root, "papers/new.md", "x")
	putUnder(t, root, "papers/new.go", "x")
	putUnder(t, root, "src/n.go", "x")

	bad, err := scopeOf(t, "papers/**/*.md").outside(t.Context(), filepath.Join(root, "papers"), start)

	require.NoError(t, err)
	assert.Equal(t, []string{"papers/new.go", "src/n.go"}, bad)
}
