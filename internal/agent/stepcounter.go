package agent

import (
	"context"
	"io"
	"strings"
)

// stepCounter passes whole output lines to out, then counts the shell calls that the adapter finds in them. A CLI with no turn cap of its own
// has no turns, so a tool step stands for a turn. The call past the limit cancels the run with errTurnCap, which kills the process group.
// The call that goes past the limit has started by then, so one call more than the limit can run.
// A call that holds an id counts once, because a stream names the same call on more than one line. A call with no id counts each time.
type stepCounter struct {
	out     io.Writer
	adapter Adapter
	limit   int
	cancel  context.CancelCauseFunc
	seen    map[string]bool
	steps   int
}

// newStepCounter returns the counter in front of out when the runner enforces the turn cap (a cap is set and the profile has no turn flag), and out itself otherwise.
func newStepCounter(spec Spec, out io.Writer, cancel context.CancelCauseFunc) io.Writer {
	if spec.MaxTurns <= 0 || spec.Adapter.Profile().TurnsFlag != "" {
		return out
	}

	return &stepCounter{out: out, adapter: spec.Adapter, limit: spec.MaxTurns, cancel: cancel, seen: map[string]bool{}}
}

func (c *stepCounter) Write(data []byte) (int, error) {
	n, err := c.out.Write(data)

	for line := range strings.SplitSeq(string(data), "\n") {
		c.count(line)
	}

	return n, err
}

func (c *stepCounter) count(line string) {
	for _, call := range c.adapter.Calls(line).Shell {
		if call.ID != "" && c.seen[call.ID] {
			continue
		}

		c.seen[call.ID] = true
		c.steps++

		if c.steps > c.limit {
			c.cancel(errTurnCap)
		}
	}
}
