package flow

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/tobocop2/beehave/internal/workflow"
)

const (
	// firstOfKind is the step kind of the first_of builtin.
	firstOfKind = "first_of"
	// childTopic is the topic of the message that a child run sends to its parent when it ends. It is reserved, so no workflow file sends it.
	childTopic = "beehave.child"
	// lookSuffix ends the name of the step that records one turn of a first_of call, after the name of its step, and the number of the turn follows.
	lookSuffix = ".look"
	// firstOfWindow is the longest that one turn of first_of waits before it reads the runs again, so a lost end notice costs this much and never a hang.
	firstOfWindow = time.Minute
	// firstOfSettle is the wait of a turn that follows an end notice and read no ended run: a run sends the notice before the store marks its row as ended.
	// firstOfSettleTurns bounds those short turns, so a notice of a run that this call does not watch costs a few reads and no more.
	firstOfSettle      = 250 * time.Millisecond
	firstOfSettleTurns = 40
)

// firstOfCall is the arguments of a first_of call. wait is zero for a call with no timeout.
type firstOfCall struct {
	refs   []spawnedRun
	topics []string
	wait   time.Duration
	label  string
}

// endedRun is a run that ended and that no first_of call of this run returned before, with how it ended. Run is the run as spawn returned it.
type endedRun struct {
	Run     spawnedRun   `json:"run"`
	Outcome childOutcome `json:"outcome"`
}

// firstLook is one turn of a first_of call, as the step <step>.look<n> records it: the places of the runs that failed with retries left, else every run that ended
// in this read and that no call took, in the order of the list, else when the wait of the turn ends. Last is true when that time is the deadline of the call,
// so a wait that runs out then means the timer.
type firstLook struct {
	Retry []int      `json:"retry,omitempty"`
	Ended []endedRun `json:"ended,omitempty"`
	Until int64      `json:"until,omitempty"`
	Last  bool       `json:"last,omitempty"`
}

// firstChoice is what a first_of call returned, as its step records it. Ended holds every run that ended in the read that found the winner, the winner first.
// Message is the JSON value of a message.
type firstChoice struct {
	Kind    workflow.FirstKind `json:"kind"`
	Ended   []endedRun         `json:"ended,omitempty"`
	Message json.RawMessage    `json:"message,omitempty"`
}

// firstOf waits for the first of a run that ended, a message on one of the topics and the timeout, and returns which one came first.
// A run that an earlier call returned does not win again.
func (s *script) firstOf(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	call, err := unpackFirstOf(b, args, kwargs)
	if err != nil {
		return nil, err
	}

	spawned, err := s.firstOfRuns(call)
	if err != nil {
		return nil, err
	}

	chosen, err := s.firstChosen(call, spawned)
	if err != nil {
		return nil, err
	}

	return firstOfValue(chosen)
}

// firstChosen waits for what a call returns, records it as the counted step of the call, and marks a run that ended as taken.
func (s *script) firstChosen(call firstOfCall, spawned []spawnedRun) (firstChoice, error) {
	step := s.nextName(firstOfKind, call.label)

	deadline, err := s.firstDeadline(call.wait, step)

	var chosen firstChoice
	if err == nil {
		chosen, err = s.awaitFirst(call, spawned, step, deadline)
	}

	if err == nil {
		chosen, err = countedStep(s, firstOfKind, call.label, func(context.Context) (firstChoice, error) { return chosen, nil })
	}

	for _, one := range chosen.Ended {
		s.taken = append(s.taken, one.Run.Run)
	}

	return chosen, err
}

// firstOfRuns checks that the runs of a call are children of this run, and that the call has something left to wait for.
func (s *script) firstOfRuns(call firstOfCall) ([]spawnedRun, error) {
	spawned, err := s.checkChildren(call.refs)
	if err == nil && len(call.topics) == 0 && call.wait == 0 && len(s.open(spawned)) == 0 {
		err = errors.New("every run of first_of was returned by an earlier call, and the call has no topics and no timeout. Pass the runs that are still open, or add a timeout")
	}

	return spawned, err
}

// open lists the runs that no first_of call of this run returned.
func (s *script) open(spawned []spawnedRun) []spawnedRun {
	return slices.DeleteFunc(slices.Clone(spawned), func(one spawnedRun) bool { return slices.Contains(s.taken, one.Run) })
}

// unpackFirstOf reads and checks the arguments of a first_of call.
func unpackFirstOf(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (firstOfCall, error) {
	var (
		call         firstOfCall
		runs, topics starlark.Value
		timeout      string
	)

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{runsField: &runs, topicsKeyword: &topics, timeoutKeyword: &timeout, nameKeyword: &call.label}); err != nil {
		return call, err
	}

	if err := call.read(runs, topics, timeout); err != nil {
		return call, err
	}

	return call, workflow.FirstOfSources(len(call.refs) > 0, len(call.topics) > 0, timeout != "")
}

