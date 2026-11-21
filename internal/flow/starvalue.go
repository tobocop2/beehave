package flow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	starjson "go.starlark.net/lib/json"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/workflow"
)

// stepResult is what a builtin records in its DBOS step. Outputs become the fields of the struct the builtin returns.
// Note carries text that a later part of the same builtin reads on replay. Stopped is set by a command whose code is in its stop_on_rc.
type stepResult struct {
	Outputs map[string]any `json:"outputs,omitempty"`
	Note    string         `json:"note,omitempty"`
	// Final is true for a Note that a new try of the agent cannot cure, so the step fails with no other try.
	Final   bool     `json:"final,omitempty"`
	Stopped *Stopped `json:"stopped,omitempty"`
}

// UnmarshalJSON reads a recorded step with each number kept as a json.Number, so that a replay holds the digits and the int or float kind
// that the first run recorded. A plain decode would turn every number into a float64.
func (r *stepResult) UnmarshalJSON(data []byte) error {
	type plain stepResult

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()

	var decoded plain

	if err := decoder.Decode(&decoded); err != nil {
		return fmt.Errorf("decode a recorded step: %w", err)
	}

	*r = stepResult(decoded)

	return nil
}

// script is the state of one run of a workflow file. The thread of the run calls its builtins one at a time on the workflow goroutine.
type script struct {
	e     *Engine
	ctx   dbos.Context
	in    ItemInput
	login string
	// treeStart is the commit that the recorded step made the worktree of a round item at. A step that finds the worktree removed makes it again there.
	// It is empty for any other item.
	treeStart string
	// severity is the severity that the tracker gave the item, from the recorded fields of the run. It sets the place of the gates and the agent steps of the run in their queues.
	severity model.Severity

	// sources are the pinned files of the run. spawn asks them whether its entry is a function of the workflow file.
	sources *workflow.Sources

	// counts holds the number of calls so far of each builtin, which numbers its steps.
	counts map[string]int
	// kinds is the table of step kinds that name accepts. It is nil, and so the table of the registry of the engine, unless a test sets it.
	kinds []string

	// batches holds the names of the merge calls of this run, so that a name is never used twice.
	batches map[string]bool

	// undos are the finished writes of this run that name an undo, in the order of the writes. A failed run runs them in reverse.
	undos []undoStep

	// stopped is the command that ended the item on a code of its stop_on_rc.
	stopped *Stopped

	// value is the JSON text that the entry function of a run that spawn started returned.
	value json.RawMessage

	// spawned holds the runs that the spawn calls of this run returned, started or reused. gather accepts only these.
	spawned []spawnedRun

	// keys holds the keys that publish set in this run, in the order of their first use. The output of each publish step holds the list, so a replay rebuilds it.
	keys []string
	// inbox holds the messages that a recv received for another topic, oldest first. The receives are recorded steps, so a replay builds the same buffer.
	inbox []envelope

	// hostStarted is true once a host_up call of this run launched the host. host_teardown stops no other host.
	hostStarted bool

	// hostChosen is the cluster that host_up recorded. host_teardown stops that cluster when the call names none.
	hostChosen string

	// fixed is true once the first gate has recorded scope, the closing issues of the pull request at the first review.
	// owned holds the issues that the owner kept by an answer, for each item.
	fixed bool
	scope []int
	owned map[string][]int
}

// name returns the name of the DBOS step of one call: `<builtin>:<label>#<n>`, or `<builtin>#<n>` for a call with no label.
// The count is the position of the call among the calls of that builtin, so a replay that makes the same calls gets the same names.
func (s *script) name(builtin, label string) (string, error) {
	kinds := s.kinds
	if kinds == nil {
		kinds = s.e.registry.Kinds()
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
	name := s.nextName(builtin, label)
	s.counts[builtin]++

	return name
}

// nextName is the name that the next call of builtin with label records, `<builtin>:<label>#<n>`. It counts nothing.
func (s *script) nextName(builtin, label string) string {
	n := strconv.Itoa(s.counts[builtin] + 1)

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
// a replay holds a json.Number from the record. Both give the same Starlark int, and a dict keeps the key order of the text, which is sorted.
func toStarlark(v any) (starlark.Value, error) { //nolint:ireturn // the value is any of the Starlark types
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("a workflow file cannot hold this value: %w", err)
	}

	return starlark.Call(&starlark.Thread{Name: "json"}, starjson.Module.Members["decode"], starlark.Tuple{starlark.String(data)}, nil)
}
