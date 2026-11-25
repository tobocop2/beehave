package flow

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/require"
)

// recordsAt returns step records with the given ids and no names.
func recordsAt(ids ...int) []dbos.StepInfo {
	steps := make([]dbos.StepInfo, len(ids))
	for i, id := range ids {
		steps[i] = dbos.StepInfo{StepID: id}
	}

	return steps
}

func TestFirstGap_OneStepIDWithNoRecordIsNoGap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ids  []int
		want int
	}{
		{"one id with no record", []int{0, 1, 3, 4}, 4},
		{"two ids with no record at the start", []int{2, 3}, 0},
		{"two ids with no record after a record", []int{0, 1, 4}, 2},
		{"one id, then two ids", []int{0, 2, 5}, 3},
		{"a skip next to a refused pair", []int{0, 1, 2, 6}, 3},
		{"no id with no record", []int{0, 1, 2}, 3},
		{"no records", nil, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, firstGap(recordsAt(tt.ids...)))
		})
	}
}

func TestResume_AReceiveThatFindsItsMessageWaitingDoesNotRunTheAgentAgain(t *testing.T) {
	t.Parallel()

	var agents atomic.Int32

	flowCtx, id := failedHoleRun(t, &agents, false)
	require.EqualValues(t, 1, agents.Load())

	require.NoError(t, (&Client{flow: flowCtx}).reopen(id))

	handle, err := dbos.RetrieveWorkflow[int](flowCtx, id)
	require.NoError(t, err)

	got, err := resultWithin(t, handle)
	require.NoError(t, err)
	require.Equal(t, 7, got)
	require.EqualValues(t, 1, agents.Load())
}

func TestPremise_AStepRefusedWithItsFailureRecordLeavesTwoStepIDsWithNoRecord(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset.Add(-30*time.Millisecond))
	inTurn(h, limited(limitReset, 0.2), exited)

	engine := h.engine(t)

	_, err := sqlOf(t, h).ExecContext(t.Context(), "CREATE TRIGGER refuse_gap BEFORE INSERT ON operation_outputs WHEN NEW.function_name = 'agent:fix#1/limit1.0' BEGIN SELECT RAISE(ABORT, 'the store refused the record'); END")
	require.NoError(t, err)

	_, err = engine.Run(testItem, testProject)
	require.ErrorContains(t, err, "the store refused the record")
	require.Equal(t, []int{2}, idRuns(recordsOfTestRun(t, engine)))
}
