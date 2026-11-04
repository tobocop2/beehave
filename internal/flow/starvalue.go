package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	starjson "go.starlark.net/lib/json"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/workflow"
)

// stepResult is what a builtin records in its DBOS step. Outputs become the fields of the struct the builtin returns.
// Note carries text that a later part of the same builtin reads on replay. Stopped is set by a command whose code is in its stop_on_rc.
type stepResult struct {
	Outputs map[string]any `json:"outputs,omitempty"`
	Note    string         `json:"note,omitempty"`
	Stopped *Stopped       `json:"stopped,omitempty"`
}

// script is the state of one run of a workflow file. The thread of the run calls its builtins one at a time on the workflow goroutine.
type script struct {
	e     *Engine
	ctx   dbos.Context
	in    ItemInput
	login string

	// sources are the pinned files of the run. spawn asks them whether its entry is a function of the workflow file.
	sources *workflow.Sources

	// counts holds the number of calls so far of each builtin, which numbers its steps.
	counts map[string]int
	// kinds is the table of step kinds that name accepts. It is nil, and so the table of the engine, unless a test sets it.
	kinds []string

	// spent is the sum of the costs that the agent steps of this run recorded so far, marks included. A replay adds the same recorded costs again in the same order.
	spent agent.Spend

	// merge and sentBack are what a merge builtin reported. They go into the result of the item.
	mergeState, sentBack, closed string

	// batches holds the names of the batch_merge calls of this run, so that a name is never used twice.
	batches map[string]bool

	// undos are the finished writes of this run that name an undo, in the order of the writes. A failed run runs them in reverse.
	undos []undoStep

	// gated holds the head and the steps of each gate call of this run. A merge call that merges takes only the steps of a gate call at the head of the merge.
	gated []gatedCall

	// stopped is the command that ended the item on a code of its stop_on_rc.
	stopped *Stopped

	// value is the JSON text that the entry function of a run that spawn started returned.
	value json.RawMessage

	// hostStarted is true once a host_up call of this run launched the host. host_teardown stops no other host.
	hostStarted bool

	// hostChosen is the cluster that host_up recorded. host_teardown stops that cluster when the call names none.
	hostChosen string

	// fixed is true once the first gate has recorded scope, the closing issues of the pull request at the first review.
	// owned holds the issues that the owner kept by an answer.
	fixed bool
	scope []int
	owned []int
}

// name returns the name of the DBOS step of one call: `<builtin>:<label>#<n>`, or `<builtin>#<n>` for a call with no label.
// The count is the position of the call among the calls of that builtin, so a replay that makes the same calls gets the same names.
func (s *script) name(builtin, label string) (string, error) {
	kinds := s.kinds
	if kinds == nil {
		kinds = stepKinds()
	}

	if err := checkStepKind(kinds, builtin); err != nil {
		return "", err
	}

	return s.countedName(builtin, label), nil
}

// countedStep runs fn as the step of the next call of a step kind. The kind must be in the table of step kinds.
func countedStep[T any](s *script, kind, label string, fn func(context.Context) (T, error)) (T, error) { //nolint:ireturn // the step returns the caller's own type
	name, err := s.name(kind, label)
	if err != nil {
		var zero T

		return zero, err
	}

	return runStep(s.e, s.ctx, name, fn)
}

func (s *script) countedName(builtin, label string) string {
	s.counts[builtin]++
	n := strconv.Itoa(s.counts[builtin])

	if label != "" {
		builtin += ":" + label
	}

	return builtin + "#" + n
}

// durable runs one call of a builtin as a recorded DBOS step named `<builtin>:<label>#<n>` and returns the struct of the outputs it recorded.
// fn gets the step name. A step that carries a stop unwinds the run instead.
func (s *script) durable(builtin, label string, fn func(ctx context.Context, name string) (stepResult, error)) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	return s.durableWith(builtin, label, nil, fn)
}

// durableWith is durable for a builtin that gained result fields after its first records. A record that lacks a field reads it as the zero in zeros,
// so a run that an older build recorded replays under a file that reads the new field.
func (s *script) durableWith(builtin, label string, zeros map[string]any, fn func(ctx context.Context, name string) (stepResult, error)) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	fields, err := s.durableFields(builtin, label, zeros, fn)
	if err != nil {
		return nil, err
	}

	return starlarkstruct.FromStringDict(starlarkstruct.Default, fields), nil
}

// durableFields runs a recorded step and returns the Starlark value of each output field, by name.
func (s *script) durableFields(builtin, label string, zeros map[string]any, fn func(ctx context.Context, name string) (stepResult, error)) (starlark.StringDict, error) {
	name, err := s.name(builtin, label)
	if err != nil {
		return nil, err
	}

	done, err := runStep(s.e, s.ctx, name, func(ctx context.Context) (stepResult, error) { return fn(ctx, name) })
	if err != nil {
		return nil, err
	}

	if done.Stopped != nil {
		s.stopped = done.Stopped

		return nil, errStopped
	}

	return fields(withZeros(done.Outputs, zeros))
}

// result makes the struct that a builtin returns from the outputs it recorded.
func result(outputs map[string]any) (*starlarkstruct.Struct, error) {
	dict, err := fields(outputs)
	if err != nil {
		return nil, err
	}

	return starlarkstruct.FromStringDict(starlarkstruct.Default, dict), nil
}

// fields converts each recorded output to a Starlark value.
func fields(outputs map[string]any) (starlark.StringDict, error) {
	dict := starlark.StringDict{}

	for name, v := range outputs {
		value, err := toStarlark(v)
		if err != nil {
			return nil, fmt.Errorf("output %q: %w", name, err)
		}

		dict[name] = value
	}

	return dict, nil
}

// toStarlark converts a recorded value through its JSON text with the decoder of the Starlark JSON library. A first run holds a Go int and
// a replay holds a float64 from JSON. Both give the same Starlark int, and a dict keeps the key order of the text, which is sorted.
func toStarlark(v any) (starlark.Value, error) { //nolint:ireturn // the value is any of the Starlark types
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("a workflow file cannot hold this value: %w", err)
	}

	return starlark.Call(&starlark.Thread{Name: "json"}, starjson.Module.Members["decode"], starlark.Tuple{starlark.String(data)}, nil)
}
