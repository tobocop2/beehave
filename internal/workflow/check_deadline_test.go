package workflow_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/workflow"
)

func TestCheck_AcceptsAGatherWithADeadlineAndACancelOfAChild(t *testing.T) {
	t.Parallel()

	got := checkText(t, "def main(item):\n    runs = spawn([\"1\"])\n    cancel(runs.runs[0], name = \"drop\")\n    gather(runs, timeout = \"30s\", on_timeout = \"return\")\n    gather(runs, timeout = \"30s\", on_timeout = \"cancel\")\n    gather(runs, timeout = \"30s\")\n")

	assert.Empty(t, got)
}

func TestCheck_ADeadlineModeIsRefusedAloneOrOutsideItsSet(t *testing.T) {
	t.Parallel()

	cases := map[string]struct{ call, want string }{
		"mode alone":   {`gather(runs, on_timeout = "cancel")`, "gather: on_timeout needs a timeout. Add timeout = \"10m\" to the call, or leave on_timeout out."},
		"unknown mode": {`gather(runs, timeout = "30s", on_timeout = "wait")`, "on_timeout"},
		"zero timeout": {`gather(runs, timeout = "0s")`, "timeout is \"0s\", which must be above zero"},
		"cancel bare":  {`cancel()`, "cancel needs the `run` keyword"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := checkText(t, "def main(item):\n    runs = spawn([\"1\"])\n    "+c.call+"\n")

			require.Len(t, got, 1)
			assert.Contains(t, got[0], c.want)
		})
	}
}

func TestGroupDeadline_ReadsTheTimeoutAndTheModeOfAGather(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		timeout, mode string
		wait          time.Duration
		want          workflow.OnTimeout
		err           string
	}{
		"none":         {},
		"cancel":       {timeout: "30s", mode: "cancel", wait: 30 * time.Second, want: workflow.OnTimeoutCancel},
		"return":       {timeout: "2m", mode: "return", wait: 2 * time.Minute, want: workflow.OnTimeoutReturn},
		"default":      {timeout: "1h", wait: time.Hour, want: workflow.OnTimeoutCancel},
		"mode alone":   {mode: "return", err: "on_timeout needs a timeout"},
		"unknown mode": {timeout: "1s", mode: "wait", err: "on_timeout is \"wait\". Use \"cancel\" or \"return\""},
		"zero":         {timeout: "0s", err: "must be above zero"},
		"not duration": {timeout: "soon", err: "not a duration"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			wait, mode, err := workflow.GroupDeadline(c.timeout, c.mode)
			if c.err != "" {
				require.ErrorContains(t, err, c.err)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, c.wait, wait)
			assert.Equal(t, c.want, mode)
		})
	}
}

func TestOnTimeoutModes_ListsCancelAndReturn(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"cancel", "return"}, workflow.OnTimeoutModes())
}
