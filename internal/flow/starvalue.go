package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

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

// optional reads an argument that is a list, a dict or another value of type T. None and an absent argument give the zero value of T.
// The second result is false when the argument has another type.
func optional[T starlark.Value](v starlark.Value) (out T, ok bool) { //nolint:ireturn // the argument is any of the Starlark types
	if v == nil || v == starlark.None {
		return out, true
	}

	out, ok = v.(T)

	return out, ok
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

// toGo converts a value of a workflow file to a Go value for a prompt template or a request, through the encoder of the Starlark JSON library.
// A struct becomes a map. A number becomes an int64, or a float64 when it has a fraction, so that eq, gt and printf work on it.
// An integer beyond int64 stays a json.Number, which keeps its digits. A float that is NaN or infinite is refused by the encoder.
// T is any, or the type that the caller expects.
func toGo[T any](v starlark.Value) (out T, err error) { //nolint:ireturn // T is any when the caller wants every kind of value
	decoded, err := encodeDecode(v)
	if err != nil {
		return out, err
	}

	settled := settle(decoded)

	out, ok := settled.(T)
	if !ok && settled != nil {
		err = fmt.Errorf("a %s is not the kind of value this place takes", v.Type())
	}

	return out, err
}

// encodeDecode sends v through the encoder of the Starlark JSON library and decodes the text, with a number kept as a json.Number.
func encodeDecode(v starlark.Value) (decoded any, err error) {
	text, err := starlark.Call(&starlark.Thread{Name: "json"}, starjson.Module.Members["encode"], starlark.Tuple{v}, nil)
	if err != nil {
		return nil, fmt.Errorf("a value cannot go into a prompt or a request. Use text, numbers, booleans, lists and dicts (%w)", err)
	}

	encoded, _ := starlark.AsString(text) // json.encode returns a string.
	decoder := json.NewDecoder(strings.NewReader(encoded))
	decoder.UseNumber()

	err = decoder.Decode(&decoded) // The text comes from the encoder, so it always decodes.

	return decoded, err
}

// settle walks a decoded value. A json.Number becomes an int64 or a float64.
// An integer beyond int64 is left as a json.Number.
func settle(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for key, item := range x {
			x[key] = settle(item)
		}
	case []any:
		for i, item := range x {
			x[i] = settle(item)
		}
	case json.Number:
		return settleNumber(x)
	}

	return v
}

// settleNumber returns an int64 for a whole number in range, a float64 for a number with a fraction or an exponent, and the number itself
// for an integer beyond int64.
func settleNumber(x json.Number) any {
	if n, err := x.Int64(); err == nil {
		return n
	}

	if f, err := x.Float64(); err == nil && strings.ContainsAny(string(x), ".eE") {
		return f
	}

	return x
}

// elements returns the members of a list, tuple or other sequence. The second result is false when v is not a sequence.
// None and an absent argument are an empty sequence.
func elements(v starlark.Value) ([]starlark.Value, bool) {
	seq, ok := optional[starlark.Sequence](v)
	if !ok || seq == nil {
		return nil, ok
	}

	out := make([]starlark.Value, 0, seq.Len())

	it := seq.Iterate()
	defer it.Done()

	for item := starlark.Value(nil); it.Next(&item); {
		out = append(out, item)
	}

	return out, true
}

// words reads an argument that is a list of strings. None and an absent argument are empty.
func words(name string, v starlark.Value) ([]string, error) {
	return listOfEach(v, func(item starlark.Value) (string, error) {
		text, ok := starlark.AsString(item)
		if !ok {
			return "", fmt.Errorf("%s must hold strings only, and %s is a %s", name, item, item.Type())
		}

		return text, nil
	}, fmt.Errorf("%s must be a list of strings", name))
}

// listOfEach reads an argument that is a list and converts each member with one. None and an absent argument give an empty list.
// A value that is not a list gives notList.
func listOfEach[T any](v starlark.Value, one func(starlark.Value) (T, error), notList error) ([]T, error) {
	items, ok := elements(v)
	if !ok {
		return nil, notList
	}

	var out []T

	for _, item := range items {
		value, err := one(item)
		if err != nil {
			return nil, err
		}

		out = append(out, value)
	}

	return out, nil
}

// duration reads a timeout argument. An empty text is zero.
func duration(name, text string) (time.Duration, error) {
	if text == "" {
		return 0, nil
	}

	d, err := time.ParseDuration(text)
	if err != nil {
		return 0, fmt.Errorf("%s is %q, which is not a duration. Write a duration such as \"90s\" or \"10m\"", name, text)
	}

	return d, nil
}
