package flow

import (
	"cmp"
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/beehest/internal/agent"
)

// The scopes of a cap that stops an agent step.
const (
	scopeRun  = "run"
	scopeTree = "tree"
	scopeWeek = "week"
)

// CapStop is a cap that holds an agent step back. The step waits for the owner to raise the cap, so the run stays open and no step fails.
// The view shows it on the run and on the line of the held step.
type CapStop struct {
	// Scope is the cap that stopped the step: run (agents.max_run_budget_usd), tree (agents.max_tree_budget_usd) or week (agents.max_week_budget_usd).
	Scope string `json:"scope"`
	// Cap is the dollars of the cap that the check read from the config.
	Cap float64 `json:"cap_usd"`
	// Spent is the dollars that the check found spent.
	Spent float64 `json:"spent_usd"`
	// Note says which key to raise.
	Note string `json:"note"`
}

// text is the note of the stop for the human view of the status.
func (c CapStop) text() string {
	return "stopped at the " + c.Scope + " cap $" + dollars(c.Cap) + ", spent $" + dollars(c.Spent)
}

// capCheck is what one check of the caps records: the stop, or nothing when the step may start.
type capCheck struct {
	Stop *CapStop `json:"stop,omitempty"`
}

// capLimit names a cap for the text of its stop.
type capLimit struct {
	scope string
	// subject is what the cap counts, and key the config key that sets it.
	subject, key, env string
	limit             float64
	// spenders names who spent the sum, for the note of the stop.
	spenders string
}

// capLookStepName is the step of a phase that looks at the run cap and the tree cap before the agent starts.
const capLookStepName = "agent/caps"

// lookAtCaps is the look at the caps that opens a phase, as a recorded step. A rerun of the phase rewinds to it, so every agent run looks first.
func (e *Engine) lookAtCaps(ctx dbos.Context, in PhaseInput) (capCheck, error) {
	return runStep(e, ctx, capLookStepName, func(context.Context) (capCheck, error) {
		return e.checkCaps(in.Call, in.Run)
	})
}

// holdAtCap records the stop that the look of a phase returned, as look number look of the try, and sleeps before the next phase looks again.
func (s *script) holdAtCap(name string, try, look int, stop *CapStop) error {
	_, err := runStep(s.e, s.ctx, name+"/budget"+strconv.Itoa(try)+"."+strconv.Itoa(look), func(context.Context) (capCheck, error) {
		return capCheck{Stop: stop}, nil
	})
	if err != nil {
		return err
	}

	return s.waitForCap()
}

// waitForCap is the durable sleep between two looks at a cap that stopped a step.
func (s *script) waitForCap() error {
	if _, err := dbos.Sleep(s.ctx, cmp.Or(s.e.deps.Clock.CapWait, capWaitFloor)); err != nil {
		return fmt.Errorf("wait for a cap of item %s to be raised: %w", s.in.Item, err)
	}

	return nil
}

// runCap is the cap of one item run, as the config of the engine sets it.
func (e *Engine) runCap() capLimit {
	return capLimit{scopeRun, "one item run", "agents.max_run_budget_usd", "BEEHEST_AGENTS_MAX_RUN_BUDGET_USD", e.maxRunBudget, "the item run"}
}

// treeCap is the cap of the item tree, as the config of the engine sets it.
func (e *Engine) treeCap() capLimit {
	return capLimit{scopeTree, "the item tree", "agents.max_tree_budget_usd", "BEEHEST_AGENTS_MAX_TREE_BUDGET_USD", e.maxTreeBudget, "the item tree"}
}

// weekCap is the cap of the last seven days over all items, as the config of the engine sets it.
func (e *Engine) weekCap() capLimit {
	return capLimit{scopeWeek, "the last seven days", "agents.max_week_budget_usd", "BEEHEST_AGENTS_MAX_WEEK_BUDGET_USD", e.maxWeekBudget, "all items"}
}

// checkCaps reads the caps and the sums they count for the item run run. A cap of 0 is not read. Both sums read the records of the runs, so a resume keeps them.
func (e *Engine) checkCaps(name, run string) (capCheck, error) {
	for _, c := range e.capsSet() {
		if stop, err := underCap(name, c.cap, run, c.sum); err != nil || stop != nil {
			return capCheck{Stop: stop}, err
		}
	}

	return capCheck{}, nil
}

// cappedSum is a cap with the sum it counts.
type cappedSum struct {
	cap capLimit
	sum func(id string) (agent.Spend, error)
}

// capsSet lists the caps that are set, the run cap first.
func (e *Engine) capsSet() []cappedSum {
	var set []cappedSum

	if run := e.runCap(); run.limit > 0 {
		set = append(set, cappedSum{run, e.ownSpend})
	}

	if tree := e.treeCap(); tree.limit > 0 {
		set = append(set, cappedSum{tree, e.treeSpend})
	}

	if week := e.weekCap(); week.limit > 0 {
		set = append(set, cappedSum{week, e.weekSpend})
	}

	return set
}

