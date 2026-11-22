package flow

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/workflow"
)

const (
	// askKey is the event in which an open wait shows who it asks. A wait that names no one and sets no choices sets nothing.
	askKey = "ask"
	// deadlineStep records the moment a wait with a timeout ends, so each receive of the wait reads the time that is left.
	deadlineStep = "wait:deadline"
	// waitIDStep records the id of a wait with people, so a replay and the voices it counts name the same wait.
	waitIDStep = "wait:id"
	// remindStepPrefix starts the name of the step of each reminder: wait:remind-1, wait:remind-2 and so on.
	remindStepPrefix = "wait:remind-"
	// dueStep records, each time a receive of a wait with reminders ends with no voice, which reminders are owed and whether the deadline passed.
	// A replay reads the record, so it takes the same steps the first run took.
	dueStep = "wait:due"
)

// errRefusedAnswer is returned when an answer does not fit the open wait.
var errRefusedAnswer = errors.New("the wait refuses this answer")

// Asked is what an open wait asks of people. It is shown by status and read by beehave answer.
// From names the people who answer, and Quorum is how many of them must. Choices is the closed set of texts an answer may be.
// Heard lists the people who answered so far, in the order of their first answer.
type Asked struct {
	Wait     string   `json:"wait,omitempty"`
	Question string   `json:"question,omitempty"`
	From     []string `json:"from,omitempty"`
	Quorum   int      `json:"quorum,omitempty"`
	Choices  []string `json:"choices,omitempty"`
	Heard    []string `json:"heard,omitempty"`
	// Reminders is how many reminders the wait declares, and Reminded how many it sent. NextRemind is the moment of the next one, and is zero once all are sent.
	Reminders  int       `json:"reminders,omitempty"`
	Reminded   int       `json:"reminded,omitempty"`
	NextRemind time.Time `json:"next_remind,omitzero"`
}

// voice is the value of one answer on the inbox. Wait is empty for an answer to a plain wait, and From is empty when the wait names no people.
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
	remind   []float64
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
func (s *script) waitVoices(plan waitPlan) (heard, error) {
	ctx, project, item := s.ctx, s.in.Project, s.in.Item
	asked, count := plan.asked, &tally{answers: map[string]string{}}
	asked.Question, asked.Wait = e.redact(plan.question), plan.id

	// The ask event is the only event of the wait. It holds the question, the people and the id of the wait, so beehave answer decides from one read,
	// and the wait opens and ends with one write each. The question event stays unset: a plain wait owns it.
	if err := show(ctx, item, askKey, asked, "who it asks"); err != nil {
		return heard{}, err
	}

	s.e.announce(ctx, project, item, plan.question)

	timedOut, err := s.within(plan.deadline, plan.remind, &asked, count)
	if err == nil {
		err = dbos.SetEvent(ctx, askKey, Asked{})
	}

	if err != nil {
		return heard{}, fmt.Errorf("item %s waits for people: %w. Answer with beehave answer %s <text>", item, err, item)
	}

	return count.outcome(timedOut), nil
}

// within records the end of the deadline and counts voices until the quorum or that end. A wait with reminders sends each one when it falls due.
func (s *script) within(deadline time.Duration, shares []float64, asked *Asked, count *tally) (bool, error) {
	end, err := s.e.endOf(s.ctx, deadline)
	if err != nil {
		return false, err
	}

	if len(shares) == 0 {
		return s.collect(end, asked, count)
	}

	plan := remindersOf(end, deadline, shares)
	asked.Reminders, asked.NextRemind = len(plan.dues), plan.next(0)

	if err := show(s.ctx, s.in.Item, askKey, *asked, "who it asks"); err != nil {
		return false, err
	}

	return s.collectReminding(plan, asked, count)
}

// schedule is the moments at which the reminders of a wait fall due, in order, and the moment the wait ends.
type schedule struct {
	dues []time.Time
	end  time.Time
}

// remindersOf places each share of the window, which starts deadline before end, on the clock.
func remindersOf(end time.Time, deadline time.Duration, shares []float64) schedule {
	start, dues := end.Add(-deadline), make([]time.Time, len(shares))

	for i, share := range shares {
		dues[i] = start.Add(time.Duration(float64(deadline) * share))
	}

	return schedule{dues: dues, end: end}
}

// next is the moment of the reminder after the sent ones, or the zero time when all are sent.
func (s schedule) next(sent int) time.Time {
	if sent >= len(s.dues) {
		return time.Time{}
	}

	return s.dues[sent]
}

