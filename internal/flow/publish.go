package flow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/tobocop2/beehave/internal/workflow"
)

const (
	// userEventPrefix starts the DBOS event name of a published key.
	userEventPrefix = "u."
	// keysEvent is the DBOS event that lists the keys a run has published, because DBOS has no call that lists the events of a run.
	keysEvent = "beehave.keys"
	// publishKind and progressKind are the step kinds of the two builtins.
	publishKind  = "publish"
	progressKind = "progress"
	keyField     = "key"
	valueField   = "value"
	// runField and runsField are the field of one spawned run and the argument and field that hold a list of them.
	runField  = "run"
	runsField = "runs"
)

// eventValue is the value of one published key as its event holds it. It is a struct so that a published None differs from a key that was never published.
type eventValue struct {
	Value json.RawMessage `json:"value"`
}

// published is the output of a publish step: the keys of the run so far, in the order of their first use.
type published struct {
	Keys []string `json:"keys"`
}

// childProgress is what a progress step read from one child: whether it published the key, and the value.
type childProgress struct {
	Item  string          `json:"item"`
	Run   string          `json:"run"`
	Set   bool            `json:"set"`
	Value json.RawMessage `json:"value,omitempty"`
}

// publish sets the value of a key of this run. A parent reads it with progress while this run goes on. It overwrites the last value of the key.
func (s *script) publish(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var (
		key, label string
		value      starlark.Value
	)

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{keyField: &key, valueField: &value, nameKeyword: &label}); err != nil {
		return nil, err
	}

	raw, err := eventText(key, value)
	if err != nil {
		return nil, err
	}

	return starlark.None, s.setKey(label, key, raw)
}

// setKey records the step that sets the key, and keeps the list of keys that the step returned. A replay of the step returns the same list.
func (s *script) setKey(label, key, text string) error {
	done, err := countedStep(s, publishKind, label, func(c context.Context) (published, error) { return s.e.setEvents(c, s.keys, key, s.e.redact(text)) })
	if err == nil {
		s.keys = done.Keys
	}

	return err
}

// eventText is the JSON text of a value to publish under a key. It refuses a key that breaks the rule of keys. A None is the JSON null, and a value over the cap of a run value is refused.
func eventText(key string, v starlark.Value) (string, error) {
	if err := workflow.CheckEventKey(key); err != nil {
		return "", err
	}

	c := converter{budget: valueLimit}

	data, err := c.encode(v)
	if errors.Is(err, errValueTooBig) {
		return "", fmt.Errorf("publish takes a value of at most %d bytes as JSON. Publish a short summary and keep the bulk in a file", valueLimit)
	}

	if err != nil {
		return "", fmt.Errorf("publish takes a value that is JSON-safe: %w. Use a dict, list, string, number, bool or None", err)
	}

	return string(data), nil
}

// setEvents sets the event of the key and the event that lists the keys, in the step that the call of publish records. The context is the one the library gives to the body of a step.
func (e *Engine) setEvents(ctx context.Context, keys []string, key, text string) (published, error) {
	step, ok := ctx.(dbos.Context)
	if !ok {
		return published{}, fmt.Errorf("publish the key %s: the step has no run to hold the key", key)
	}

	if !slices.Contains(keys, key) {
		keys = append(slices.Clone(keys), key)
	}

	err := dbos.SetEvent(step, userEventPrefix+key, eventValue{Value: json.RawMessage(text)})
	if err == nil {
		err = dbos.SetEvent(step, keysEvent, keys)
	}

	if err != nil {
		return published{}, fmt.Errorf("publish the key %s: %w", key, err)
	}

	return published{Keys: keys}, nil
}

// progress reads the value that each child published under a key, in one recorded step. It does not wait: a child that published nothing reads as not set.
func (s *script) progress(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var (
		runs       starlark.Value
		key, label string
	)

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{runsField: &runs, keyField: &key, nameKeyword: &label}); err != nil {
		return nil, err
	}

	refs, err := s.childrenOf(runs, key)
	if err != nil {
		return nil, err
	}

	read, err := countedStep(s, progressKind, label, func(context.Context) ([]childProgress, error) { return s.e.readProgress(refs, key) })
	if err != nil {
		return nil, err
	}

	return progressValue(read)
}

// childrenOf checks the key and the runs of a progress call, and returns the runs. Each run must be a child that this run spawned.
func (s *script) childrenOf(runs starlark.Value, key string) ([]spawnedRun, error) {
	if err := workflow.CheckEventKey(key); err != nil {
		return nil, err
	}

	refs, err := runRefs(runs)
	if err != nil {
		return nil, err
	}

	return refs, s.checkChildren(refs)
}

// readProgress reads the event of the key from each run. The reads go through the context of the engine and record nothing, as the reads of the status do.
func (e *Engine) readProgress(refs []spawnedRun, key string) ([]childProgress, error) {
	read := make([]childProgress, len(refs))

	for i, ref := range refs {
		value, set, err := e.eventOf(ref.Run, key)
		if err != nil {
			return nil, err
		}

		read[i] = childProgress{Item: ref.Item, Run: ref.Run, Set: set, Value: value}
	}

	return read, nil
}

// eventOf reads the value of a key that a run published. The library answers a read of an event that does not exist with a timeout, so the read waits for no time.
func (e *Engine) eventOf(run, key string) (json.RawMessage, bool, error) {
	got, err := dbos.GetEvent[eventValue](dbos.WithoutCancel(e.ctx), run, userEventPrefix+key, 0)
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, false, nil
	}

	if err != nil {
		return nil, false, fmt.Errorf("read the key %s of run %s: %w", key, run, err)
	}

	return got.Value, true, nil
}

// progressValue makes the list that progress returns: one struct with the fields item, run, set and value for each run.
func progressValue(read []childProgress) (*starlark.List, error) {
	out := make([]starlark.Value, len(read))

	for i, one := range read {
		value, err := valueOf(one.Value)
		if err != nil {
			return nil, fmt.Errorf("read the value of run %s: %w", one.Run, err)
		}

		out[i] = starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{
			itemName: starlark.String(one.Item), runField: starlark.String(one.Run), "set": starlark.Bool(one.Set), valueField: value,
		})
	}

	return starlark.NewList(out), nil
}

// PublishedValue is one key that a run published, with its value as JSON text.
type PublishedValue struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

// Published lists the keys that a run published and their last values, in the order of the first publish. A run that published nothing gives an empty list.
func (c *Client) Published(id string) ([]PublishedValue, error) {
	keys, err := dbos.GetEvent[[]string](c.flow, id, keysEvent, questionReadLimit)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return nil, fmt.Errorf("read the keys that %s published: %w", id, err)
	}

	values := make([]PublishedValue, len(keys))

	for i, key := range keys {
		got, err := dbos.GetEvent[eventValue](c.flow, id, userEventPrefix+key, questionReadLimit)
		if err != nil {
			return nil, fmt.Errorf("read the key %s of %s: %w", key, id, err)
		}

		values[i] = PublishedValue{Key: key, Value: got.Value}
	}

	return values, nil
}
