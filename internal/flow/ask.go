package flow

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

const (
	// askKey is the event in which an open wait shows who it asks. A wait that names no one and sets no choices sets nothing.
	askKey = "ask"
	// voiceTopic carries the answers to a wait that names people or choices. A plain wait receives on ownerTopic.
	voiceTopic = "voice"
	// deadlineStep records the moment a wait with people ends, so each receive of the wait reads the time that is left.
	deadlineStep = "wait:deadline"
	// waitIDStep records the id of a wait with people, so a replay and the voices it counts name the same wait.
	waitIDStep = "wait:id"
)

// errRefusedAnswer is returned when an answer does not fit the open wait.
var errRefusedAnswer = errors.New("the wait refuses this answer")

// Asked is what an open wait asks of people. It is shown by status and read by obee answer.
// From names the people who answer, and Quorum is how many of them must. Choices is the closed set of texts an answer may be.
// Heard lists the people who answered so far, in the order of their first answer.
type Asked struct {
	Wait     string   `json:"wait,omitempty"`
	Question string   `json:"question,omitempty"`
	From     []string `json:"from,omitempty"`
	Quorum   int      `json:"quorum,omitempty"`
	Choices  []string `json:"choices,omitempty"`
	Heard    []string `json:"heard,omitempty"`
}

// voice is one answer to a wait that names people or choices. From is empty when the wait names no people.
type voice struct {
	Wait string `json:"wait,omitempty"`
	From string `json:"from"`
	Text string `json:"text"`
}

// waitPlan is the reading of the arguments of one wait.
type waitPlan struct {
	question string
	id       string
	label    string
	deadline time.Duration
	asked    Asked
}

// addressed is true when the wait names people or sets choices. Such a wait receives voices; any other wait receives plain text.
func (p waitPlan) addressed() bool { return len(p.asked.From) > 0 || len(p.asked.Choices) > 0 }

// accepts reports whether a voice fits the wait: a named person, and a text in the choices when the wait sets them.
func (a Asked) accepts(v voice) bool {
	return a.refuses(v.From, v.Text) == nil
}

// refuses returns the reason that an answer of a person does not fit the wait, or nil.
func (a Asked) refuses(from, text string) error {
	switch {
	case len(a.From) == 0 && from != "":
		return fmt.Errorf("%w: this wait names no people, so leave out --as", errRefusedAnswer)
	case len(a.From) > 0 && from == "":
		return fmt.Errorf("%w: name yourself with --as: %s", errRefusedAnswer, strings.Join(a.From, ", "))
	case len(a.From) > 0 && !slices.Contains(a.From, from):
		return fmt.Errorf("%w: %q is not one of the people this wait asks: %s", errRefusedAnswer, from, strings.Join(a.From, ", "))
	case len(a.Choices) > 0 && !slices.Contains(a.Choices, text):
		return fmt.Errorf("%w: %q is not one of the choices: %s", errRefusedAnswer, text, strings.Join(a.Choices, ", "))
	}

	return nil
}

// tally holds the counted answers of a wait with people. A second answer of one person replaces the first and counts once.
type tally struct {
	answers map[string]string
	last    string
}

// take counts one voice. A wait that names no people counts the first voice as the whole quorum.
func (t *tally) take(a *Asked, v voice) {
	t.last = v.Text

	if v.From == "" {
		return
	}

	if _, again := t.answers[v.From]; !again {
		a.Heard = append(a.Heard, v.From)
	}

	t.answers[v.From] = v.Text
}

// enough is true once the answers reach the quorum.
func (t *tally) enough(a Asked) bool {
	if len(a.From) == 0 {
		return t.last != ""
	}

	return len(t.answers) >= a.Quorum
}

// waitVoices asks the people a wait names and counts their answers until the quorum is reached or the deadline passes.
// Each answer is one recorded receive, and each receive that follows reads the deadline from one recorded step, so a replay counts the same answers.
func (e *Engine) waitVoices(ctx dbos.Context, item string, plan waitPlan) (heard, error) {
	asked, count := plan.asked, &tally{answers: map[string]string{}}
	asked.Question, asked.Wait = plan.question, plan.id

	// The ask event is the only event of the wait. It holds the question, the people and the id of the wait, so obee answer decides from one read,
	// and the wait opens and ends with one write each. The question event stays unset: a plain wait owns it.
	if err := show(ctx, item, askKey, asked, "who it asks"); err != nil {
		return heard{}, err
	}

	timedOut, err := e.within(ctx, item, plan.deadline, &asked, count)
	if err == nil {
		err = dbos.SetEvent(ctx, askKey, Asked{})
	}

	if err != nil {
		return heard{}, fmt.Errorf("item %s waits for people: %w. Answer with obee answer %s <text>", item, err, item)
	}

	return count.outcome(timedOut), nil
}

// within records the end of the deadline and counts voices until the quorum or that end.
func (e *Engine) within(ctx dbos.Context, item string, deadline time.Duration, asked *Asked, count *tally) (bool, error) {
	end, err := e.endOf(ctx, deadline)
	if err != nil {
		return false, err
	}

	return e.collect(ctx, item, end, asked, count)
}

// heard is what a wait got: the answer that completed it, the answers by name, and whether the deadline passed first.
type heard struct {
	answer   string
	answers  map[string]string
	timedOut bool
}

// outcome is what the tally holds. A wait that timed out has no answer, and keeps the answers that came.
func (t *tally) outcome(timedOut bool) heard {
	if timedOut {
		return heard{answers: t.answers, timedOut: true}
	}

	return heard{answer: t.last, answers: t.answers}
}

