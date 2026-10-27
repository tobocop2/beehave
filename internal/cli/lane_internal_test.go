package cli

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/brief"
)

// The exhaustruct linter proves that the one keyed literal in laneOf sets every brief.Lane field.
// This test checks that the value of each field of the lane flags is the value that the lane gets.
func TestLaneOf_CopiesEachValueOfTheLaneFlagsIntoTheLane(t *testing.T) {
	t.Parallel()

	opts := &briefNewOptions{
		kind: "rework",
		lane: laneFlags{
			key: "k", mechanism: "m", acceptance: "a", baseResult: "b", handoff: "h", transcript: "t", reviewPR: "7",
			expected: "e", branch: "br", readFirst: "r", cap: 9, newBehavior: true, union: true,
		},
	}

	lane, err := laneOf(opts)
	require.NoError(t, err)
	require.Equal(t, brief.LaneRework, lane.Kind)
	require.Equal(t, "k", lane.Key)
	require.Equal(t, "m", lane.Mechanism)
	require.Equal(t, "a", lane.Acceptance)
	require.Equal(t, "b", lane.BaseResult)
	require.Equal(t, "h", lane.Handoff)
	require.Equal(t, "t", lane.Transcript)
	require.Equal(t, "7", lane.ReviewPR)
	require.Equal(t, "e", lane.Expected)
	require.Equal(t, "br", lane.Branch)
	require.Equal(t, "r", lane.ReadFirst)
	require.Equal(t, 9, lane.Cap)
	require.True(t, lane.NewBehavior)
	require.True(t, lane.Union)
	require.Nil(t, lane.Body)
}

func TestLaneOf_RefusesAnUnknownKind(t *testing.T) {
	t.Parallel()

	_, err := laneOf(&briefNewOptions{kind: "bogus"})
	require.Error(t, err)
}
