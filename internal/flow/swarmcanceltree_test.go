package flow

import (
	"context"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/workflow"
)

// nestedFlow spawns 21, whose run spawns 22 and waits for it. The run of 22 holds in its command. The parent stops the run of 21 with the call it is given,
// then runs a command that names what the call returned.
func nestedFlow(stop string) string {
	return `def inner(item):
    command(name = "hold", run = ["hold"])

def outer(item):
    gather(spawn(items = ["22"], entry = "inner", name = "deep"), name = "deep")

def main(item):
    runs = spawn(items = ["21"], entry = "outer", name = "fan")
    command(name = "ready", run = ["ready"])
    got = ` + stop + `
    command(name = "after", run = ["after", got])
`
}

// holdInner holds the command of the run of 22 until its context ends, and holds the command ready of the parent until the run of 22 holds,
// so the stop of the parent finds a child with its own open child.
func holdInner(h *harness) {
	holding := make(chan struct{})

	h.commands.before = func(ctx context.Context, request CommandRequest) error {
		switch request.Item {
		case "22":
			close(holding)
			<-ctx.Done()

			return ctx.Err()
		case testItem:
			if request.Run[0] == "ready" {
				<-holding
			}
		}

		return nil
	}
}

func TestCancelTree_EndsTheChildAndTheChildOfThatChild(t *testing.T) {
	t.Parallel()

	cases := map[string]struct{ stop, want string }{
		"cancel":   {`str(cancel(runs.runs[0], name = "drop").` + workflow.CancelledField + `)`, "True"},
		"deadline": {`gather(runs, timeout = "1s", on_timeout = "cancel", name = "fan").results[0].error`, "deadline"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			holdInner(h)

			engine := h.engine(t)
			_, err := engine.RunFile(testItem, testProject, writeFlow(t, nestedFlow(c.stop)), modeOff())

			require.NoError(t, err)
			assert.Contains(t, h.commands.ranBy(testItem), "after "+c.want)

			for _, item := range []string{"21", "22"} {
				waitOn(t, condition(func() bool { return childStatus(t, engine, item) == dbos.WorkflowStatusCancelled }))
			}
		})
	}
}
