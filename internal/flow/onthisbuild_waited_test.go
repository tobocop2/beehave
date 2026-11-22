package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// waitThenFail is a workflow file that waits and then fails, so the run ends failed after it received the answer.
func waitThenFail(arguments string) string {
	return "def main(item):\n    wait(name = \"ask\", question = \"What now?\"" + arguments + ")\n    fail(\"stop\")\n"
}

// forgetSteps deletes the recorded steps of the run whose names match the patterns, as a run that an older build recorded holds none of them.
func forgetSteps(t *testing.T, h *harness, patterns ...string) {
	t.Helper()

	for _, pattern := range patterns {
		_, err := sqlOf(t, h).ExecContext(t.Context(), "DELETE FROM operation_outputs WHERE workflow_uuid = ? AND function_name LIKE ?", testRun, pattern)
		require.NoError(t, err)
	}
}

// waitedRun runs a workflow file that waits, answers it, and leaves the run failed after the receive of the answer.
// The run is the record of an older build at the moment after the receive: it holds the receive, and neither the step that runs the notify command
// (an older build did not write it) nor the step that closes the wait (the run did not reach it).
func waitedRun(t *testing.T, flowText, from string) *harness {
	t.Helper()

	harness := newHarness(t)
	harness.flowIs(t, flowText)
	engine := harness.engine(t)
	client := harness.client(t, testVersion)
	done := runInBackground(engine, testItem)

	waitOn(t, ownerAsks(t, harness))
	require.NoError(t, client.Reply(testItem, Reply{From: from, Text: "go"}))
	require.Error(t, receive(t, done).err, "the file fails after the wait")
	require.NoError(t, engine.Shutdown())

	forgetSteps(t, harness, "wait:ask%", notifyStep)

	return harness
}

// namesOf reads the recorded steps of the test run through a client of another build.
func namesOf(t *testing.T, c *Client) []string {
	t.Helper()

	names, err := c.stepNames(testRun)
	require.NoError(t, err)

	return names
}

// requireStranded asserts that the move is refused for the module, and that the run stays on its build.
func requireStranded(t *testing.T, c *Client, module string) {
	t.Helper()

	err := c.ResumeOnThisBuild(testItem)

	require.ErrorIs(t, err, errStranded)
	require.ErrorContains(t, err, module+" module")

	row, rowErr := c.row(testRun)
	require.NoError(t, rowErr)
	assert.Equal(t, testVersion, buildVersion(row.ApplicationVersion), "a refused run stays where it was")
}

func TestClient_ResumeOnThisBuildRefusesAWaitedRunWhoseOnlyCoreStepIsTheReceiveWhenTheCoreModuleChanged(t *testing.T) {
	t.Parallel()

	forms := map[string]struct{ arguments, from string }{
		"plain wait": {"", ""},
	}

	for name, form := range forms {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			harness := waitedRun(t, waitThenFail(form.arguments), form.from)
			other := buildWithModule(harness.client(t, otherBuild), moduleCore, coreVersion+1)

			names := namesOf(t, other)
			require.Contains(t, names, "DBOS.recv", "the run recorded the receive of the answer")

			for _, step := range names {
				_, named := kindOfStep(step)
				require.False(t, named, "the run recorded no named step of any module, and %q is one: %v", step, names)
			}

			requireStranded(t, other, "core")
		})
	}
}

func TestClient_ResumeOnThisBuildRefusesAnAddressedWaitWhenTheCoreModuleChanged(t *testing.T) {
	t.Parallel()

	forms := map[string]struct{ arguments, from, step string }{
		"wait by id":                 {", to = [\"ana\"]", "ana", "wait:id"},
		"wait with a deadline":       {", to = [\"ana\"], timeout = \"1h\"", "ana", "wait:deadline"},
		"plain wait with a deadline": {", timeout = \"1h\"", "", "wait:deadline"},
	}

	for name, form := range forms {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			harness := waitedRun(t, waitThenFail(form.arguments), form.from)
			other := buildWithModule(harness.client(t, otherBuild), moduleCore, coreVersion+1)

			names := namesOf(t, other)
			require.Contains(t, names, form.step, "the wait records this step before the receive")
			require.Contains(t, names, "DBOS.recv")

			requireStranded(t, other, "core")
		})
	}
}

func TestClient_ResumeOnThisBuildMovesAWaitedRunWhenTheCoreModuleDidNotChange(t *testing.T) {
	t.Parallel()

	harness := waitedRun(t, waitThenFail(""), "")
	other := buildWithModule(harness.client(t, otherBuild), moduleGate, gateVersion+1)

	names := namesOf(t, other)
	require.Contains(t, names, "DBOS.recv", "the run waited")
	require.NotContains(t, modulesUsed(names), moduleGate, "the run recorded no step of the gate module")

	require.NoError(t, other.ResumeOnThisBuild(testItem))

	row, rowErr := other.row(testRun)
	require.NoError(t, rowErr)
	assert.Equal(t, other.version, row.ApplicationVersion)
}

func TestClient_ResumeOnThisBuildMovesARunThatRecordedNothingOfCoreAndDidNotWaitWhenTheCoreModuleChanged(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.agents.err = errFake
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)
	require.Error(t, err)
	require.NoError(t, engine.Shutdown())

	harness.agents.mu.Lock()
	harness.agents.err = nil
	harness.agents.mu.Unlock()

	other := buildWithModule(harness.client(t, otherBuild), moduleCore, coreVersion+1)

	names := namesOf(t, other)
	require.NotContains(t, names, "DBOS.recv", "the run did not wait")
	require.NotContains(t, modulesUsed(names), moduleCore, "the run recorded no step of the core module")

	require.NoError(t, other.ResumeOnThisBuild(testItem))

	row, rowErr := other.row(testRun)
	require.NoError(t, rowErr)
	assert.Equal(t, other.version, row.ApplicationVersion)
}