// underCap reads the sum of a cap for the run and returns the stop of the step, or nil when the step may start.
func underCap(name string, c capLimit, id string, sum func(id string) (agent.Spend, error)) (*CapStop, error) {
	spent, err := sum(id)
	if err != nil {
		return nil, err
	}

	return overCap(name, c, spent)
}

// ownSpend sums the recorded cost of the item run id, the failed runs that a resume kept included.
func (e *Engine) ownSpend(id string) (agent.Spend, error) {
	// The read goes through the context of the engine, not the one of the run: a library call on the context of a run records a step of its own.
	return runSpend(e.store(), id)
}

// overCap is the stop of an agent step that may not start under a cap, or nil when it may. A sum that holds an unknown cost fails the step instead:
// the cap cannot be checked against a cost that is not known.
func overCap(name string, c capLimit, spent agent.Spend) (*CapStop, error) {
	switch {
	case spent.Unreadable:
		return nil, fmt.Errorf("the agent step %s does not start: the cap for %s is $%s, but a recorded cost of an earlier agent step cannot be read, "+
			"so the cap cannot be checked. Repair or remove that record, or set %s (or %s) to 0 for no cap", name, c.subject, dollars(c.limit), c.key, c.env)
	case spent.Unknown:
		return nil, fmt.Errorf("the agent step %s does not start: the cap for %s is $%s, but the cost is unknown for an earlier agent step "+
			"(its model has no price), so the cap cannot be checked. "+
			"Add a price for the model, or set %s (or %s) to 0 for no cap", name, c.subject, dollars(c.limit), c.key, c.env)
	case spent.Cost < c.limit:
		return nil, nil //nolint:nilnil // no stop is the answer: the step may start
	}

	return &CapStop{Scope: c.scope, Cap: c.limit, Spent: spent.Cost, Note: fmt.Sprintf("the agent step %s waits: the cap for %s is $%s and the agent steps of %s spent $%s. "+
		"The cap is checked when a step starts, so the last step can pass it by its own cost. "+
		"Raise %s (or %s) and restart beehest, or set it to 0 for no cap", name, c.subject, dollars(c.limit), c.spenders, dollars(spent.Cost), c.key, c.env)}, nil
}

// treeSpend sums the recorded cost of the root run of the item tree and of every run below it. It reads the step records and the stream of failed agent runs of each run, so a resume keeps the sum.
func (e *Engine) treeSpend(id string) (agent.Spend, error) {
	// The reads go through the context of the engine, not the one of the run: a library call on the context of a run records a step of its own.
	root, err := rootRun(e.store(), id)
	if err != nil {
		return agent.Spend{}, err
	}

	return runAndBelowSpend(e.store(), root)
}

// treeRoot is the root run of the item tree of the run of ctx.
func (e *Engine) treeRoot(ctx dbos.Context, item string) (string, error) {
	id, err := dbos.GetWorkflowID(ctx)
	if err != nil {
		return "", fmt.Errorf("read the id of the run of item %s: %w", item, err)
	}

	// The reads go through the context of the engine, not the one of the run: a library call on the context of a run records a step of its own.
	return rootRun(e.store(), id)
}

// runAndBelowSpend sums the cost of a run and of every item run below it.
func runAndBelowSpend(ctx storeView, id string) (agent.Spend, error) {
	var spent agent.Spend

	err := walkTree(ctx, id, dbos.WithStepsLoadOutput(true), func(run string, records []dbos.StepInfo) error {
		own, err := recordsSpend(ctx, run, records)
		spent = spent.Plus(own)

		return err
	})

	return spent, err
}

// walkTree passes a run and its step records, and then each item run below it with its step records, to fold. It is the one walk of an item tree:
// the sum of the tree cap and the count of the host losses both fold over it. load says whether the records hold the outputs of the steps.
// A read that fails, or a fold that fails, ends the walk with its error.
func walkTree(ctx storeView, id string, load dbos.GetWorkflowStepsOption, fold func(run string, records []dbos.StepInfo) error) error {
	records, err := dbos.GetWorkflowSteps(ctx.flow, id, load)
	if err != nil {
		return fmt.Errorf("read the steps of %s: %w", id, err)
	}

	if err := fold(id, records); err != nil {
		return err
	}

	return walkBelow(ctx, id, load, fold)
}

// walkBelow walks each item run that a run spawned, with walkTree.
func walkBelow(ctx storeView, id string, load dbos.GetWorkflowStepsOption, fold func(run string, records []dbos.StepInfo) error) error {
	children, err := itemRuns(ctx, dbos.WithFilterParentWorkflowID(id))
	if err != nil {
		return err
	}

	for _, child := range children {
		if err := walkTree(ctx, child.ID, load, fold); err != nil {
			return err
		}
	}

	return nil
}

