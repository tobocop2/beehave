package flow

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
)

// laneRig is the example rig with a lane kind and its globs. The step named by writer also writes the given files in the worktree.
func laneRig(t *testing.T, kind, globs string, verdicts func(int) string, writer string, files ...string) (*agentRig, string) {
	t.Helper()

	rig, file := exampleRig(t, verdicts)
	rig.h.opts.Env["LANE_KIND"] = kind

	if globs != "" {
		rig.h.opts.Env["ALLOW_PATHS"] = globs
	}

	inner := rig.h.agents.result
	rig.h.agents.result = func(request AgentRequest) agent.Result {
		if strings.Contains(request.Prompt, writer) {
			for _, name := range files {
				rig.put(filepath.Join(rig.dir, name), "x")
			}
		}

		return inner(request)
	}

	return rig, file
}

// implementMarker is a phrase that only the prompt of the implement step holds.
const implementMarker = "Run the acceptance script before your first edit"

// fixMarker is a phrase that only the prompt of the fix step holds.
const fixMarker = "earlier agents already tried once"

func TestExampleLane_APapersLaneThatChangesASourcePathFailsAndNamesIt(t *testing.T) {
	t.Parallel()

	rig, file := laneRig(t, "papers", "papers/** docs/**", func(int) string { return "approve" }, implementMarker, "papers/a.md", "src/lib.go")

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.ErrorContains(t, err, "src/lib.go")
	assert.NotContains(t, err.Error(), "papers/a.md")
}

func TestExampleLane_APapersLaneThatChangesOnlyItsPathsReachesTheMerge(t *testing.T) {
	t.Parallel()

	rig, file := laneRig(t, "papers", "papers/** docs/**", func(int) string { return "approve" }, implementMarker, "papers/a.md", "docs/b.md")

	result, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, "merged", result.Merge)
}

func TestExampleLane_ADesignLaneFixThatChangesASourcePathFailsAndNamesIt(t *testing.T) {
	t.Parallel()

	rig, file := laneRig(t, "design", "design/**", func(review int) string {
		if review == 1 {
			return "changes"
		}

		return "approve"
	}, fixMarker, "src/fix.go")

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.ErrorContains(t, err, "src/fix.go")
	assert.Equal(t, []string{"opus", "sonnet", "opus", "sonnet"}, rig.models(), "the implement step passed and the fix step failed")
}

// settleMarker is a phrase that only the prompt of the settle step holds.
const settleMarker = "owner ruled on each open finding"

func TestExampleLane_APapersLaneSettleThatChangesASourcePathFailsAndNamesIt(t *testing.T) {
	t.Parallel()

	rig, file := laneRig(t, "papers", "papers/**", func(int) string { return "changes" }, settleMarker, "src/settled.go")
	done := runInBackground2(rig.h.engine(t), file)

	waitForTheOwner(t, rig.h)
	require.NoError(t, rig.h.client(t, testVersion).Answer(testItem, "fix all"))
	outcome := <-done

	require.ErrorContains(t, outcome.err, "src/settled.go")
}

func TestExampleLane_APapersLaneWithNoGlobsIsRefusedBeforeAnyAgentStarts(t *testing.T) {
	t.Parallel()

	rig, file := laneRig(t, "papers", "", func(int) string { return "approve" }, implementMarker)

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.ErrorContains(t, err, "ALLOW_PATHS")
	assert.Empty(t, rig.models())
}

func TestExampleLane_AnUnknownLaneKindIsRefusedAndNamesTheKinds(t *testing.T) {
	t.Parallel()

	rig, file := laneRig(t, "paper", "papers/**", func(int) string { return "approve" }, implementMarker)

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.ErrorContains(t, err, "impl, papers or design")
	assert.Empty(t, rig.models())
}

func TestExampleLane_AnImplLaneMayChangeASourcePath(t *testing.T) {
	t.Parallel()

	rig, file := laneRig(t, "impl", "", func(int) string { return "approve" }, implementMarker, "src/lib.go")

	result, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	assert.Equal(t, "merged", result.Merge)
}
