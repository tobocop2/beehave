package agent

import (
	"context"
	"io"
	"strconv"
	"strings"
)

// spendCounter passes whole output lines to out, then adds the dollars that the adapter finds in them (Seen.Spent). It is the only reader of Seen.Spent:
// the sum goes to Result.Streamed when the run ends, and the caller does not read the dollars of a line again. An adapter that keeps state in Calls
// (a dedupe by message id) is therefore asked for the dollars of a line once.
// A line with a Spent that is not a positive number (zero, negative or NaN) adds nothing, so no line can lower the sum or stop it from passing the limit.
// When limit is above 0 and cancel is set, a sum past the limit cancels the run with errBudgetCap, which kills the process group.
// The step in flight is not in the sum yet, so the run can pass the limit by the cost of one step. A sum that only reaches the limit does not stop the run.
// The redacting writer in front of the counter replaces a line past maxLineBytes by DroppedLine, so the dollars on such a line are not in the sum.
type spendCounter struct {
	out     io.Writer
	adapter Adapter
	limit   float64
	cancel  context.CancelCauseFunc
	spent   float64
	priced  bool
}

// newSpendCounter returns the counter for a profile with StreamsCost, or nil for any other. The counter enforces the cap of the spec only when the profile
// has no cost flag of its own.
func newSpendCounter(spec Spec, out io.Writer, cancel context.CancelCauseFunc) *spendCounter {
	profile := spec.Adapter.Profile()
	if !profile.StreamsCost {
		return nil
	}

	counter := &spendCounter{out: out, adapter: spec.Adapter}
	if profile.BudgetFlag == "" {
		counter.limit, counter.cancel = spec.MaxBudget, cancel
	}

	return counter
}

func (c *spendCounter) Write(data []byte) (int, error) {
	n, err := c.out.Write(data)

	for line := range strings.SplitSeq(string(data), "\n") {
		seen := c.adapter.Calls(line)
		if seen.Spent > 0 {
			c.spent += seen.Spent
		}

		c.priced = c.priced || seen.Priced || seen.Spent > 0

		if c.cancel != nil && c.over() {
			c.cancel(errBudgetCap)
		}
	}

	return n, err
}

// over is true when the sum is past the limit. A limit of 0 means no limit.
func (c *spendCounter) over() bool {
	return c.limit > 0 && c.spent > c.limit
}

// capNote returns why a run that ended without a stop still ends as a budget cap, or "" when the sum holds up the cap.
// A cost cap is only enforced from the dollars that the CLI reports, so a run where no line carried a cost field did not stay under the cap: nothing says
// what it cost. A cost of 0 on a line is a real cost (Seen.Priced), so a free run with a cap runs normally.
// A sum past the cap that the counter read after the process had exited (the last line, with no newline) also ends the run as a budget cap.
func (c *spendCounter) capNote() string {
	switch {
	case c.limit <= 0:
		return ""
	case !c.priced:
		return "the CLI reported no cost, so the cost cap of " + usd(c.limit) + " dollars could not be checked"
	case c.over():
		return "the cost of the run, " + usd(c.spent) + " dollars, is past the cap of " + usd(c.limit) + " dollars"
	}

	return ""
}

func usd(amount float64) string {
	return strconv.FormatFloat(amount, 'f', -1, floatBits)
}