// listen waits for the owner. A wait that names people or choices counts their answers; any other wait takes the first plain answer.
func (e *Engine) listen(ctx dbos.Context, item string, plan waitPlan) (heard, error) {
	if plan.addressed() {
		id, err := runStep(e, ctx, waitIDStep, func(context.Context) (string, error) {
			return strconv.FormatInt(time.Now().UnixNano(), 36), nil
		})
		if err != nil {
			return heard{}, err
		}

		plan.id = id

		return e.waitVoices(ctx, item, plan)
	}

	answer, timedOut, err := e.waitOwnerUntil(ctx, item, plan.question, plan.deadline)

	return heard{answer: answer, timedOut: timedOut}, err
}

// collect counts voices until the quorum is reached or the deadline passes, and reports whether the deadline passed.
// A voice that the wait refuses is dropped. Each counted voice updates who the wait shows as heard.
func (e *Engine) collect(ctx dbos.Context, item string, end time.Time, asked *Asked, count *tally) (bool, error) {
	var shown error

	for shown == nil && !count.enough(*asked) {
		v, got, late, err := e.nextVoice(ctx, end)

		switch {
		case err != nil:
			return false, err
		case late:
			return true, nil
		case got && v.Wait == asked.Wait && asked.accepts(v):
			count.take(asked, v)
			shown = show(ctx, item, askKey, *asked, "who it asks")
		}
	}

	return false, shown
}

// nextVoice receives one voice. late is true when the deadline passed with no voice. A window of a wait with no deadline that ends with no voice
// gives got false, and the caller receives again.
func (e *Engine) nextVoice(ctx dbos.Context, end time.Time) (v voice, got, late bool, err error) {
	v, err = e.receiveVoice(ctx, end)
	if errors.Is(err, context.DeadlineExceeded) {
		return voice{}, false, !end.IsZero(), nil
	}

	return v, err == nil, false, err
}

// endOf records the moment that a deadline from now ends. It is the zero time for a wait with no deadline.
func (e *Engine) endOf(ctx dbos.Context, deadline time.Duration) (time.Time, error) {
	if deadline <= 0 {
		return time.Time{}, nil
	}

	millis, err := runStep(e, ctx, deadlineStep, func(context.Context) (int64, error) {
		return time.Now().Add(deadline).UnixMilli(), nil
	})
	if err != nil {
		return time.Time{}, err
	}

	return time.UnixMilli(millis), nil
}

// receiveVoice waits for one voice. With a deadline the receive ends at it, and a receive that finds the deadline gone still reads a voice that is waiting.
// With no deadline, the caller receives again when a window of the wait ends.
func (e *Engine) receiveVoice(ctx dbos.Context, end time.Time) (voice, error) {
	window := e.waitWindow()

	if !end.IsZero() {
		window = max(time.Until(end), time.Millisecond)
	}

	return dbos.Recv[voice](ctx, voiceTopic, window)
}

// waitWindow is the time of one receive of a wait with no deadline.
func (e *Engine) waitWindow() time.Duration {
	return cmp.Or(e.deps.Clock.OwnerWait, ownerWaitLimit)
}

// withZeros adds the fields of a result that a record from before a change does not hold, each as its zero value.
func withZeros(outputs, zeros map[string]any) map[string]any {
	if len(zeros) == 0 {
		return outputs
	}

	out := make(map[string]any, len(outputs)+len(zeros))

	for name, zero := range zeros {
		out[name] = zero
	}

	for name, value := range outputs {
		out[name] = value
	}

	return out
}

// waitZeros are the fields that the result of a wait gained after the first records: no one answered by name.
func waitZeros() map[string]any { return map[string]any{"answers": map[string]string{}} }

// AnswerAs sends the answer of one named person to the open run of an item. A wait that names people accepts only them, and a wait with choices only those texts.
// The name is not proved: obee has no key store, and anyone who can run obee answer on the run can give any name that the wait asks.
func (c *Client) AnswerAs(item, from, text string) error {
	if text == "" {
		return fmt.Errorf("%w: write the answer after the item", errEmptyAnswer)
	}

	last, err := c.latest(item)
	if err != nil {
		return err
	}

	info, err := c.requireQuestion(item, last)
	if err != nil {
		return err
	}

	return c.deliver(info, from, text)
}

// deliver sends an answer to the run on the topic that its open wait receives on, after the wait accepts it.
func (c *Client) deliver(info RunInfo, from, text string) error {
	id, asked := info.ID, info.Asked

	if asked == nil {
		if from != "" {
			return fmt.Errorf("%w: this wait names no people, so leave out --as", errRefusedAnswer)
		}

		return dbos.Send(c.flow, id, text, ownerTopic)
	}

	if err := asked.refuses(from, text); err != nil {
		return err
	}

	return dbos.Send(c.flow, id, voice{Wait: asked.Wait, From: from, Text: text}, voiceTopic)
}

// questionText is the question that the ask event holds. A wait that asks people sets that event and no other, and clears it with one write, so it alone shows which kind of wait is open.
func (a *Asked) questionText() string {
	if a == nil {
		return ""
	}

	return a.Question
}

// askedOf reads who an open run asks. It is nil for a run that asks no named people and sets no choices.
func (c *Client) askedOf(id string) (*Asked, error) {
	asked, err := dbos.GetEvent[Asked](c.flow, id, askKey, questionReadLimit)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return nil, fmt.Errorf("read who %s asks: %w", id, err)
	}

	if len(asked.From) == 0 && len(asked.Choices) == 0 {
		return nil, nil //nolint:nilnil // nil is the answer for a wait that asks no one by name
	}

	return &asked, nil
}
