package flow

import (
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// succeededRun is a stored run that ended with success, with the given stored input and stored output.
func succeededRun(input, output string) run {
	return run{Attempt: 1, Status: dbos.WorkflowStatus{ID: runID("21", 1), Status: dbos.WorkflowStatusSuccess, Input: input, Output: output}}
}

const foreignOutput = `{"item": "21", "value": {"reason": "foreign", "pr": 7, "findings": [{"class": "blocking", "text": "x"}]}}`

func TestQueue_AForeignRunWhoseProjectCannotBeReadFailsClosedAndIsNotDone(t *testing.T) {
	t.Parallel()

	q := &fakeQueue{states: map[int]string{7: "MERGED"}}
	engine := &Engine{deps: Deps{Queue: q}}

	settled, err := engine.stateOf(t.Context(), succeededRun("not an item input", foreignOutput), true, false)

	require.ErrorContains(t, err, "read the input of run "+runID("21", 1))
	assert.Empty(t, settled.state, "an unreadable project gives no state, so the row is not reported as done")
	assert.Empty(t, q.stateAsked, "no pull request state is read without a project")
}

func TestQueue_ARunWhoseValueIsNotAForeignRecordStaysDoneAndReadsNoState(t *testing.T) {
	t.Parallel()

	q := &fakeQueue{states: map[int]string{7: "MERGED"}}
	engine := &Engine{deps: Deps{Queue: q}}

	settled, err := engine.stateOf(t.Context(), succeededRun(`{"item": "21", "project": "`+testProject+`"}`, `{"item": "21", "value": "text"}`), true, false)

	require.NoError(t, err)
	assert.Equal(t, queueDone, settled.state)
	assert.Empty(t, q.stateAsked, "a value that is not a foreign record reads no pull request state")
}

func TestQueue_ARunWhoseResultCannotBeReadFailsClosedAndIsNotDone(t *testing.T) {
	t.Parallel()

	for name, output := range map[string]string{
		"the stored output is not JSON":         "not json",
		"the stored output is the empty string": "",
		"the stored output has a wrong field":   `{"item": 21, "value": {"reason": "foreign", "pr": 7, "findings": [{"class": "blocking"}]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			q := &fakeQueue{states: map[int]string{7: "MERGED"}}
			engine := &Engine{deps: Deps{Queue: q}}

			settled, err := engine.stateOf(t.Context(), succeededRun(`{"item": "21", "project": "`+testProject+`"}`, output), true, false)

			require.ErrorContains(t, err, "read the result of "+runID("21", 1))
			assert.Empty(t, settled.state, "status reads the result the same way, so a result that status cannot read gives no row state")
			assert.Empty(t, q.stateAsked)
		})
	}
}