// read checks the runs, the topics and the timeout of a first_of call and keeps them in the call. A call with no timeout has none: wait stays zero.
func (c *firstOfCall) read(runs, topics starlark.Value, timeout string) error {
	var err error

	if c.refs, err = runRefs(runs); err != nil {
		return err
	}

	if c.topics, err = workflow.Topics(topics); err != nil {
		return err
	}

	if timeout != "" {
		c.wait, err = workflow.Deadline(timeoutKeyword, timeout)
	}

	return err
}

// awaitFirst runs the turns of a first_of call. A turn reads the rows of the runs in a recorded step, then waits on the inbox until a message of a topic,
// an end notice or the end of the turn. A turn ends at the deadline of the call or after the window, whichever is first, so a missed end notice costs one window.
// The deadline is the recorded step <step>.deadline, and each turn is the recorded step <step>.look<n>: a replay makes the same calls whatever the clock reads.
func (s *script) awaitFirst(call firstOfCall, spawned []spawnedRun, step string, deadline time.Time) (firstChoice, error) {
	refs, restarts := s.lastAttempts(spawned)
	settling := 0

	for turn := 1; ; turn++ {
		seen, again, err := s.look(lookTurn{step: step, n: turn, spawned: spawned, deadline: deadline, settling: settling > 0}, &refs, restarts)
		if err != nil {
			return firstChoice{}, err
		}

		if again {
			continue
		}

		chosen, noticed, found, err := s.afterLook(call.topics, seen, restarts)
		if found || err != nil {
			return chosen, err
		}

		settling = nextSettling(settling, noticed)
	}
}

// nextSettling is the number of turns that still wait a short while only: a notice starts the count, and each turn after it uses one.
func nextSettling(settling int, noticed bool) int {
	if noticed {
		return firstOfSettleTurns
	}

	return max(settling-1, 0)
}

// afterLook returns what a turn found: the run that ended, else a message of a topic from the buffer or the inbox, else the timer at the deadline.
// found is false when the turn is over and the call goes on.
func (s *script) afterLook(topics []string, seen firstLook, restarts map[string]int) (chosen firstChoice, noticed, found bool, err error) {
	if len(seen.Ended) > 0 {
		for i := range seen.Ended {
			seen.Ended[i].Outcome.Attempts = 1 + restarts[seen.Ended[i].Outcome.Item]
		}

		return firstChoice{Kind: workflow.FirstRun, Ended: seen.Ended}, false, true, nil
	}

	if got, ok := s.buffered(topics); ok {
		return got, false, true, nil
	}

	return s.turnOf(topics, seen)
}

// lookTurn names one turn of a first_of call.
type lookTurn struct {
	step     string
	n        int
	spawned  []spawnedRun
	deadline time.Time
	settling bool
}

// look runs the recorded read of one turn. When the read finds a run to start again, it starts it, keeps the new attempt in refs and returns again.
func (s *script) look(turn lookTurn, refs *[]spawnedRun, restarts map[string]int) (seen firstLook, again bool, err error) {
	seen, err = runStep(s.e, s.ctx, turn.step+lookSuffix+strconv.Itoa(turn.n), func(context.Context) (firstLook, error) {
		return s.e.lookFirst(s.in, *refs, turn.spawned, s.taken, turn.deadline, turn.settling)
	})
	if err == nil && len(seen.Retry) > 0 {
		*refs, err = s.restart(fmt.Sprintf("%s.retry%d", turn.step, turn.n), turn.spawned, *refs, seen.Retry, restarts)
		again = err == nil
	}

	return seen, again, err
}

// firstDeadline records the deadline of a call that sets a timeout, as the step <step>.deadline, so a replay keeps the first one. A call with no timeout has none.
func (s *script) firstDeadline(wait time.Duration, step string) (time.Time, error) {
	if wait <= 0 {
		return time.Time{}, nil
	}

	millis, err := runStep(s.e, s.ctx, step+deadlineSuffix, func(context.Context) (int64, error) {
		return s.e.deps.Clock.reading().Add(wait).UnixMilli(), nil
	})

	return time.UnixMilli(millis), err
}

// lookFirst is one turn of first_of: it reads the runs. A run that failed with retries left comes first, and the call starts it again. Else it returns the first run
// that ended and that no call took. With none, it returns when the wait of the turn ends.
func (e *Engine) lookFirst(in ItemInput, refs, spawned []spawnedRun, taken []string, deadline time.Time, settling bool) (firstLook, error) {
	looks, err := e.readRuns(refs)
	if err != nil {
		return firstLook{}, err
	}

	if retry := retrying(looks); len(retry) > 0 {
		return firstLook{Retry: retry}, nil
	}

	if places := endedPlaces(looks, spawned, taken); len(places) > 0 {
		return e.endedLook(in.Item, looks, spawned, places)
	}

	return e.turnEnd(deadline, settling), nil
}

// endedLook prices the runs at places and returns them as the result of the turn.
func (e *Engine) endedLook(parent string, looks []look, spawned []spawnedRun, places []int) (firstLook, error) {
	used := make([]look, len(places))
	for i, at := range places {
		used[i] = looks[at]
	}

	done, err := e.priced(parent, used, raiseResult{trigger: -1})
	if err != nil {
		return firstLook{}, err
	}

	ended := make([]endedRun, len(places))
	for i, at := range places {
		ended[i] = endedRun{Run: spawned[at], Outcome: done.Outcomes[i]}
	}

	return firstLook{Ended: ended}, nil
}

