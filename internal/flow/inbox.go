package flow

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"go.starlark.net/starlark"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/workflow"
)

const (
	// inboxTopic is the one DBOS topic that every message to a run arrives on. The topic of the workflow is a field of the envelope.
	inboxTopic = "beehave.inbox"
	// recvKind and sendKind are the step kinds of the inbox builtins.
	recvKind = "recv"
	sendKind = "send"
	// topicKeyword is the keyword of recv and send that names the topic of a message.
	topicKeyword = "topic"
	// parentTarget is the value of the to keyword of send that names the run that spawned this run.
	parentTarget = "parent"
	// deadlineSuffix ends the name of the step that records the deadline of one recv call.
	deadlineSuffix = ".deadline"
	// shortestReceive is the least time that one receive of a recv waits, so a recv whose deadline passed still takes a message that is there.
	shortestReceive = time.Millisecond
)

// inboxDefs lists the builtins of the inbox.
func inboxDefs() []builtinDef {
	return []builtinDef{
		{name: recvKind, call: (*script).recv},
		{name: sendKind, call: (*script).send},
	}
}

// envelope is one message on the inbox of a run: its topic, the run or person that sent it, its id, and its value as JSON.
type envelope struct {
	Topic  string          `json:"topic"`
	Sender string          `json:"sender"`
	ID     string          `json:"id"`
	Value  json.RawMessage `json:"value,omitempty"`
}

// recv waits for the next message of a topic on the inbox of the run, up to the timeout. It returns the value, the sender, the id and timed_out.
func (s *script) recv(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var topic, timeout, label string

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{topicKeyword: &topic, timeoutKeyword: &timeout, nameKeyword: &label}); err != nil {
		return nil, err
	}

	wait, err := recvLimits(topic, timeout)
	if err != nil {
		return nil, err
	}

	got, timedOut, err := s.receive(topic, label, wait)
	if err != nil {
		return nil, err
	}

	return s.durable(recvKind, label, func(context.Context, string) (stepResult, error) {
		return stepResult{Outputs: recvOutputs(got, timedOut)}, nil
	})
}

// recvLimits checks the topic and the timeout of a recv call.
func recvLimits(topic, timeout string) (time.Duration, error) {
	if err := workflow.MessageName(topicKeyword, topic); err != nil {
		return 0, fmt.Errorf("recv: %w", err)
	}

	wait, err := workflow.Deadline(timeoutKeyword, timeout)
	if err != nil {
		return 0, fmt.Errorf("recv: %w", err)
	}

	return wait, nil
}

// recvOutputs are the fields of the result of recv. A recv that timed out has no value, sender or id.
func recvOutputs(got envelope, timedOut bool) map[string]any {
	var value any
	if len(got.Value) > 0 {
		value = got.Value
	}

	return map[string]any{valueField: value, "sender": got.Sender, "id": got.ID, "timed_out": timedOut}
}

// receive returns the oldest message of the topic: from the messages that the run received for other topics, else from the inbox.
// A message of another topic waits in the buffer of the run for a later recv. The deadline is a recorded step, so a resumed run waits only the time left.
// Every turn calls Recv, even when the deadline has passed, so a replay makes the same calls whatever the clock reads.
func (s *script) receive(topic, label string, wait time.Duration) (envelope, bool, error) {
	if at := slices.IndexFunc(s.inbox, func(one envelope) bool { return one.Topic == topic }); at >= 0 {
		got := s.inbox[at]
		s.inbox = slices.Delete(s.inbox, at, at+1)

		return got, false, nil
	}

	deadline, err := runStep(s.e, s.ctx, s.nextName(recvKind, label)+deadlineSuffix, func(context.Context) (time.Time, error) {
		return s.e.deps.Clock.reading().Add(wait), nil
	})

	got, timedOut := envelope{}, false
	if err == nil {
		got, timedOut, err = s.await(topic, deadline)
	}

	return got, timedOut, err
}

// await receives from the inbox until a message of the topic arrives or the deadline passes. A message of another topic goes to the buffer.
func (s *script) await(topic string, deadline time.Time) (envelope, bool, error) {
	for {
		got, err := dbos.Recv[envelope](s.ctx, inboxTopic, max(deadline.Sub(s.e.deps.Clock.reading()), shortestReceive))

		switch {
		case errors.Is(err, context.DeadlineExceeded):
			return envelope{}, true, nil
		case err != nil:
			return envelope{}, false, fmt.Errorf("item %s receives on topic %s: %w", s.in.Item, topic, err)
		case got.Topic == topic:
			return got, false, nil
		}

		s.inbox = append(s.inbox, got)
	}
}

// send puts a message on the inbox of the parent run or of a run that this run spawned, and returns its id.
// The id is the run and the step name, and it is the idempotency key of the DBOS send, so a resumed run inserts the message once.
func (s *script) send(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var to, value starlark.Value

	var topic, label string
	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{"to": &to, topicKeyword: &topic, valueField: &value, nameKeyword: &label}); err != nil {
		return nil, err
	}

	env, dest, err := s.outgoing(to, topic, value, label)
	if err != nil {
		return nil, fmt.Errorf("send: %w", err)
	}

	sent, err := s.durable(sendKind, label, func(context.Context, string) (stepResult, error) { return s.e.sendStep(env, dest) })
	if err == nil {
		err = s.deliver(dest.Run, env)
	}

	return sent, err
}

