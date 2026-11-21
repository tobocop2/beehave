package flow

import (
	"context"
	"fmt"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/beehave/internal/workflow"
)

// restartPlan is one run that gather starts again: the next attempt of the item, with the input of the attempt that failed and a new login.
type restartPlan struct {
	Run   spawnedRun `json:"run"`
	Input ItemInput  `json:"input"`
}

// retriedRuns refuses a retries value out of range, then plans the runs of a spawn call, each with the retries of the call.
func (s *script) retriedRuns(call spawnCall) ([]spawnedRun, error) {
	if _, err := workflow.Retry(call.retries, ""); err != nil {
		return nil, err
	}

	plan, err := s.planRuns(call)
	for i := range plan {
		plan[i].Retries = call.retries
	}

	return plan, err
}

// retries reports whether the run failed and gather starts it again: it ended without success, nobody canceled it by hand, and it has retries left.
func (l look) retries() bool {
	return !l.open && !l.out.OK && !l.canceled && l.ref.Retries > 0
}

// retrying lists the places of the runs that gather starts again.
func retrying(looks []look) []int {
	var places []int

	for i, l := range looks {
		if l.retries() {
			places = append(places, i)
		}
	}

	return places
}

// gatherRetried runs the gather step name. While a run failed with retries left, it starts that run again as the next attempt of its item, outside a step,
// and waits once more in the step `<name>.<k>`. The plan of each start again is the recorded step `<name>.retry<k>`, so a replay starts nothing twice.
func (s *script) gatherRetried(name string, refs []spawnedRun, call gatherCall) (gathered, error) {
	attempts := map[string]int{}
	step := name

	for round := 1; ; round++ {
		done, err := runStep(s.e, s.ctx, step, func(c context.Context) (gathered, error) {
			return s.e.gather(c, s.in, refs, call.fail == failRaise, call.until == untilAny)
		})
		if err != nil || len(done.Retry) == 0 {
			countAttempts(done.Outcomes, attempts)

			return done, err
		}

		if refs, err = s.restart(fmt.Sprintf("%s.retry%d", name, round), refs, done.Retry, attempts); err != nil {
			return gathered{}, err
		}

		step = fmt.Sprintf("%s.%d", name, round)
	}
}

// countAttempts sets the attempts of each outcome: 1, and one more for each start again of its item.
func countAttempts(outcomes []childOutcome, restarts map[string]int) {
	for i := range outcomes {
		outcomes[i].Attempts = 1 + restarts[outcomes[i].Item]
	}
}

// restart records the plan of the runs to start again in the step name, then starts each one as a child of this run, and counts each start in restarts.
// It returns the runs of the call with each restarted run in its place.
func (s *script) restart(name string, refs []spawnedRun, places []int, restarts map[string]int) ([]spawnedRun, error) {
	plans, err := runStep(s.e, s.ctx, name, func(c context.Context) ([]restartPlan, error) { return s.e.planRestarts(c, s.in, refs, places) })
	if err != nil {
		return nil, err
	}

	next := append([]spawnedRun(nil), refs...)

	for k, place := range places {
		one := plans[k]
		if _, err := dbos.RunWorkflow(s.ctx, s.e.item, one.Input, dbos.WithQueue(s.e.itemQueues[one.Input.Depth]), dbos.WithWorkflowID(one.Run.Run), dbos.WithPriority(one.Run.Priority)); err != nil {
			return nil, fmt.Errorf("start the run of item %s again: %w", one.Run.Item, err)
		}

		next[place] = one.Run
		restarts[one.Run.Item]++
	}

	return next, nil
}

// planRestarts plans the next attempt of each failed run: its run id, a login, its place in the queue, and the input of the attempt that failed.
func (e *Engine) planRestarts(ctx context.Context, in ItemInput, refs []spawnedRun, places []int) ([]restartPlan, error) {
	plans := make([]restartPlan, 0, len(places))

	var taken []string

	for _, place := range places {
		plan, err := e.planRestart(ctx, in, refs[place], taken)
		if err != nil {
			return nil, err
		}

		taken = append(taken, plan.Run.Login)
		plans = append(plans, plan)
	}

	return plans, nil
}

// planRestart plans the next attempt of one failed run. The new attempt keeps the input of the failed one, with the new login.
func (e *Engine) planRestart(ctx context.Context, in ItemInput, failed spawnedRun, taken []string) (restartPlan, error) {
	last, _, err := latestRun(e.ctx, failed.Item)
	if err != nil {
		return restartPlan{}, err
	}

	input, err := last.input()
	if err != nil {
		return restartPlan{}, err
	}

	next, err := e.startOf(ctx, in, failed.Item, runID(failed.Item, last.Attempt+1), taken)
	if err != nil {
		return restartPlan{}, err
	}

	next.Retries = failed.Retries - 1
	input.Login = next.Login

	return restartPlan{Run: next, Input: input}, nil
}
