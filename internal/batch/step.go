package batch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

type envelope[In, Out any] struct {
	Input  In  `json:"input"`
	Output Out `json:"output"`
}

type rawEnvelope struct {
	Input  json.RawMessage `json:"input"`
	Output json.RawMessage `json:"output"`
}

// step records one result with the input that made it, and stores the result in out.
// A replay returns the recorded output, and an input that differs from the recorded one is an error.
// A step name alone stands for different input when the code changed between the first run and the replay, so the input is compared too.
func step[In, Out any](ctx context.Context, rec Recorder, name string, in In, run func() (Out, error), out *Out) error {
	want, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}

	raw, err := rec.Step(ctx, name, func() ([]byte, error) {
		result, err := run()
		if err != nil {
			return nil, err
		}

		return json.Marshal(envelope[In, Out]{Input: in, Output: result})
	})
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}

	return decode(name, want, raw, out)
}

func decode[Out any](name string, want, raw []byte, out *Out) error {
	var env rawEnvelope

	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("the record of %s cannot be read: %w", name, err)
	}

	if !bytes.Equal(env.Input, want) {
		return fmt.Errorf("the record of %s ran on other input: recorded %s, now %s", name, env.Input, want)
	}

	if err := json.Unmarshal(env.Output, out); err != nil {
		return fmt.Errorf("the record of %s cannot be read: %w", name, err)
	}

	return nil
}
