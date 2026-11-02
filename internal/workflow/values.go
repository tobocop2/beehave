package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	starjson "go.starlark.net/lib/json"
	"go.starlark.net/starlark"
)

// The functions of this file own the rules for the values of the arguments of a builtin. The run calls them with the value it got. The check calls
// them with the value of a literal, so a literal that the run refuses is a problem before the run starts.

// Optional reads an argument that is a list, a dict or another value of type T. None and an absent argument give the zero value of T.
// The second result is false when the argument has another type.
func Optional[T starlark.Value](v starlark.Value) (out T, ok bool) { //nolint:ireturn // the argument is any of the Starlark types
	if v == nil || v == starlark.None {
		return out, true
	}

	out, ok = v.(T)

	return out, ok
}

// Elements returns the members of a list, tuple or other sequence. The second result is false when v is not a sequence.
// None and an absent argument are an empty sequence.
func Elements(v starlark.Value) ([]starlark.Value, bool) {
	seq, ok := Optional[starlark.Sequence](v)
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

// Words reads an argument that is a list of strings. None and an absent argument are empty.
func Words(name string, v starlark.Value) ([]string, error) {
	return ListOfEach(v, func(item starlark.Value) (string, error) {
		text, ok := starlark.AsString(item)
		if !ok {
			return "", fmt.Errorf("%s must hold strings only, and %s is a %s", name, item, item.Type())
		}

		return text, nil
	}, fmt.Errorf("%s must be a list of strings", name))
}

// ListOfEach reads an argument that is a list and converts each member with one. None and an absent argument give an empty list.
// A value that is not a list gives notList.
func ListOfEach[T any](v starlark.Value, one func(starlark.Value) (T, error), notList error) ([]T, error) {
	items, ok := Elements(v)
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

// Duration reads a timeout argument. An empty text is zero.
func Duration(name, text string) (time.Duration, error) {
	if text == "" {
		return 0, nil
	}

	d, err := time.ParseDuration(text)
	if err != nil {
		return 0, fmt.Errorf("%s is %q, which is not a duration. Write a duration such as \"90s\" or \"10m\"", name, text)
	}

	return d, nil
}

// Deadline reads a timeout argument that the caller gave and that sets a deadline. A deadline of zero or less would mean no deadline, so it is refused.
func Deadline(name, text string) (time.Duration, error) {
	d, err := Duration(name, text)
	if err != nil {
		return 0, err
	}

	if d <= 0 {
		return 0, fmt.Errorf("%s is %q, which must be above zero. Write a duration such as \"48h\", or leave %s out to wait with no deadline", name, text, name)
	}

	return d, nil
}

// Budget refuses a cap in dollars that is negative, infinite or not a number. 0 means no cap.
func Budget(name string, dollars float64) error {
	if !(dollars >= 0) || math.IsInf(dollars, 0) { // the negated form also refuses NaN
		return fmt.Errorf("%s must be 0 (no cap) or more, in dollars, and a finite number", name)
	}

	return nil
}

// OutputTries refuses a number of tries below one.
func OutputTries(n int) error {
	if n < 1 {
		return errors.New("output_tries must be a whole number of 1 or more")
	}

	return nil
}

// RoundsKeyword is the keyword of the agent builtin that names the cap of the loop that the call belongs to.
const RoundsKeyword = "rounds"

// Rounds reads the cap of a loop: a whole number of 1 or more. The status view shows the rounds used against it.
func Rounds(v starlark.Value) (int, error) {
	n, isInt := v.(starlark.Int)
	if !isInt {
		return 0, errors.New(RoundsKeyword + " must be a whole number of 1 or more")
	}

	limit, small := n.Int64()
	if !small || limit < 1 || limit > math.MaxInt32 {
		return 0, errors.New(RoundsKeyword + " must be a whole number of 1 or more")
	}

	return int(limit), nil
}

// StopCodes reads the stop_on_rc list. Each code must be a whole number from 1 to 255.
func StopCodes(v starlark.Value) ([]int, error) {
	return ListOfEach(v, func(item starlark.Value) (int, error) {
		code, isInt := item.(starlark.Int)
		n, small := code.Int64()

		if !isInt || !small || !StopCode(int(n)) {
			return 0, errors.New("stop_on_rc must hold whole numbers from 1 to 255. Write each exit code as a number")
		}

		return int(n), nil
	}, errors.New("stop_on_rc must be a list of exit codes. Write stop_on_rc = [2]"))
}

// ProcessCap reads the max_new_processes keyword: a whole number of 1 or more, or zero for a call that sets none. A call with a host cannot set it,
// because a host job takes a command line only.
func ProcessCap(v starlark.Value, host string) (int, error) {
	if v == nil {
		return 0, nil
	}

	if host != "" {
		return 0, errors.New("max_new_processes cannot be used together with host. A host job takes a command line only. Remove max_new_processes or host")
	}

	n, isInt := v.(starlark.Int)
	limit, small := n.Int64()

	if !isInt || !small || limit < MinProcessCap || limit > MaxProcessCap {
		return 0, fmt.Errorf("max_new_processes must be a whole number from %d to %d. Write max_new_processes = 50", MinProcessCap, MaxProcessCap)
	}

	return int(limit), nil
}

// AllowPaths reads the allow_paths argument: a list of globs in the doublestar dialect. A leading `./` is dropped from each glob, because the
// paths that git lists have none. It returns the patterns as written and the clean globs. A missing argument gives none. An empty list is refused.
func AllowPaths(v starlark.Value) (patterns, globs []string, err error) {
	patterns, err = Words("allow_paths", v)
	if err != nil {
		return nil, nil, err
	}

	if len(patterns) == 0 {
		if v == nil || v == starlark.None {
			return nil, nil, nil
		}

		return nil, nil, errors.New("allow_paths is an empty list, which would allow every path. To turn the check off, leave the keyword out")
	}

	globs, err = cleanGlobs(patterns)

	return patterns, globs, err
}

// cleanGlobs drops the leading `./` of each pattern and refuses a pattern that does not parse.
func cleanGlobs(patterns []string) ([]string, error) {
	globs := make([]string, 0, len(patterns))

	for _, pattern := range patterns {
		clean := strings.TrimPrefix(pattern, "./")
		if !doublestar.ValidatePattern(clean) {
			return nil, fmt.Errorf("allow_paths has the pattern %q, which does not parse: %w", pattern, doublestar.ErrBadPattern)
		}

		globs = append(globs, clean)
	}

	return globs, nil
}

// InputMap converts the inputs dict of an agent call into the data of its prompt template. An absent dict is empty.
func InputMap(v starlark.Value) (map[string]any, error) {
	dict, ok := Optional[*starlark.Dict](v)
	if !ok {
		return nil, fmt.Errorf("inputs must be a dict, not a %s. Write inputs = {\"name\": value}", v.Type())
	}

	if dict == nil {
		return map[string]any{}, nil
	}

	return ToGo[map[string]any](dict)
}

// ToGo converts a value of a workflow file to a Go value for a prompt template or a request, through the encoder of the Starlark JSON library.
// A struct becomes a map. A number becomes an int64, or a float64 when it has a fraction, so that eq, gt and printf work on it.
// An integer beyond int64 stays a json.Number, which keeps its digits. A float that is NaN or infinite is refused by the encoder.
// T is any, or the type that the caller expects.
func ToGo[T any](v starlark.Value) (out T, err error) { //nolint:ireturn // T is any when the caller wants every kind of value
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
