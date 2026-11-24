package flow

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
)

// Fixture is the script of a dry run: the result of each agent call, by key. A key is the name of the builtin, with the label of the call after a colon (agent:review).
// A key holds a list with one result for each call of that key, in order, or one object that every call of that key takes.
// A call whose key the file does not list fails the run, and so does a result that no call used.
type Fixture struct {
	file   string
	keys   []string
	shapes map[string]fixtureShape
	agents map[string]fixtureCalls[agentResult]
	gates  map[string]fixtureCalls[gateResult]
	waits  map[string]fixtureCalls[waitResult]
	recvs  map[string]fixtureCalls[recvResult]
}

// waitResult is the scripted result of one wait call: the answer, and whether the wait timed out with none.
type waitResult struct {
	TimedOut bool   `json:"timed_out"`
	Answer   string `json:"answer"`
}

// recvResult is the scripted result of one recv call: the value of the message, as JSON, and whether the call timed out with none.
type recvResult struct {
	TimedOut bool            `json:"timed_out"`
	Value    json.RawMessage `json:"value"`
}

// gateResult is the scripted result of one gate call. A result with no passed field passes.
type gateResult struct {
	Passed *bool `json:"passed"`
}

// agentResult is the scripted result of one agent call. A declared output that Outputs does not name keeps the base value of the dry run.
type agentResult struct {
	Outputs map[string]string `json:"outputs"`
}

// fixtureCalls holds the results of one key: each is the list, one for each call, and every is the object that every call takes.
type fixtureCalls[T any] struct {
	each  []T
	every *T
}

// fixtureShape is what a key holds: the count of its list, or an object for every call. The end of the run reads it to find a result that no call used.
type fixtureShape struct {
	results int
	object  bool
}

// The builtins of the keys that script a call. A key is one of them, with the label of the call after a colon.
const (
	fixtureAgent = "agent"
	fixtureGate  = "gate"
	fixtureWait  = "wait"
	fixtureRecv  = "recv"
)

// LoadFixture reads the fixture file at the path. It refuses an unknown key, an unknown field and a value of the wrong type, and names the file in each message.
func LoadFixture(path string) (Fixture, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the path is the file that the owner names with --fixture
	if err != nil {
		return Fixture{}, fmt.Errorf("read the fixture: %w", err)
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return Fixture{}, fmt.Errorf("fixture %s: the file must hold a JSON object of calls: %w", path, err)
	}

	loaded := Fixture{file: path, keys: slices.Sorted(maps.Keys(top)), shapes: map[string]fixtureShape{}, agents: map[string]fixtureCalls[agentResult]{}, gates: map[string]fixtureCalls[gateResult]{}, waits: map[string]fixtureCalls[waitResult]{}, recvs: map[string]fixtureCalls[recvResult]{}}

	for _, key := range loaded.keys {
		if err := loaded.add(key, top[key]); err != nil {
			return Fixture{}, fmt.Errorf("fixture %s: key %q: %w", path, key, err)
		}
	}

	return loaded, nil
}

// add decodes the value of one key into the table of its builtin.
func (f *Fixture) add(key string, raw json.RawMessage) error {
	builtin, _, _ := strings.Cut(key, ":")

	switch builtin {
	case fixtureAgent:
		calls, err := decodeCalls[agentResult](raw)
		f.agents[key], f.shapes[key] = calls, calls.shape()

		return err
	case fixtureGate:
		calls, err := decodeCalls[gateResult](raw)
		f.gates[key], f.shapes[key] = calls, calls.shape()

		return err
	}

	return f.addTimed(key, builtin, raw)
}

// addTimed decodes the value of a key of a wait or a recv, and refuses any other builtin.
func (f *Fixture) addTimed(key, builtin string, raw json.RawMessage) error {
	switch builtin {
	case fixtureWait:
		calls, err := decodeCalls[waitResult](raw)
		f.waits[key], f.shapes[key] = calls, calls.shape()

		return err
	case fixtureRecv:
		calls, err := decodeCalls[recvResult](raw)
		f.recvs[key], f.shapes[key] = calls, calls.shape()

		return err
	}

	return fmt.Errorf("the builtin must be %s, %s, %s or %s", fixtureAgent, fixtureGate, fixtureWait, fixtureRecv)
}

// decodeCalls reads a list of results, or one object, and refuses a field that the result does not have.
func decodeCalls[T any](raw json.RawMessage) (fixtureCalls[T], error) {
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("[")) {
		var each []T

		err := decodeStrict(raw, &each)

		return fixtureCalls[T]{each: each}, err
	}

	var one T

	err := decodeStrict(raw, &one)

	return fixtureCalls[T]{every: &one}, err
}

func decodeStrict(raw json.RawMessage, into any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()

	return decoder.Decode(into)
}

func (c fixtureCalls[T]) shape() fixtureShape {
	return fixtureShape{results: len(c.each), object: c.every != nil}
}