// due is what one look at the clock found: how many reminders have fallen due since the start, and whether the deadline has passed.
type due struct {
	Owed  int  `json:"owed"`
	Ended bool `json:"ended"`
}

// collectReminding counts voices like collect, and each time a receive ends with no voice it sends the reminders that are owed.
// A receive lasts until the next reminder, the deadline or one wait window, whichever comes first, so the clock is read often enough.
func (s *script) collectReminding(plan schedule, asked *Asked, count *tally) (bool, error) {
	for !count.enough(*asked) {
		v, timedOut, err := s.hear(s.e.deps.Clock.reading().Add(s.e.reminderWindow(plan, asked.Reminded)))

		switch {
		case err != nil:
			return false, err
		case timedOut:
			if ended, sendErr := s.e.remindDue(s.ctx, s.in.Item, plan, asked); sendErr != nil || ended {
				return ended, sendErr
			}
		case v.Wait == asked.Wait && asked.accepts(v):
			count.take(asked, v)

			if shown := show(s.ctx, s.in.Item, askKey, *asked, "who it asks"); shown != nil {
				return false, shown
			}
		}
	}

	return false, nil
}

// reminderWindow is the time of one receive: until the next reminder, the deadline or one wait window, whichever comes first.
func (e *Engine) reminderWindow(plan schedule, sent int) time.Duration {
	now := e.deps.Clock.reading()

	window := min(plan.end.Sub(now), e.waitWindow())
	if next := plan.next(sent); !next.IsZero() {
		window = min(window, next.Sub(now))
	}

	return max(window, time.Millisecond)
}

// owedAt counts the reminders that have fallen due at a moment.
func (s schedule) owedAt(now time.Time) int {
	owed := 0

	for _, at := range s.dues {
		if !at.After(now) {
			owed++
		}
	}

	return owed
}

// remindDue reads the clock once, in a recorded step, and sends every reminder that is owed and not sent, in order. It reports whether the deadline passed.
// The reading is recorded, so a replay sends the same reminders and a restart never sends one twice.
func (e *Engine) remindDue(ctx dbos.Context, item string, plan schedule, asked *Asked) (bool, error) {
	look, err := runStep(e, ctx, dueStep, func(context.Context) (due, error) {
		now := e.deps.Clock.reading()

		return due{Owed: plan.owedAt(now), Ended: !now.Before(plan.end)}, nil
	})
	if err != nil {
		return false, err
	}

	for number := asked.Reminded + 1; number <= look.Owed; number++ {
		if err := e.remind(ctx, item, plan, asked, number); err != nil {
			return false, err
		}
	}

	return look.Ended, nil
}

// remind sends one reminder to the people of the wait. The notice is the ask event that beehave status and beehave answer read: it moves the count of sent
// reminders and the moment of the next one. The log line is written inside the recorded step, so a replay does not write it again.
func (e *Engine) remind(ctx dbos.Context, item string, plan schedule, asked *Asked, number int) error {
	_, err := runStep(e, ctx, remindStepPrefix+strconv.Itoa(number), func(c context.Context) (struct{}, error) {
		e.logger.InfoContext(c, fmt.Sprintf("reminder %d of %d to %s", number, len(plan.dues), strings.Join(asked.From, ", ")), "item", item)

		return struct{}{}, nil
	})
	if err != nil {
		return err
	}

	asked.Reminded, asked.NextRemind = number, plan.next(number)

	return show(ctx, item, askKey, *asked, "who it asks")
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
func (s *script) listen(plan waitPlan) (heard, error) {
	if plan.addressed() {
		id, err := runStep(s.e, s.ctx, waitIDStep, func(context.Context) (string, error) {
			return strconv.FormatInt(time.Now().UnixNano(), 36), nil
		})
		if err != nil {
			return heard{}, err
		}

		plan.id = id

		return s.waitVoices(plan)
	}

	answer, timedOut, err := s.waitOwnerUntil(plan.question, plan.deadline)

	return heard{answer: answer, timedOut: timedOut}, err
}

// collect counts voices until the quorum is reached or the deadline passes, and reports whether the deadline passed.
// A voice that the wait refuses is dropped. Each counted voice updates who the wait shows as heard.
func (s *script) collect(end time.Time, asked *Asked, count *tally) (bool, error) {
	var shown error

	for shown == nil && !count.enough(*asked) {
		v, got, late, err := s.nextVoice(end)

		switch {
		case err != nil:
			return false, err
		case late:
			return true, nil
		case got && v.Wait == asked.Wait && asked.accepts(v):
			count.take(asked, v)
			shown = show(s.ctx, s.in.Item, askKey, *asked, "who it asks")
		}
	}

	return false, shown
}

// nextVoice receives one voice. late is true when the deadline passed with no voice. A window of a wait with no deadline that ends with no voice
// gives got false, and the caller receives again.
func (s *script) nextVoice(end time.Time) (v voice, got, late bool, err error) {
	v, timedOut, err := s.hear(end)
	if timedOut {
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
		return e.deps.Clock.reading().Add(deadline).UnixMilli(), nil
	})
	if err != nil {
		return time.Time{}, err
	}

	return time.UnixMilli(millis), nil
}