// runSpend sums the cost that the step records and the failed runs of one run hold, as the view of the steps reads it.
func runSpend(ctx storeView, id string) (agent.Spend, error) {
	return runSpendSince(ctx, id, time.Time{})
}

// runSpendSince is runSpend for the lines that count in the window that starts at since. A zero since counts every line.
func runSpendSince(ctx storeView, id string, since time.Time) (agent.Spend, error) {
	records, err := dbos.GetWorkflowSteps(ctx.flow, id, dbos.WithStepsLoadOutput(true))
	if err != nil {
		return agent.Spend{}, fmt.Errorf("read the steps of %s: %w", id, err)
	}

	return recordsSpendSince(ctx, id, records, since)
}

// recordsSpend sums the cost of the step records of a run and of the failed runs that the run holds.
func recordsSpend(ctx storeView, id string, records []dbos.StepInfo) (agent.Spend, error) {
	return recordsSpendSince(ctx, id, records, time.Time{})
}

// recordsSpendSince is recordsSpend for the lines that count in the window that starts at since. A zero since counts every line.
func recordsSpendSince(ctx storeView, id string, records []dbos.StepInfo, since time.Time) (agent.Spend, error) {
	lines, err := runLines(ctx, id, records, "", false, time.Time{})
	if err != nil {
		return agent.Spend{}, err
	}

	var spent agent.Spend

	for _, line := range lines {
		if inWindow(line, since) {
			spent = spent.Plus(line.Spend)
		}
	}

	return spent, nil
}

// inWindow reports whether the cost of a line counts in the window that starts at since: the line ended at since or later. A line that has not ended counts from its start,
// and a line with no time at all counts, so a cost that cannot be placed in time never drops out of the cap.
func inWindow(line StepLine, since time.Time) bool {
	at := cmp.Or(line.Ended, line.Started)

	return at.IsZero() || !at.Before(since)
}

// weekWindow is the span that the week cap counts back from the time of the check.
const weekWindow = 7 * 24 * time.Hour

// windowRuns lists the item runs that can hold a cost that ended at or after since: the runs that finished after since, and the runs that have not finished.
// A run that finished before since holds no such cost, so the sum never reads it.
func windowRuns(ctx storeView, since time.Time) ([]dbos.WorkflowStatus, error) {
	finished, err := itemRuns(ctx, dbos.WithFilterCompletedAfter(since), dbos.WithFilterStatus(dbos.WorkflowStatusSuccess, dbos.WorkflowStatusError, dbos.WorkflowStatusCancelled, dbos.WorkflowStatusMaxRecoveryAttemptsExceeded))
	if err != nil {
		return nil, err
	}

	open, err := itemRuns(ctx, dbos.WithFilterStatus(dbos.WorkflowStatusPending, dbos.WorkflowStatusEnqueued, dbos.WorkflowStatusDelayed, dbos.WorkflowStatusSuspended))
	if err != nil {
		return nil, err
	}

	return append(finished, open...), nil
}

// weekSpend sums the recorded cost of every item run of the store that ended inside the last week. The run id is not read: the week cap is one sum for all items.
func (e *Engine) weekSpend(string) (agent.Spend, error) {
	since := e.deps.Clock.wall().Add(-weekWindow)

	// The reads go through the context of the engine, not the one of the run: a library call on the context of a run records a step of its own.
	runs, err := windowRuns(e.store(), since)
	if err != nil {
		return agent.Spend{}, err
	}

	var spent agent.Spend

	for _, run := range runs {
		own, err := runSpendSince(e.store(), run.ID, since)
		if err != nil {
			return agent.Spend{}, err
		}

		spent = spent.Plus(own)
	}

	return spent, nil
}

// itemRuns lists the item runs that match the filters. With no filter it lists every item run of the store.
func itemRuns(ctx storeView, filters ...dbos.ListWorkflowsOption) ([]dbos.WorkflowStatus, error) {
	rows, err := ctx.List(append(filters, dbos.WithFilterName(itemName))...)
	if err != nil {
		return nil, fmt.Errorf("list the item runs: %w", err)
	}

	return rows, nil
}

// rootRun climbs from a run to the highest item run above it. A run that no run spawned is its own root.
func rootRun(ctx storeView, id string) (string, error) {
	for {
		rows, err := itemRuns(ctx, dbos.WithFilterWorkflowIDs(id))
		if err != nil || len(rows) == 0 || rows[0].ParentWorkflowID == "" {
			return id, err
		}

		above, err := itemRuns(ctx, dbos.WithFilterWorkflowIDs(rows[0].ParentWorkflowID))
		if err != nil || len(above) == 0 {
			return id, err
		}

		id = above[0].ID
	}
}