// fixtureRun is the state of one dry run over a fixture: how many calls each key has had. Spawned runs call in parallel, so the count sits under a lock.
type fixtureRun struct {
	Fixture
	mu   sync.Mutex
	made map[string]int
}

// start returns the state for one run. A run with no fixture holds a nil state, and every method of the state accepts nil and then scripts nothing.
func (f Fixture) start() *fixtureRun {
	return &fixtureRun{Fixture: f, made: map[string]int{}}
}

// nextResult counts one call of the key and returns the result for it. The result is nil when the file has no entry for the key.
func nextResult[T any](r *fixtureRun, key string, table map[string]fixtureCalls[T]) (result *T, call int, err error) {
	entry, listed := table[key]
	if !listed {
		return nil, 0, nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.made[key]++
	call = r.made[key]

	switch {
	case entry.every != nil:
		return entry.every, call, nil
	case call > len(entry.each):
		return nil, call, fmt.Errorf("fixture %s: %s call %d: the file has no result for it; it lists %d", r.file, key, call, len(entry.each))
	}

	return &entry.each[call-1], call, nil
}

// unlisted is the error of a call whose key the file does not list.
func (r *fixtureRun) unlisted(key string) error {
	return fmt.Errorf("fixture %s: the call %s has no entry; the file has the keys %v", r.file, key, r.keys)
}

// agentOutputs counts one agent call and returns the outputs that the fixture sets for it. call is the name of the call, such as agent:fix#1.
func (r *fixtureRun) agentOutputs(call string, declared []string) (map[string]string, error) {
	if r == nil {
		return nil, nil
	}

	key, _, _ := strings.Cut(call, "#")

	result, number, err := nextResult(r, key, r.agents)

	switch {
	case err != nil:
		return nil, err
	case result == nil:
		return nil, r.unlisted(key)
	}

	return result.Outputs, r.declares(key, number, result.Outputs, declared)
}

// gatePassed counts one gate call and returns whether the fixture lets it pass. call is the key of the gate call, such as gate:check.
// A gate with no key, which is the gate of a merge, is not scripted and passes.
func (r *fixtureRun) gatePassed(call string) (bool, error) {
	if r == nil || call == "" {
		return true, nil
	}

	result, _, err := nextResult(r, call, r.gates)

	switch {
	case err != nil:
		return false, err
	case result == nil:
		return false, r.unlisted(call)
	}

	return result.Passed == nil || *result.Passed, nil
}

// wait counts one wait call and returns its answer. A wait with no entry in the file times out at once when it has a deadline, and is refused when it has none.
func (r *fixtureRun) wait(label string, deadline bool) (heard, error) {
	var result *waitResult

	if r != nil {
		var err error
		if result, _, err = nextResult(r, callKey(fixtureWait, label), r.waits); err != nil {
			return heard{}, err
		}
	}

	switch {
	case result != nil:
		return heard{answer: result.Answer, timedOut: result.TimedOut}, nil
	case deadline:
		return heard{timedOut: true}, nil
	}

	return heard{}, fmt.Errorf("the wait %s has no deadline and the fixture gives no answer for it: add timeout to the call, or add the key %s to the fixture", callKey(fixtureWait, label), callKey(fixtureWait, label))
}

// recv counts one recv call and returns its message. A recv with no entry in the file times out at once.
func (r *fixtureRun) recv(label string) (envelope, bool, error) {
	if r == nil {
		return envelope{}, true, nil
	}

	result, _, err := nextResult(r, callKey(fixtureRecv, label), r.recvs)

	switch {
	case err != nil:
		return envelope{}, false, err
	case result == nil:
		return envelope{}, true, nil
	}

	return envelope{Value: result.Value}, result.TimedOut, nil
}

// callKey is the key of a call of the builtin: its name, then a colon and the label when the call has one.
func callKey(builtin, label string) string {
	if label == "" {
		return builtin
	}

	return builtin + ":" + label
}

// declares refuses an output that the result sets and the call does not declare.
func (r *fixtureRun) declares(key string, number int, set map[string]string, declared []string) error {
	for _, name := range slices.Sorted(maps.Keys(set)) {
		if !slices.Contains(declared, name) {
			return fmt.Errorf("fixture %s: %s call %d sets output %q; the declared outputs: %v", r.file, key, number, name, declared)
		}
	}

	return nil
}

// leftover returns an error for each key that holds a result which no call used. It reads the counts at the end of a run that went clean.
func (r *fixtureRun) leftover() error {
	if r == nil {
		return nil
	}

	var unused []error

	for _, key := range r.keys {
		shape, made := r.shapes[key], r.made[key]

		switch {
		case shape.object && made == 0:
			unused = append(unused, fmt.Errorf("fixture %s: %s: the file has a result for every call, and the run never made a call", r.file, key))
		case made < shape.results:
			unused = append(unused, fmt.Errorf("fixture %s: %s: results in the file: %d; calls in the run: %d; result %d was never made", r.file, key, shape.results, made, made+1))
		}
	}

	return errors.Join(unused...)
}
