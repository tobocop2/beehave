package flow

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/obee/internal/workflow"
)

// Rounds counts the rounds of a loop that a run used, against the cap that its agent calls named with the rounds keyword. Cap is 0 when no call named one.
type Rounds struct {
	Used int `json:"used"`
	Cap  int `json:"cap"`
}

// GateSeen is the last gate result of a run and the head that the gate judged.
type GateSeen struct {
	Passed bool   `json:"passed"`
	Head   string `json:"head,omitempty"`
}

// RunProgress is what the status of a run shows besides its state. Every value comes from what the workflow library records for the run:
// the step records, the events and the options of the client. The store holds no table for it.
type RunProgress struct {
	// Spent is the dollars that the agent steps of the run spent, failed runs included: the sum that the run cap checks.
	Spent float64 `json:"spent_usd"`
	// Cap is agents.max_run_budget_usd. It is 0 for no cap.
	Cap float64 `json:"cap_usd"`
	// Now is the step that an open run is in. It is empty for a finished run.
	Now string `json:"now,omitempty"`
	// Deadline is the time that the wait of an open run ends. It is the zero time for a run that waits with no timeout, or does not wait.
	Deadline time.Time `json:"deadline,omitzero"`
	// Gate is the last gate result. It is nil for a run that ran no gate.
	Gate *GateSeen `json:"gate,omitempty"`
	// Fix counts the agent calls of the lane rework against the rounds cap that they named. Review does the same for the lane review.
	Fix    Rounds `json:"fix_rounds"`
	Review Rounds `json:"review_rounds"`
}

// Notes lists the facts of the progress as short texts, one for each fact the run has: the cost, the step that runs now, the deadline of a wait,
// the last gate result and the rounds. A run with none has no notes.
func (p RunProgress) Notes() []string {
	return p.NotesAt(time.Time{})
}

// NotesAt is Notes with the time left to the deadline of a wait, counted from now. A zero now leaves the time left out.
func (p RunProgress) NotesAt(now time.Time) []string {
	notes := p.spentNotes()

	if p.Now != "" {
		notes = append(notes, "now "+p.Now)
	}

	if !p.Deadline.IsZero() {
		notes = append(notes, p.deadlineNote(now))
	}

	if p.Gate != nil {
		notes = append(notes, p.Gate.text())
	}

	notes = append(notes, p.Fix.notes("fix")...)

	return append(notes, p.Review.notes("review")...)
}

// deadlineNote is the note of the deadline of a wait, with the time left when now is set.
func (p RunProgress) deadlineNote(now time.Time) string {
	note := "answer by " + p.Deadline.Format(time.RFC3339)
	if now.IsZero() {
		return note
	}

	return note + ", " + TimeLeft(p.Deadline.Sub(now))
}

// day is the length of the largest unit that TimeLeft shows.
const day = 24 * time.Hour

// TimeLeft says how long a wait has to its deadline: days and hours, hours and minutes, minutes, or seconds in the last minute.
// A deadline that has passed gives "deadline passed".
func TimeLeft(left time.Duration) string {
	switch {
	case left <= 0:
		return "deadline passed"
	case left < time.Minute:
		return fmt.Sprintf("%ds left", int(left/time.Second))
	case left < time.Hour:
		return fmt.Sprintf("%dm left", int(left/time.Minute))
	case left < day:
		return fmt.Sprintf("%dh%dm left", int(left/time.Hour), int(left%time.Hour/time.Minute))
	}

	return fmt.Sprintf("%dd%dh left", int(left/day), int(left%day/time.Hour))
}

// spentNotes is the cost note: the dollars spent, against the cap when one is set. A run that spent nothing has none.
func (p RunProgress) spentNotes() []string {
	switch {
	case p.Spent <= 0:
		return nil
	case p.Cap <= 0:
		return []string{"spent $" + dollars(p.Spent)}
	}

	return []string{"spent $" + dollars(p.Spent) + " of $" + dollars(p.Cap)}
}

func (g GateSeen) text() string {
	result := "fail"
	if g.Passed {
		result = "pass"
	}

	if g.Head == "" {
		return "gate " + result
	}

	return "gate " + result + " at " + g.Head
}

// notes is the note of one lane: its name, the rounds used and the cap. A lane with no round and no cap has none.
func (r Rounds) notes(lane string) []string {
	switch {
	case r == Rounds{}:
		return nil
	case r.Cap == 0:
		return []string{lane + " " + strconv.Itoa(r.Used)}
	}

	return []string{fmt.Sprintf("%s %d of %d", lane, r.Used, r.Cap)}
}

// progressOf reads the progress of a run from its step lines. open is true for a run that has not ended. The cost, the gate and the rounds come from the
// lines, which the view of the steps decodes from the records; nothing is counted twice or stored.
func progressOf(lines []StepLine, limit float64, open bool) RunProgress {
	progress := RunProgress{Cap: limit}

	for _, line := range lines {
		progress.take(line, open)
	}

	return progress
}

// take adds one step line to the progress.
func (p *RunProgress) take(line StepLine, open bool) {
	p.Spent += line.Cost

	if line.Passed != nil {
		p.Gate = &GateSeen{Passed: *line.Passed, Head: line.Head}
	}

	switch line.Kind {
	case workflow.LaneRework:
		p.Fix.add(line)
	case workflow.LaneReview:
		p.Review.add(line)
	}

	if open && (line.State == stepRunning || line.State == string(runRunning)) {
		p.Now = line.Name
	}
}

// add counts one agent call and keeps the highest cap that a call of the lane named.
func (r *Rounds) add(line StepLine) {
	r.Used++
	r.Cap = max(r.Cap, line.Rounds)
}

// sleepStep is the name of the step in which the workflow library records the deadline of a receive, as epoch milliseconds, before the receive waits.
// A wait with no timeout receives again in windows of ownerWaitLimit, so a window of that length is no deadline.
const sleepStep = "DBOS.sleep"

// waitDeadline reads the time that the wait of an open run ends from the record of its receive. The wait has set a question, and the receive is the last
// record: a receive that has an answer records its result after the deadline, and a message that was there before the wait leaves no deadline record.
// It is the zero time for a run that does not wait, waits with no timeout, or whose record is not a time.
func waitDeadline(records []dbos.StepInfo, question string) time.Time {
	if question == "" || len(records) == 0 || records[len(records)-1].StepName != sleepStep {
		return time.Time{}
	}

	last := records[len(records)-1]

	millis, err := strconv.ParseInt(strings.Trim(fmt.Sprint(last.Output), `"`), 10, 64)
	if err != nil {
		return time.Time{}
	}

	at := time.UnixMilli(millis)
	if at.Sub(last.StartedAt) >= ownerWaitLimit-time.Minute {
		return time.Time{}
	}

	return at
}

// deadlineFor returns the deadline of the wait of an open run, given the question that the wait shows (see waitOf). A finished run has none.
func deadlineFor(row dbos.WorkflowStatus, records []dbos.StepInfo, question string) time.Time {
	if (run{Status: row}).terminal() {
		return time.Time{}
	}

	return waitDeadline(records, question)
}
