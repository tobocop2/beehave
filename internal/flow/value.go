package flow

import (
	"encoding/json"
	"errors"
	"fmt"

	"go.starlark.net/starlark"
)

const (
	// valueLimit is the most bytes of JSON that a spawned run may return. The value is a recorded output of the run, kept with its result.
	valueLimit = 64 * 1024
	// valueDepth is the deepest nesting of a returned value. A list that holds itself reaches it.
	valueDepth = 32
	// floatCost is the bytes that the conversion counts for a float, the most that one float takes as JSON.
	floatCost = 24
)

// errValueTooBig stops the conversion of a value that passes the byte limit.
var errValueTooBig = fmt.Errorf("the value is larger than %d bytes as JSON", valueLimit)

// returnedValue turns the value that an entry function returned into its JSON text, or says why the run cannot keep it.
// Only a dict with text keys, a list, a tuple, text, a number, a bool and None are kept. None is no value.
// The conversion counts bytes as it goes and stops at the limit, so a value that shares one list many times is refused before it is built.
func returnedValue(entry string, v starlark.Value) (json.RawMessage, error) {
	if v == nil || v == starlark.None {
		return nil, nil
	}

	c := converter{budget: valueLimit}

	data, err := c.encode(v)
	if errors.Is(err, errValueTooBig) {
		return nil, fmt.Errorf("%s returned a value that is larger than %d bytes as JSON. Return a short summary and keep the bulk in a file", entry, valueLimit)
	}

	if err != nil {
		return nil, fmt.Errorf("%s returned a value that is not JSON-safe: %w. Return a dict, list, string, number, bool or None, or remove the return", entry, err)
	}

	return data, nil
}

// converter turns a Starlark value into a Go value that encoding/json writes, and counts the bytes of its JSON text.
type converter struct {
	budget int
}

// encode converts the value and writes its JSON text. The count of the conversion is an estimate, so the text is checked against the limit too.
func (c *converter) encode(v starlark.Value) ([]byte, error) {
	plain, err := c.plain(v, 0)
	if err != nil {
		return nil, err
	}

	data, err := json.Marshal(plain)
	if err != nil {
		return nil, err
	}

	if len(data) > valueLimit {
		return nil, errValueTooBig
	}

	return data, nil
}

// spend takes n bytes from the budget and fails when none are left.
func (c *converter) spend(n int) error {
	c.budget -= n
	if c.budget < 0 {
		return errValueTooBig
	}

	return nil
}

// plain converts a value. A dict keeps its text keys, and encoding/json sorts them, so the text is the same on a replay.
// A float that is not finite is not checked here: json.Marshal refuses it, and the error names it.
func (c *converter) plain(v starlark.Value, depth int) (any, error) {
	if depth > valueDepth {
		return nil, fmt.Errorf("the value nests deeper than %d levels", valueDepth)
	}

	if value, cost, ok := scalar(v); ok {
		return value, c.spend(cost)
	}

	switch x := v.(type) {
	case *starlark.List:
		return c.items(x, depth)
	case starlark.Tuple:
		return c.items(x, depth)
	case *starlark.Dict:
		return c.dict(x, depth)
	}

	return nil, fmt.Errorf("the type %s cannot be kept", v.Type())
}

// scalar converts None, a bool, text, an int or a float, with the bytes that its JSON text takes. It reports false for any other value.
func scalar(v starlark.Value) (any, int, bool) {
	switch x := v.(type) {
	case starlark.NoneType:
		return nil, len("null"), true
	case starlark.Bool:
		return bool(x), len("false"), true
	case starlark.String:
		return string(x), len(x) + len(`""`), true
	case starlark.Int:
		return json.Number(x.String()), len(x.String()), true
	case starlark.Float:
		return float64(x), floatCost, true
	}

	return nil, 0, false
}

// items converts the elements of a list or a tuple. Each element costs its own bytes and a comma, so a long list of empty lists runs out too.
func (c *converter) items(seq starlark.Indexable, depth int) (any, error) {
	out := make([]any, 0, min(seq.Len(), valueLimit))

	err := c.spend(len("[]"))

	for i := 0; i < seq.Len() && err == nil; i++ {
		var one any

		if one, err = c.plain(seq.Index(i), depth+1); err == nil {
			err = c.spend(len(","))
			out = append(out, one)
		}
	}

	return out, err
}

// dict converts a dict whose keys are all text.
func (c *converter) dict(d *starlark.Dict, depth int) (any, error) {
	out := make(map[string]any, min(d.Len(), valueLimit))

	err := c.spend(len("{}"))

	for _, pair := range d.Items() {
		if err != nil {
			break
		}

		err = c.pair(out, pair, depth)
	}

	return out, err
}

// pair converts one key and value of a dict into out. The key must be text.
func (c *converter) pair(out map[string]any, pair starlark.Tuple, depth int) error {
	key, ok := pair[0].(starlark.String)
	if !ok {
		return fmt.Errorf("a dict key has the type %s, and a key must be a string", pair[0].Type())
	}

	if err := c.spend(len(key) + len(`"":,`)); err != nil {
		return err
	}

	one, err := c.plain(pair[1], depth+1)
	out[string(key)] = one

	return err
}

// valueOf makes the Starlark value of a recorded child value: None when the run kept none.
func valueOf(raw json.RawMessage) (starlark.Value, error) { //nolint:ireturn // the value is any of the Starlark types
	if len(raw) == 0 {
		return starlark.None, nil
	}

	return toStarlark(raw)
}
