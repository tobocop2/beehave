package flow

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/agent"
)

const papersScope = `allow_paths = ["papers/**"], `

func TestAgentStep_AllowPathsNamesAnEditOfAFileInsideAnIgnoredFolderThatWasThereBefore(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.leavesFilesBeforeTheRun(t)
	rig.h.agents.result = func(AgentRequest) agent.Result {
		rig.put(filepath.Join(rig.dir, "build/cache.o"), "a longer body than before")
		rig.put(filepath.Join(rig.dir, "papers/new.md"), "x")

		return exited
	}

	_, err := rig.run(t, agentFlow(papersScope), "x")

	require.ErrorContains(t, err, "build/cache.o")
	assert.NotContains(t, err.Error(), "run.log")
}

func TestScope_ARewriteOfAnIgnoredFileWithTheSameSizeAndANewTimeIsNamed(t *testing.T) {
	t.Parallel()

	dir, _ := gitTree(t)
	putUnder(t, dir, ".gitignore", "build/\n")
	gitIn(t, dir, "add", ".gitignore")
	gitIn(t, dir, "commit", "-q", "-m", "two")
	putUnder(t, dir, "build/cache.o", "x")
	putUnder(t, dir, "build/other.o", "x")
	start := markOf(t, dir)
	later := time.Now().Add(time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(dir, "build/cache.o"), later, later))

	bad, err := scopeOf(t, "papers/**").outside(t.Context(), engineGit(dir), start)

	require.NoError(t, err)
	assert.Equal(t, []string{"build/cache.o"}, bad)
}

func TestAgentStep_AllowPathsAcceptsAWriteInsideAKnownBuildCacheThatWasThereBefore(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.leavesFilesBeforeTheRun(t)
	rig.put(filepath.Join(rig.dir, ".git/info/exclude"), "node_modules/\n")
	rig.put(filepath.Join(rig.dir, "node_modules/pkg/a.js"), "x")
	rig.writesIn("papers/new.md", "node_modules/pkg/b.js", "node_modules/other/c.js")

	_, err := rig.run(t, agentFlow(papersScope), "x")

	require.NoError(t, err)
}

func TestAgentStep_AllowPathsNamesAKnownBuildCacheFolderThatTheAgentCreated(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.leavesFilesBeforeTheRun(t)
	rig.put(filepath.Join(rig.dir, ".git/info/exclude"), "node_modules/\n")
	rig.writesIn("papers/new.md", "node_modules/pkg/b.js")

	_, err := rig.run(t, agentFlow(papersScope), "x")

	require.ErrorContains(t, err, "node_modules/")
	assert.NotContains(t, err.Error(), "b.js")
}

func TestScope_OnlyAFileInsideAKnownBuildCacheFolderIsOneEntryOfTheFolder(t *testing.T) {
	t.Parallel()

	for name, want := range map[string]string{
		"node_modules/a/b.js":       "node_modules/",
		"pkg/__pycache__/x.pyc":     "pkg/__pycache__/",
		"a/.cache/b/c":              "a/.cache/",
		"build/o":                   "build/o",
		"a.log":                     "a.log",
		"docs/node_modules":         "docs/node_modules",
		"node_modules_extra/x.js":   "node_modules_extra/x.js",
		"src/node_modules/x/.cache": "src/node_modules/",
	} {
		assert.Equal(t, want, collapse(name), name)
	}
}
