package flow

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/agent"
)

// laneRig is the example rig with a lane kind and its globs. The step named by writer also writes the given files in the worktree.
func laneRig(t *testing.T, kind, globs string, verdicts func(int) string, writer string, files ...string) (*agentRig, string) {
	t.Helper()

	rig, file := exampleRig(t, verdicts)
	rig.setRound("lane_kind", kind)
	rig.setRound("allow_paths", listOf(globs))

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

	_, err := runPipeline(rig.h.engine(t), file)

	require.ErrorContains(t, err, "src/lib.go")
	assert.NotContains(t, err.Error(), "papers/a.md")
}

func TestExampleLane_APapersLaneThatChangesOnlyItsPathsReturnsTheApprovedHead(t *testing.T) {
	t.Parallel()

	rig, file := laneRig(t, "papers", "papers/** docs/**", func(int) string { return "approve" }, implementMarker, "papers/a.md", "docs/b.md")

	result, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	assert.True(t, childOf(t, result).Approved)
}

func TestExampleLane_ADesignLaneFixThatChangesASourcePathFailsAndNamesIt(t *testing.T) {
	t.Parallel()

	rig, file := laneRig(t, "design", "design/**", func(review int) string {
		if review == 1 {
			return "changes"
		}

		return "approve"
	}, fixMarker, "src/fix.go")

	_, err := runPipeline(rig.h.engine(t), file)

	require.ErrorContains(t, err, "src/fix.go")
	assert.Equal(t, []string{"opus", "sonnet", "opus", "sonnet"}, rig.models(), "the implement step passed and the fix step failed")
}

// settleMarker is a phrase that only the prompt of the settle step holds.
const settleMarker = "owner ruled on each open finding"

func TestExampleLane_APapersLaneSettleThatChangesASourcePathFailsAndNamesIt(t *testing.T) {
	t.Parallel()

	rig, file := laneRig(t, "papers", "papers/**", func(int) string { return "changes" }, settleMarker, "src/settled.go")
	rig.setRound("fix_rounds", oneFixRound)
	done := pipelineInBackground(rig.h.engine(t), file)

	waitOn(t, ownerAsks(t, rig.h))
	require.NoError(t, rig.h.client(t, testVersion).Reply(testItem, Reply{Text: "fix all"}))
	outcome := receive(t, done)

	require.ErrorContains(t, outcome.err, "src/settled.go")
}

func TestExampleLane_AnImplLaneMayChangeASourcePath(t *testing.T) {
	t.Parallel()

	rig, file := laneRig(t, "impl", "", func(int) string { return "approve" }, implementMarker, "src/lib.go")

	result, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	assert.True(t, childOf(t, result).Approved)
}
