package flow

import (
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"

	"github.com/tobocop2/obee/internal/workflow"
)

const finishCall = "def finish(item):\n    command(name = \"teardown\", run = [\"teardown\"])\n"

func TestFinish_RunsAfterAMainThatFailed(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := walkText(t, h, "def main(item):\n    fail(\"main broke\")\n\n"+finishCall)

	require.ErrorContains(t, err, "main broke")
	assert.Equal(t, []string{"teardown"}, h.commands.ran(), "finish ran after the failure")
}

func TestFinish_RunsAfterACommandStopsTheItem(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = func(r CommandRequest) (CommandOutput, error) {
		if r.Run[0] == "lint" {
			return CommandOutput{Stdout: "no", RC: 2}, nil
		}

		return CommandOutput{}, nil
	}

	result, err := walkText(t, h, "def main(item):\n    command(name = \"lint\", run = [\"lint\"], stop_on_rc = [2])\n\n"+finishCall)

	require.NoError(t, err)
	assert.NotNil(t, result.Stopped)
	assert.Equal(t, []string{"lint", "teardown"}, h.commands.ran())
}

func TestFinish_RunsAfterAMainThatSucceeded(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := walkText(t, h, "def main(item):\n    command(name = \"work\", run = [\"work\"])\n\n"+finishCall)

	require.NoError(t, err)
	assert.Equal(t, []string{"work", "teardown"}, h.commands.ran())
}

func TestFinish_DoesNotRunWhenTheRunIsCanceled(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)
	file := writeFlow(t, "def main(item):\n    wait(name = \"ask\", question = \"go?\")\n\n"+finishCall)
	done := runInBackground2(engine, file)

	waitForTheOwner(t, h)
	require.NoError(t, h.client(t, testVersion).Cancel(testItem))

	outcome := <-done

	require.Error(t, outcome.err)
	assert.NotContains(t, outcome.err.Error(), "finish: ", "a cancel does not call finish, so finish reports nothing")
	assert.Empty(t, h.commands.ran(), "a cancel leaves the host for a resume")
}

// finishCalls runs main and then finish of the file on a script whose context is canceled or live, and counts the print calls of finish.
func finishCalls(t *testing.T, canceled bool) int {
	t.Helper()

	file := writeFlow(t, "def main(item):\n    pass\n\ndef finish(item):\n    print(\"finish ran\")\n")
	pinned, err := workflow.Pin(file)
	require.NoError(t, err)

	sources, err := workflow.Open(file, pinned)
	require.NoError(t, err)

	engine := newHarness(t).engine(t)
	ctx, cancel := dbos.WithCancel(engine.ctx)

	if canceled {
		cancel()
	} else {
		defer cancel()
	}

	s := &script{e: engine, ctx: ctx, in: ItemInput{Item: testItem}, counts: map[string]int{}}
	require.NoError(t, s.main(engine.thread(testItem), sources, nil))

	calls := 0
	thread := engine.thread(testItem)
	thread.Print = func(*starlark.Thread, string) { calls++ }

	require.NoError(t, s.finish(thread, sources, nil))

	return calls
}

func TestFinish_ACanceledScriptDoesNotCallIt(t *testing.T) {
	t.Parallel()

	assert.Zero(t, finishCalls(t, true))
	assert.Equal(t, 1, finishCalls(t, false), "the same file calls finish when the run is live")
}

func TestFinish_AFailureAfterAMainThatSucceededIsTheErrorOfTheRun(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := walkText(t, h, "def main(item):\n    pass\n\ndef finish(item):\n    fail(\"teardown broke\")\n")

	require.ErrorContains(t, err, "finish: ")
	assert.ErrorContains(t, err, "teardown broke")
}

func TestFinish_AFailureIsReportedNextToTheErrorOfMain(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := walkText(t, h, "def main(item):\n    fail(\"main broke\")\n\ndef finish(item):\n    fail(\"teardown broke\")\n")

	require.ErrorContains(t, err, "main broke")
	require.ErrorContains(t, err, "teardown broke")
}

func TestFinish_AFileWithoutItRunsAsBefore(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := walkText(t, h, "def main(item):\n    command(name = \"work\", run = [\"work\"])\n")

	require.NoError(t, err)
	assert.Equal(t, []string{"work"}, h.commands.ran())
}