// hear takes the next answer from the inbox of the run: the oldest one that a receive of another topic held, else one that arrives before end.
// A zero end is one wait window, so a wait with no deadline receives again when the window ends. timedOut is true when end passed with no answer.
func (s *script) hear(end time.Time) (v voice, timedOut bool, err error) {
	got, held := s.held(answerTopic)
	if !held {
		if end.IsZero() {
			end = s.e.deps.Clock.reading().Add(s.e.waitWindow())
		}

		if got, timedOut, err = s.await(answerTopic, end); err != nil || timedOut {
			return voice{}, timedOut, err
		}
	}

	if err = json.Unmarshal(got.Value, &v); err != nil {
		return voice{}, false, fmt.Errorf("item %s got an answer from %s that holds no text: %w", s.in.Item, got.Sender, err)
	}

	return v, false, nil
}

// waitWindow is the time of one receive of a wait with no deadline.
func (e *Engine) waitWindow() time.Duration {
	return cmp.Or(e.deps.Clock.OwnerWait, receiveWindow)
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

// Reply is an answer to the open wait of a run: the text, the person who gives it, and the key that makes a repeat of it count once. An empty Key gets a fresh one.
type Reply struct {
	From string
	Text string
	Key  string
}

// check refuses an answer with no text, and a key that the rules of a message refuse.
func (r Reply) check() error {
	if r.Text == "" {
		return fmt.Errorf("%w: write the answer after the item", errEmptyAnswer)
	}

	if r.Key == "" {
		return nil
	}

	return workflow.MessageName("key", r.Key)
}

// Reply sends an answer to the open run of an item. A wait that names people accepts only them, and a wait with choices only those texts.
// The name is not proved: beehave has no key store, and anyone who can run beehave answer on the run can give any name that the wait asks.
// A second Reply with the same key inserts nothing.
func (c *Client) Reply(item string, reply Reply) error {
	if err := reply.check(); err != nil {
		return err
	}

	last, err := c.latest(item)
	if err != nil {
		return err
	}

	info, err := c.requireQuestion(item, last)
	if err != nil {
		return err
	}

	return c.deliver(info, reply)
}

// deliver sends an answer to the inbox of the run, after the open wait accepts it. The idempotency key of the DBOS send is the key of the answer under the reserved topic, so it cannot equal the key of a beehave send: that key may not start with the reserved prefix.
func (c *Client) deliver(info RunInfo, reply Reply) error {
	asked := info.Asked

	switch {
	case asked == nil && reply.From != "":
		return fmt.Errorf("%w: this wait names no people, so leave out --as", errRefusedAnswer)
	case asked != nil:
		if err := asked.refuses(reply.From, reply.Text); err != nil {
			return err
		}
	}

	reply.Key = cmp.Or(reply.Key, strings.ToLower(rand.Text()))
	reply.Text = c.redact(reply.Text)

	return dbos.Send(c.flow, info.ID, reply.envelope(asked), inboxTopic, dbos.WithIdempotencyKey(answerTopic+"."+reply.Key))
}

// envelope is the message that carries the answer on the inbox. It names the wait that the answer is for, when the wait asks people.
func (r Reply) envelope(asked *Asked) envelope {
	value := voice{From: r.From, Text: r.Text}
	if asked != nil {
		value.Wait = asked.Wait
	}

	raw, _ := json.Marshal(value) //nolint:errcheck // three strings always encode

	return envelope{Topic: answerTopic, Sender: cmp.Or(r.From, answerSender), ID: r.Key, Value: json.RawMessage(agent.Redactor()(string(raw)))}
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
