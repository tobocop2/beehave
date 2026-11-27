package flow

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"go.starlark.net/starlark"

	"github.com/tobocop2/beehest/internal/workflow"
)

// The step kind and the builtin name of earlier_reads, its keywords and its result field. waitStepKind is the kind of the steps of wait, which earlier_reads reads beside pr_closing.
const (
	earlierReadsKind = "earlier_reads"
	waitStepKind     = "wait"
	kindKeyword      = "kind"
	ownRunKeyword    = "own_run"
	readsField       = "reads"
)

// waitRead is what a wait step recorded, as earlier_reads gives it back: the answer, which is empty when the wait timed out.
type waitRead struct {
	Answer string `json:"answer"`
}

// stepReader collects the reads of one step kind from the recorded steps of the runs with the ids. It gives a list of the typed reads of that kind.
type stepReader func(ctx dbos.Context, ids []string, prefix string) (any, error)

// earlierReaders maps each step kind that earlier_reads reads to the function that reads it.
func earlierReaders() map[string]stepReader {
	return map[string]stepReader{prClosingKind: readerOf[closingRead], waitStepKind: readerOf[waitRead]}
}

// readerOf is the stepReader of the reads of type T.
func readerOf[T any](ctx dbos.Context, ids []string, prefix string) (any, error) {
	return collect[T](ctx, ids, prefix)
}

// earlierReads gives the reads that the recorded steps of a kind and a name made in the other runs of the item, in one recorded step.
// The newest run comes first, and the newest step of a run comes first in that run. With own_run the steps of this run that come before the call come first of all.
// A step that failed and a record that cannot be read are left out. The item is the item of the run when the call names none. A replay returns the first read.
func (s *script) earlierReads(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var (
		kind, name, item string
		own              bool
	)

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{kindKeyword: &kind, nameKeyword: &name, itemKeyword: &item, ownRunKeyword: &own}); err != nil {
		return nil, err
	}

	if _, known := earlierReaders()[kind]; !known {
		return nil, fmt.Errorf("%s cannot read steps of kind %q. Use one of: %s", b.Name(), kind, strings.Join(slices.Sorted(maps.Keys(earlierReaders())), ", "))
	}

	return s.durable(earlierReadsKind, name, func(context.Context, string) (stepResult, error) {
		reads, err := s.readEarlier(kind, name, cmp.Or(item, s.in.Item), own)

		return stepResult{Outputs: earlierReadsOutputs(reads)}, err
	})
}

// earlierReadsOutputs are the fields of earlier_reads: the typed reads of the kind, newest first.
func earlierReadsOutputs(reads any) map[string]any { return map[string]any{readsField: reads} }

// readEarlier reads the steps of the kind and the name in the runs of the item. The kind is one of earlierReaders.
func (s *script) readEarlier(kind, name, item string, own bool) (any, error) {
	ids, err := s.runIDs(item, own)
	if err != nil {
		return nil, err
	}

	return earlierReaders()[kind](s.e.ctx, ids, stepPrefix(kind, name))
}

// runIDs lists the ids of the earlier runs of the item, newest first, and with own the id of this run before them.
func (s *script) runIDs(item string, own bool) ([]string, error) {
	ids, err := s.earlierRuns(item)
	if err != nil || !own {
		return ids, err
	}

	return slices.Insert(ids, 0, workflowID(s.ctx)), nil
}

// earlierRuns lists the ids of the other runs of the item, the newest attempt first. It filters on no state and no attempt number, so a canceled, failed, live or newer run is in the list.
// A caller that counts work over these runs counts a concurrent run as spent: that errs on the safe side for a cap.
func (s *script) earlierRuns(item string) ([]string, error) {
	attempts, err := attemptsOf(s.e.store(), item)
	if err != nil {
		return nil, fmt.Errorf("list the earlier runs of item %s: %w", item, err)
	}

	attempts = slices.DeleteFunc(attempts, func(a run) bool { return a.Status.ID == workflowID(s.ctx) })
	slices.SortFunc(attempts, func(a, b run) int { return b.Attempt - a.Attempt })

	ids := make([]string, len(attempts))
	for i, attempt := range attempts {
		ids[i] = attempt.Status.ID
	}

	return ids, nil
}

// collect reads the typed reads of the steps whose name starts with the prefix, run after run in the order of the ids, the newest step of a run first.
func collect[T any](ctx dbos.Context, ids []string, prefix string) ([]T, error) {
	reads := []T{}

	for _, id := range ids {
		steps, err := dbos.GetWorkflowSteps(ctx, id, dbos.WithStepsLoadOutput(true))
		if err != nil {
			return nil, fmt.Errorf("read the steps of %s: %w", id, err)
		}

		slices.SortFunc(steps, func(a, b dbos.StepInfo) int { return stepNumber(b.StepName, prefix) - stepNumber(a.StepName, prefix) })

		for _, step := range steps {
			var read T
			if recordedRead(step, prefix, &read) {
				reads = append(reads, read)
			}
		}
	}

	return reads, nil
}

// stepNumber is the number of the call that a step name ends with, `<prefix><n>`. A name of another step has none and gives 0, which sorts last.
func stepNumber(name, prefix string) int {
	tail, named := strings.CutPrefix(name, prefix)
	number, err := strconv.Atoi(tail)

	if !named || err != nil {
		return 0
	}

	return number
}

// recordedRead decodes the outputs of a finished step whose name is `<prefix><n>`. A step of another name, a sub-step, a step that failed and a record that cannot be read give false.
// The store keeps the output of a step as JSON text.
func recordedRead[T any](step dbos.StepInfo, prefix string, out *T) bool {
	var record struct {
		Outputs T `json:"outputs"`
	}

	text, isText := step.Output.(string)
	if !isText || step.Error != nil || stepNumber(step.StepName, prefix) == 0 || json.Unmarshal([]byte(text), &record) != nil {
		return false
	}

	*out = record.Outputs

	return true
}
