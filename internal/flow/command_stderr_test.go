package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWalk_ACommandReturnsItsStderrApartFromItsStdout(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.commands.reply = func(CommandRequest) (CommandOutput, error) {
		return CommandOutput{Stdout: "the out", Stderr: "the err"}, nil
	}

	_, err := walkBody(t, h, "ran = command(run = [\"a\"])\ncommand(run = [\"b\"], stdin = ran.stderr, env = {\"OUT\": ran.stdout})\n")

	require.NoError(t, err)
	require.Len(t, h.commands.requests, 2)
	assert.Equal(t, "the err", h.commands.requests[1].Stdin)
	assert.Equal(t, []string{"OUT=the out"}, h.commands.requests[1].Env)
}

func TestResult_ARecordFromBeforeStderrStillGivesItsOtherFields(t *testing.T) {
	t.Parallel()

	old, err := result(map[string]any{"stdout": "the out", "rc": 0})
	require.NoError(t, err)

	out, err := old.Attr("stdout")
	require.NoError(t, err)
	assert.Equal(t, `"the out"`, out.String())

	_, err = old.Attr("stderr")
	require.Error(t, err, "the old record has no stderr, so only a workflow that reads it fails")
}
