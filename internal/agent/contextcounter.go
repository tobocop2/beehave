package agent

import (
	"context"
	"io"
	"strings"
)

// contextCounter passes whole output lines to out, then reads the context figure that the adapter finds in them (Seen.Context). A figure at or past the
// limit cancels the run with errContextCap, which kills the process group. limit is the percent times the window, so a figure compares as figure*100 >= limit.
// A line with no figure (0) never reaches the limit, so an adapter that reports none is never stopped.
type contextCounter struct {
	out     io.Writer
	adapter Adapter
	limit   int64
	cancel  context.CancelCauseFunc
}

// newContextCounter returns the counter in front of out when the spec sets a context percent and a window, and out itself otherwise.
func newContextCounter(spec Spec, out io.Writer, cancel context.CancelCauseFunc) io.Writer {
	if spec.MaxContextPercent <= 0 || spec.ContextWindow <= 0 {
		return out
	}

	return &contextCounter{out: out, adapter: spec.Adapter, limit: spec.ContextWindow * int64(spec.MaxContextPercent), cancel: cancel}
}

func (c *contextCounter) Write(data []byte) (int, error) {
	n, err := c.out.Write(data)

	for line := range strings.SplitSeq(string(data), "\n") {
		if figure := c.adapter.Calls(line).Context; figure*percentBase >= c.limit {
			c.cancel(errContextCap)
		}
	}

	return n, err
}

// percentBase turns a figure in tokens into the units of the limit, which is a percent times a window.
const percentBase = 100
