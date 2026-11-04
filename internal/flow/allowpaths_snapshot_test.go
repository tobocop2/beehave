package flow

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
)

// leavesFilesBeforeTheRun commits a .gitignore, then leaves an ignored file, an ignored folder and an untracked file outside the globs.
func (r *agentRig) leavesFilesBeforeTheRun(t *testing.T) {
	t.Helper()

	r.put(filepath.Join(r.dir, ".gitignore"), "*.log\nbuild/\n")
	gitIn(t, r.dir, "add", ".gitignore")
	gitIn(t, r.dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "ignore")
	r.put(filepath.Join(r.dir, "run.log"), "x")
	r.put(filepath.Join(r.dir, "build/cache.o"), "x")
	r.put(filepath.Join(r.dir, "notes.txt"), "x")
}

// markOf takes the start mark of a folder now.
func markOf(t *testing.T, dir string) startMark {
	t.Helper()

	mark, err := takeMark(t.Context(), dir)
	require.NoError(t, err)

	return mark
}

func TestScope_TheStartMarkNamesTheErrorWhenGitCannotListTheFolder(t *testing.T) {
	t.Parallel()

	_, err := others(t.Context(), t.TempDir())
	require.ErrorContains(t, err, "find the root of the item folder")

	dir, _ := gitTree(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "index"), []byte("not an index"), 0o600))

	_, err = takeMark(t.Context(), dir)
	require.ErrorContains(t, err, "with git ls-files")

	_, err = scopeOf(t, "papers/**").outside(t.Context(), dir, startMark{Head: "HEAD"})
	require.ErrorContains(t, err, "with git ls-files")
}

func TestScope_AFileThatGitListsAndThatIsGoneHasTheStampGone(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "gone", stamp(t.TempDir(), "missing.txt", false))
	assert.Empty(t, stamp(t.TempDir(), "missing/", true))
}

func TestScope_ARecordedNoteThatIsNotAStartMarkIsRefused(t *testing.T) {
	t.Parallel()

	_, err := parseMark("0123abcd")
	require.ErrorContains(t, err, "read the recorded start of the item folder")
}

func TestAgentStep_AllowPathsIgnoresFilesThatWereThereBeforeTheRun(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.leavesFilesBeforeTheRun(t)
	rig.writesIn("papers/new.md")

	_, err := rig.run(t, agentFlow(`allow_paths = ["papers/**"], `), "x")

	require.NoError(t, err)
}

func TestAgentStep_AllowPathsNamesAnUntrackedFileThatTheAgentDeleted(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.leavesFilesBeforeTheRun(t)
	rig.h.agents.result = func(AgentRequest) agent.Result {
		require.NoError(t, os.Remove(filepath.Join(rig.dir, "notes.txt")))
		rig.put(filepath.Join(rig.dir, "papers/new.md"), "x")

		return exited
	}

	_, err := rig.run(t, agentFlow(`allow_paths = ["papers/**"], `), "x")

	require.ErrorContains(t, err, "notes.txt")
	assert.NotContains(t, err.Error(), "run.log")
}

func TestAgentStep_AllowPathsNamesAnUntrackedFileThatTheAgentEdited(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.leavesFilesBeforeTheRun(t)
	rig.h.agents.result = func(AgentRequest) agent.Result {
		rig.put(filepath.Join(rig.dir, "notes.txt"), "a longer body than before")
		rig.put(filepath.Join(rig.dir, "papers/new.md"), "x")

		return exited
	}

	_, err := rig.run(t, agentFlow(`allow_paths = ["papers/**"], `), "x")

	require.ErrorContains(t, err, "notes.txt")
	assert.NotContains(t, err.Error(), "run.log")
	assert.NotContains(t, err.Error(), "build")
}

func TestAgentStep_AllowPathsNamesAnIgnoredFolderThatTheAgentCreated(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.leavesFilesBeforeTheRun(t)
	rig.put(filepath.Join(rig.dir, ".git/info/exclude"), "target/\n")
	rig.writesIn("papers/new.md", "build/more.o", "target/x.o", "other.log")

	_, err := rig.run(t, agentFlow(`allow_paths = ["papers/**"], `), "x")

	require.ErrorContains(t, err, "target/")
	require.ErrorContains(t, err, "other.log")
	assert.NotContains(t, err.Error(), "build")
}

func TestAgentStep_AllowPathsKeepsTheSnapshotOfTheFirstStartWhenTheRunResumes(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.leavesFilesBeforeTheRun(t)

	var arm func()

	rig.h.agents.result = func(AgentRequest) agent.Result {
		rig.put(filepath.Join(rig.dir, "late.txt"), "x")
		arm()

		return exited
	}

	file := writeFlow(t, agentFlow(`allow_paths = ["papers/**"], `))
	require.NoError(t, os.MkdirAll(filepath.Join(filepath.Dir(file), "prompts"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(file), "prompts", "a.md"), []byte("x"), 0o600))

	rig.crashItemAfterThePhaseEndedArmedBy(t, testItem, file, func(a func()) { arm = a })

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.ErrorContains(t, err, "late.txt", "the resume compares with the snapshot of the first start")
	assert.NotContains(t, err.Error(), "notes.txt")
}