// sendStep is the recorded step of a send: it refuses a child with no open attempt, and records the id of the message.
func (e *Engine) sendStep(env envelope, dest spawnedRun) (stepResult, error) {
	if err := e.checkOpen(dest); err != nil {
		return stepResult{}, fmt.Errorf("send: %w", err)
	}

	return stepResult{Outputs: sendOutputs(env.ID)}, nil
}

// checkOpen refuses a send to a child whose latest attempt ended, since no attempt reads the message. The parent is not checked: it waits for this run.
// It runs inside the send step, so a replay gives the answer that the first run recorded.
func (e *Engine) checkOpen(dest spawnedRun) error {
	if dest.Item == "" {
		return nil
	}

	byID, err := e.rowsOf([]spawnedRun{dest})
	if err != nil {
		return err
	}

	if row, found := byID[dest.Run]; found && (run{Status: row}).terminal() {
		return fmt.Errorf("item %s has no open run: its last attempt %s ended. Send to a child before it ends", dest.Item, dest.Run)
	}

	return nil
}

// sendOutputs are the fields of the result of send.
func sendOutputs(id string) map[string]any {
	return map[string]any{"id": id}
}

// deliver sends the envelope to the inbox of the run dest. A run that stopped after the record of the send step sends again on resume, with the same key, and the store inserts the message once.
func (s *script) deliver(dest string, env envelope) error {
	if err := dbos.Send(s.ctx, dest, env, inboxTopic, dbos.WithIdempotencyKey(env.ID)); err != nil {
		return fmt.Errorf("item %s cannot send on topic %s to run %s: %w", s.in.Item, env.Topic, dest, err)
	}

	return nil
}

// outgoing checks the arguments of a send call and returns the envelope and the run it goes to. The value passes the redactor.
// The id is the run and the name of the send step that comes next.
func (s *script) outgoing(to starlark.Value, topic string, value starlark.Value, label string) (envelope, spawnedRun, error) {
	if err := workflow.MessageName(topicKeyword, topic); err != nil {
		return envelope{}, spawnedRun{}, err
	}

	raw, err := workflow.MessageValue(value)
	if err != nil {
		return envelope{}, spawnedRun{}, err
	}

	dest, err := s.destination(to)
	if err != nil {
		return envelope{}, spawnedRun{}, err
	}

	sender := workflowID(s.ctx)

	return envelope{Topic: topic, Sender: sender, ID: sender + ":" + s.nextName(sendKind, label), Value: json.RawMessage(s.e.redact(string(raw)))}, dest, nil
}

// destination is the run that the to keyword of send names: the parent of this run, with no item, or the latest attempt of a run that a spawn call of this run returned.
func (s *script) destination(to starlark.Value) (spawnedRun, error) {
	if text, isText := starlark.AsString(to); isText {
		parent, err := s.parentRun(text)

		return spawnedRun{Run: parent}, err
	}

	return s.childDestination(to)
}

// childDestination is the latest attempt of the run that spawn returned, which the to keyword of send names.
func (s *script) childDestination(to starlark.Value) (spawnedRun, error) {
	var ref spawnedRun

	if holder, isStruct := to.(starlark.HasAttrs); isStruct {
		ref = spawnedRun{Item: textAttr(holder, itemName), Run: textAttr(holder, "run")}
	}

	if ref.Run == "" {
		return spawnedRun{}, fmt.Errorf("to is %s. Give \"parent\", or a run that spawn returned", to)
	}

	live, err := s.liveRuns([]spawnedRun{ref})
	if err != nil {
		return spawnedRun{}, err
	}

	return live[0], nil
}

// parentRun is the run id of the parent when the to keyword of send is "parent".
func (s *script) parentRun(text string) (string, error) {
	if text != parentTarget {
		return "", fmt.Errorf("to is %q. Give \"parent\", or a run that spawn returned", text)
	}

	if s.in.Parent == "" {
		return "", fmt.Errorf("item %s has no parent run, because a user started it. Send to \"parent\" only from a run that spawn started", s.in.Item)
	}

	return s.in.Parent, nil
}

// Outgoing is a message that beehave send puts on the inbox of the open run of an item. An empty Key gets a fresh one.
type Outgoing struct {
	Topic  string
	Key    string
	Sender string
	Value  json.RawMessage
}

// Send puts a message on the inbox of the open run of an item and returns its id. The id is the idempotency key, so a second send with the same key inserts nothing.
func (c *Client) Send(item string, msg Outgoing) (string, error) {
	if err := checkOutgoing(msg); err != nil {
		return "", err
	}

	last, err := c.latest(item)
	if errors.Is(err, errNoRun) || err == nil && last.terminal() {
		return "", fmt.Errorf("item %s has no open run. Run beehave status to see the runs, or beehave run %s to start one", item, item)
	}

	if err != nil {
		return "", err
	}

	id := cmp.Or(msg.Key, strings.ToLower(rand.Text()))
	env := envelope{Topic: msg.Topic, Sender: msg.Sender, ID: id, Value: json.RawMessage(agent.Redactor()(string(msg.Value)))}

	return id, dbos.Send(c.flow, last.Status.ID, env, inboxTopic, dbos.WithIdempotencyKey(id))
}

// checkOutgoing applies the rules of a message to the topic, the key and the value that beehave send got.
func checkOutgoing(msg Outgoing) error {
	if err := workflow.MessageName(topicKeyword, msg.Topic); err != nil {
		return err
	}

	if msg.Key != "" {
		if err := workflow.MessageName("key", msg.Key); err != nil {
			return err
		}
	}

	_, err := workflow.MessageJSON(msg.Value)

	return err
}