// endedPlaces lists the places of the runs that ended in this read and that no call took, in the order of the list. looks and spawned have the same places.
func endedPlaces(looks []look, spawned []spawnedRun, taken []string) []int {
	var places []int

	var listed []string

	for i, one := range spawned {
		if !looks[i].open && !slices.Contains(taken, one.Run) && !slices.Contains(listed, one.Run) {
			places = append(places, i)
			listed = append(listed, one.Run)
		}
	}

	return places
}

// turnEnd is the end of a turn that read no run that ended: the window, or the deadline of the call when it is first.
// A run sends its end notice before the store marks its row as ended, so a turn that follows a notice waits a short while only.
func (e *Engine) turnEnd(deadline time.Time, settling bool) firstLook {
	wait := e.deps.Clock.window()
	if settling {
		wait = min(wait, firstOfSettle)
	}

	until, last := e.deps.Clock.reading().Add(wait), false
	if !deadline.IsZero() && !deadline.After(until) {
		until, last = deadline, true
	}

	return firstLook{Until: until.UnixMilli(), Last: last}
}

// turnOf waits out one turn that read no run that ended. It returns the message of a topic from the buffer or the inbox, or the timer when the turn ended at the
// deadline. found is false when the turn is over and the call goes on. noticed is true when an end notice ended the turn.
func (s *script) turnOf(topics []string, seen firstLook) (chosen firstChoice, noticed, found bool, err error) {
	for {
		wait := max(time.UnixMilli(seen.Until).Sub(s.e.deps.Clock.reading()), shortestReceive)

		got, err := dbos.Recv[envelope](s.ctx, inboxTopic, wait)

		switch {
		case errors.Is(err, context.DeadlineExceeded):
			return firstChoice{Kind: workflow.FirstTimer}, false, seen.Last, nil
		case err != nil:
			return firstChoice{}, false, false, fmt.Errorf("item %s receives on its inbox: %w", s.in.Item, err)
		case got.Topic == childTopic:
			return firstChoice{}, true, false, nil
		case slices.Contains(topics, got.Topic):
			return firstChoice{Kind: workflow.FirstMessage, Message: got.Value}, false, true, nil
		}

		s.stash(got)
	}
}

// buffered takes the first message of one of the topics out of the buffer of the inbox.
func (s *script) buffered(topics []string) (firstChoice, bool) {
	at := slices.IndexFunc(s.inbox, func(one envelope) bool { return slices.Contains(topics, one.Topic) })
	if at < 0 {
		return firstChoice{}, false
	}

	got := s.inbox[at]
	s.inbox = slices.Delete(s.inbox, at, at+1)

	return firstChoice{Kind: workflow.FirstMessage, Message: got.Value}, true
}

// firstOfValue makes the struct that first_of returns: kind, run, outcome, runs and message. When a run ended, run and outcome are the first of the runs
// that ended, and runs lists all of them, each as a struct of item, run and outcome. Else run and outcome are None and runs is empty. message is None unless a message came.
func firstOfValue(chosen firstChoice) (*starlarkstruct.Struct, error) {
	run, outcome, ended, err := endedValues(chosen.Ended)
	if err != nil {
		return nil, err
	}

	message, err := valueOf(chosen.Message)
	if err != nil {
		return nil, fmt.Errorf("read the message of first_of: %w", err)
	}

	return starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{
		"kind": starlark.String(chosen.Kind), runField: run, "outcome": outcome, runsField: ended, "message": message,
	}), nil
}

// endedValues makes the run and outcome of the first run that ended, or None, and the list of every run that ended, each as a struct of item, run and outcome.
func endedValues(ended []endedRun) (run, outcome starlark.Value, list *starlark.List, err error) { //nolint:ireturn // starlark value
	run, outcome = starlark.None, starlark.None
	values := make([]starlark.Value, len(ended))

	for i, one := range ended {
		value, valueErr := outcomeValue(one.Outcome)
		if valueErr != nil {
			return nil, nil, nil, valueErr
		}

		values[i] = starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{
			itemName: starlark.String(one.Run.Item), runField: starlark.String(one.Run.Run), "outcome": value,
		})

		if i == 0 {
			run, outcome = runValue(one.Run), value
		}
	}

	return run, outcome, starlark.NewList(values), nil
}

// window is how long one turn of first_of waits at most.
func (c clock) window() time.Duration { return cmp.Or(c.Window, firstOfWindow) }

// endNotice tells the parent run that the run id ended, as a message on the reserved topic childTopic. The key is the same for every sender of the notice, so the
// store inserts it once. The notice only wakes the parent, which reads the row of the run, so a notice that cannot be sent costs the parent one window and no more.
func endNotice(from dbos.Client, parent, id string) error {
	key := "end:" + id

	return dbos.Send(from, parent, envelope{Topic: childTopic, Sender: id, ID: key}, inboxTopic, dbos.WithIdempotencyKey(key))
}
