package flow

import (
	"context"
	"fmt"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"go.starlark.net/starlark"

	"github.com/tobocop2/beehave/internal/workflow"
)

// callKeyword is the keyword of earlier_calls that names the agent calls to count.
const callKeyword = "call"

// earlierCalls counts the agent calls named `call` that the item started before this run, so a cap on the calls for one pull request holds over every run of the item.
// The ledger is the store, not the run records, because the retention deletes the run records: the call is a recorded step, so a replay reads the count of the first start and a resume keeps it.
// The call counts every call that started, finished or not, because a lane that started has used a round. A lane that still waits for a usage cap before it starts its agent is not in the count.
// A new try of a call and the rerun after a provider limit are part of one lane.
// The count holds the calls of every run of the item, whatever its state or attempt number, so the cap can be used up early and never exceeded.
// The calls of the run itself are not in the count.
func (s *script) earlierCalls(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var call, label string

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{callKeyword: &call, nameKeyword: &label}); err != nil {
		return nil, err
	}

	return s.durable("earlier_calls", label, func(context.Context, string) (stepResult, error) {
		count, err := s.countEarlier(call)

		return stepResult{Outputs: earlierOutputs(count)}, err
	})
}

func earlierOutputs(count int) map[string]any { return map[string]any{"count": count} }

// countEarlier counts the agent calls named call that the item started and that no step of this run recorded.
func (s *script) countEarlier(call string) (int, error) {
	own, err := s.e.executionsOf(workflowID(s.ctx))
	if err != nil {
		return 0, err
	}

	return s.e.ledger.CountCalls(s.e.ctx, s.in.Item, "agent:"+call+"#", own)
}

// executionsOf lists the ids of the agent executions that the steps of the run recorded. A caller outside a run has an empty run id and gets none.
// The id comes from the recorded output of the phase, never from a run id: a run id repeats after the retention deleted the run.
func (e *Engine) executionsOf(run string) ([]string, error) {
	if run == "" {
		return nil, nil
	}

	steps, err := dbos.GetWorkflowSteps(e.ctx, run, dbos.WithStepsLoadOutput(true))
	if err != nil {
		return nil, fmt.Errorf("read the steps of %s: %w", run, err)
	}

	var own []string

	for _, step := range steps {
		if step.ChildWorkflowID != "" {
			decodeThen(step.Output, func(phase PhaseOutput) { own = append(own, phase.Execution) })
		}
	}

	return own, nil
}
