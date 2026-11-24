package batch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// goldenInputs holds the shape of the recorded input of each batch step that a replay compares: the field names in recorded order, the type of each value,
// and for a list the shape of its first element. A replay compares this input byte for byte with the input of the step now, so a change to a field name,
// a field type, the order of the fields or a field of a member strands the runs that recorded the step. The steps belong to the land module of the package flow.
// The gate step is not here: production does not store or compare its input, because the child run is its record, so a change to it needs no bump.
var goldenInputs = map[string]string{
	"heads": "[{item:string,head:string,approved:bool,closing:null}]",
	"union": "{tipCommit:string,tipTree:string,members:[{item:string,head:string,approved:bool,closing:null}]}",
	"tip":   "{}",
}

// shapeOf reads one JSON value from the decoder and reduces it to its shape, so two runs of one call have one shape whatever their values are.
// The fields keep the order of the bytes, because a replay compares the bytes. A list needs a first element to give its shape, so an empty list is an error.
func shapeOf(dec *json.Decoder) (string, error) {
	tok, err := dec.Token()
	if err != nil {
		return "", err
	}

	switch v := tok.(type) {
	case json.Delim:
		if v == '{' {
			return objectShape(dec)
		}

		return listShape(dec)
	case string:
		return "string", nil
	case bool:
		return "bool", nil
	case float64:
		return "number", nil
	default:
		return "null", nil
	}
}

func objectShape(dec *json.Decoder) (string, error) {
	var fields []string

	for dec.More() {
		name, err := dec.Token()
		if err != nil {
			return "", err
		}

		field, err := shapeOf(dec)
		if err != nil {
			return "", err
		}

		fields = append(fields, fmt.Sprint(name)+":"+field)
	}

	if _, err := dec.Token(); err != nil {
		return "", err
	}

	return "{" + strings.Join(fields, ",") + "}", nil
}

func listShape(dec *json.Decoder) (string, error) {
	if !dec.More() {
		return "", errors.New("a list in the recorded input is empty, so the golden cannot read the shape of its elements; give the run a list with an element")
	}

	first, err := shapeOf(dec)
	if err != nil {
		return "", err
	}

	for dec.More() {
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return "", err
		}
	}

	if _, err := dec.Token(); err != nil {
		return "", err
	}

	return "[" + first + "]", nil
}

// recordedInputShape is the shape of the input in the record of the first step of a kind.
func recordedInputShape(t *testing.T, w *world, kind string) string {
	t.Helper()

	var env struct {
		Input json.RawMessage `json:"input"`
	}

	raw, ok := w.store.data["batch:q."+kind+"#1"]
	require.True(t, ok, "the batch recorded no step of the kind %s", kind)
	require.NoError(t, json.Unmarshal(raw, &env))

	shape, err := shapeOf(json.NewDecoder(bytes.NewReader(env.Input)))
	require.NoError(t, err)

	return shape
}

func TestRun_TheRecordedInputOfEachComparedBatchStepIsTheGoldenShape(t *testing.T) {
	t.Parallel()

	w := newWorld()
	_, err := Run(context.Background(), w.deps(), members("a", "b"))
	require.NoError(t, err)

	for kind, golden := range goldenInputs {
		assert.Equal(t, golden, recordedInputShape(t, w, kind),
			"the recorded input of the batch step %s changed: bump the version of the land module in moduleTable (internal/flow/stepmodules.go) and update the golden input", kind)
	}
}
