package flow

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"

	"github.com/tobocop2/obee/internal/agent"
)

// writesIn makes the fake agent write the files in the item folder, relative to it, then end with exited.
func (r *agentRig) writesIn(names ...string) {
	r.h.agents.result = func(AgentRequest) agent.Result {
		for _, name := range names {
			r.put(filepath.Join(r.dir, name), "x")
		}

		return exited
	}
}

func TestAgentStep_AllowPathsRefusesEachPathOutsideTheGlobsAndNamesIt(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.writesIn("papers/a.md", "src/lib.go", "src/deep/b.go")

	_, err := rig.run(t, agentFlow(`allow_paths = ["papers/**"], `), "x")

	require.ErrorContains(t, err, "src/lib.go")
	require.ErrorContains(t, err, "src/deep/b.go")
	assert.NotContains(t, err.Error(), "papers/a.md")
}

func TestAgentStep_AllowPathsAcceptsARunThatChangesOnlyAllowedPaths(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.writesIn("papers/a.md", "papers/deep/b.md", "design/c.svg")

	_, err := rig.run(t, agentFlow(`allow_paths = ["papers/**", "design/*.svg"], `), "x")

	require.NoError(t, err)
}

func TestAgentStep_AllowPathsKeepsANameWithASpaceWhole(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.writesIn("papers/my paper.md", "src/my file.go")

	_, err := rig.run(t, agentFlow(`allow_paths = ["papers/**"], `), "x")

	require.ErrorContains(t, err, "src/my file.go")
	assert.NotContains(t, err.Error(), "papers/my paper.md")
}

func TestAgentStep_AllowPathsSeesACommitOutsideTheGlobs(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.agents.result = func(AgentRequest) agent.Result {
		rig.put(filepath.Join(rig.dir, "src/x.go"), "x")
		gitIn(t, rig.dir, "add", "src/x.go")
		gitIn(t, rig.dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "two")

		return exited
	}

	_, err := rig.run(t, agentFlow(`allow_paths = ["papers/**"], `), "x")

	require.ErrorContains(t, err, "src/x.go")
}

func TestAgentStep_AllowPathsRefusesAGlobThatDoesNotParseAndANonList(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	_, err := rig.run(t, agentFlow(`allow_paths = ["["], `), "x")
	require.ErrorContains(t, err, "allow_paths has the pattern")

	rig = newAgentRig(t)
	_, err = rig.run(t, agentFlow("allow_paths = 3 + 0,"), "x")
	require.ErrorContains(t, err, "list of strings")
}

func TestAgentStep_AllowPathsFailsWhenTheItemFolderHasNoCommitToStartFrom(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.dir = t.TempDir()
	rig.h.trees = &fakeTrees{dir: rig.dir}

	_, err := rig.run(t, agentFlow(`allow_paths = ["papers/**"], `), "x")

	require.ErrorContains(t, err, "read HEAD of the item folder")
}

func TestScope_ChecksReportAFolderGitCannotListAgainstTheStartCommit(t *testing.T) {
	t.Parallel()

	dir, _ := gitTree(t)
	only, err := readScope(starlark.NewList([]starlark.Value{starlark.String("papers/**")}))
	require.NoError(t, err)

	_, err = only.outside(t.Context(), dir, startMark{Head: "not-a-commit"})
	require.ErrorContains(t, err, "list the changed paths of the item folder with git diff")

	err = only.check(t.Context(), dir, startMark{Head: "not-a-commit"})
	require.ErrorContains(t, err, "with git diff")
}
