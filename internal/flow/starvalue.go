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

	// merge and sentBack are what a merge builtin reported. They go into the result of the item.
	mergeState, sentBack, closed string

	// batches holds the names of the batch_merge calls of this run, so that a name is never used twice.
	batches map[string]bool

	// stopped is the command that ended the item on a code of its stop_on_rc.
	stopped *Stopped

	// hostStarted is true once a host_up call of this run launched the host. host_teardown stops no other host.
	hostStarted bool

	// fixed is true once the first gate has recorded scope, the closing issues of the pull request at the first review.
	// owned holds the issues that the owner kept by an answer.
	fixed bool
	scope []int
	owned []int
}

// name returns the name of the DBOS step of one call: `<builtin>:<label>#<n>`, or `<builtin>#<n>` for a call with no label.
// The count is the position of the call among the calls of that builtin, so a replay that makes the same calls gets the same names.
func (s *script) name(builtin, label string) string {
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
	name := s.name(builtin, label)

	done, err := runStep(s.e, s.ctx, name, func(ctx context.Context) (stepResult, error) { return fn(ctx, name) })
	if err != nil {
		return nil, err
	}

	if done.Stopped != nil {
		s.stopped = done.Stopped

		return nil, errStopped
	}

	return result(done.Outputs)
}

// result makes the struct that a builtin returns from the outputs it recorded.
func result(outputs map[string]any) (*starlarkstruct.Struct, error) {
	dict := starlark.StringDict{}

	for name, v := range outputs {
		value, err := toStarlark(v)
		if err != nil {
			return nil, fmt.Errorf("output %q: %w", name, err)
		}

		dict[name] = value
	}

	return starlarkstruct.FromStringDict(starlarkstruct.Default, dict), nil
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
