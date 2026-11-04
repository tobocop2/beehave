package flow

import (
	"context"
	"fmt"
	"strings"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"go.starlark.net/starlark"

	"github.com/tobocop2/beehave/internal/workflow"
)

// callKeyword is the keyword of earlier_calls that names the agent calls to count.
const callKeyword = "call"

// earlierCalls counts the agent calls named `call` in the other runs of the item, so a cap on the calls for one pull request holds over every run of the item.
// The run records of the engine are the store: the call is a recorded step, so a replay reads the count of the first start and a resume keeps it.
// The call counts every call that started, finished or not, because a lane that started has used a round. A lane that still waits for a usage cap before it renders its prompt has not started and is not in the count.
// The runs count whatever their state or attempt number: a canceled, failed, live or newer run counts its lanes as spent. This errs on the safe side, because the cap can be used up early and never exceeded.
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

// countEarlier adds up the agent calls named call in every other run of the item.
func (s *script) countEarlier(call string) (int, error) {
	ids, err := s.earlierRuns()
	if err != nil {
		return 0, err
	}

	return callsInRuns(s.e.ctx, ids, call)
}

// callsInRuns adds up the agent calls named call in the runs with the given ids.
func callsInRuns(ctx dbos.Context, ids []string, call string) (int, error) {
	total := 0

	for _, id := range ids {
		steps, err := dbos.GetWorkflowSteps(ctx, id)
		if err != nil {
			return 0, fmt.Errorf("read the steps of %s: %w", id, err)
		}

		total += callsNamed(steps, call)
	}

	return total, nil
}

// callsNamed counts the distinct agent calls named call in a list of steps. A call leaves a record for the call and records for its sub-steps (`agent:<call>#<n>/...`).
func callsNamed(steps []dbos.StepInfo, call string) int {
	prefix := "agent:" + call + "#"
	seen := map[string]bool{}

	for _, step := range steps {
		if strings.HasPrefix(step.StepName, prefix) {
			base, _, _ := strings.Cut(step.StepName, "/")
			seen[base] = true
		}
	}

	return len(seen)
}
