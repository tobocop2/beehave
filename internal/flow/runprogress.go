package flow

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/beehave/internal/agent"
)

// GateSeen is the last gate result of a run and the head that the gate judged.
type GateSeen struct {
	Passed bool   `json:"passed"`
	Head   string `json:"head,omitempty"`
}

// RunProgress is what the status of a run shows besides its state. Every value comes from what the workflow library records for the run:
// the step records, the events and the options of the client. The store holds no table for it.
type RunProgress struct {
	// Spent is what the agent steps of the run spent, failed runs included: the sum that the run cap checks. It says when the sum holds an estimate
	// and when some cost is unknown.
	Spent agent.Spend `json:"spent"`
	// Cap is agents.max_run_budget_usd. It is 0 for no cap.
	Cap float64 `json:"cap_usd"`
	// CapStop is the cap that holds an agent step of an open run back. It is nil for a run that no cap holds.
	CapStop *CapStop `json:"cap_stop,omitempty"`
	// LimitWait is the provider limit that an agent step of an open run waits for. It is nil for a run that no limit holds.
	LimitWait *LimitWait `json:"limit_wait,omitempty"`
	// HostLosses counts the host losses that the item runs of the tree of the run recorded, from the root: their steps <call>/host-loss.
	// Only the view of one run (status <run id>) walks the tree for it; the list of runs leaves it at 0. It is omitted at 0.
	HostLosses int `json:"host_losses,omitempty"`
	// Now is the step that an open run is in. It is empty for a finished run.
	Now string `json:"now,omitempty"`
	// Deadline is the time that the wait of an open run ends. It is the zero time for a run that waits with no timeout, or does not wait.
	Deadline time.Time `json:"deadline,omitzero"`
	// Sleeping is true when the deadline is the wake time of a sleep call and not the end of a wait.
	Sleeping bool `json:"sleeping,omitempty"`
	// Receiving is the topic that an open run waits on in a recv call. The deadline is the end of that wait. It is empty for a run that waits in no recv call.
	Receiving string `json:"receiving,omitempty"`
	// Streaming is the id of the child run that an open run waits on in a stream call. The deadline is the end of that wait. It is empty for a run that waits in no stream call.
	Streaming string `json:"streaming,omitempty"`
	// Gate is the last gate result. It is nil for a run that ran no gate.
	Gate *GateSeen `json:"gate,omitempty"`
	// Label is the label of the latest agent step that has one, as the workflow wrote it. It is empty for a run with no labeled agent step.
	Label string `json:"label,omitempty"`
	// ScopeOff is true when the run could not fix a scope because its project has no forge, so no merge of the run checks for a new closing issue.
	ScopeOff bool `json:"scope_off,omitempty"`
}

// Notes lists the facts of the progress as short texts, one for each fact the run has: the cost, the step that runs now, the deadline of a wait,
// the last gate result and the label. A run with none has no notes.
func (p RunProgress) Notes() []string {
	return p.NotesAt(time.Time{})
}

// NotesAt is Notes with the time left to the deadline of a wait, counted from now. A zero now leaves the time left out.
func (p RunProgress) NotesAt(now time.Time) []string {
	notes := append(p.spentNotes(), p.stopNotes()...)

	if p.Now != "" {
		notes = append(notes, "now "+p.Now)
	}

	if !p.Deadline.IsZero() {
		notes = append(notes, p.deadlineNote(now))
	}

	notes = append(notes, p.gateNotes()...)

	if p.Label != "" {
		notes = append(notes, p.Label)
	}

	return notes
}

// gateNotes are the notes of the last gate result and of a scope check that is off. A run with neither has none.
func (p RunProgress) gateNotes() []string {
	var notes []string

	if p.Gate != nil {
		notes = append(notes, p.Gate.text())
	}

	if p.ScopeOff {
		notes = append(notes, "scope check off: the project has no forge")
	}

	return notes
}

// stopNotes are the notes of the cap and of the provider limit that hold a step back. A run that nothing holds has none.
func (p RunProgress) stopNotes() []string {
	var notes []string

	if p.CapStop != nil {
		notes = append(notes, p.CapStop.text())
	}

	if p.LimitWait != nil {
		notes = append(notes, p.LimitWait.text())
	}

	return notes
}

// deadlineNote is the note of the deadline of a wait, with the time left when now is set.
func (p RunProgress) deadlineNote(now time.Time) string {
	note := "answer by " + p.Deadline.Format(time.RFC3339)

	switch {
	case p.Sleeping:
		note = "sleeps until " + p.Deadline.Format(time.RFC3339)
	case p.Receiving != "":
		note = "waits for a message on topic " + p.Receiving + " until " + p.Deadline.Format(time.RFC3339)
	case p.Streaming != "":
		note = "waits for the agent output of run " + p.Streaming + " until " + p.Deadline.Format(time.RFC3339)
	}

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

// spentNotes is the cost note: the dollars spent with the marks of the sum, against the cap when one is set. A run that spent nothing has none.
// A run whose only cost is unknown says so, and never shows $0.
func (p RunProgress) spentNotes() []string {
	switch {
	case p.Spent.Cost <= 0 && p.Spent.Unknown:
		return []string{"cost unknown"}
	case p.Spent.Cost <= 0:
		return nil
	}

	note := "spent $" + dollars(p.Spent.Cost) + spendMarks(p.Spent)
	if p.Cap > 0 {
		note += " of $" + dollars(p.Cap)
	}

	return []string{note}
}

// marks is the text after an amount that says what the amount holds: " (estimate)", " (some cost unknown)" or both. It is empty for a cost that is all reported.
func spendMarks(s agent.Spend) string {
	var marks []string

	if s.Estimated {
		marks = append(marks, "estimate")
	}

	if s.Unknown {
		marks = append(marks, "some cost unknown")
	}

	if len(marks) == 0 {
		return ""
	}

	return " (" + strings.Join(marks, ", ") + ")"
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
	p.Spent = p.Spent.Plus(line.Spend)

	if open {
		p.takeHolds(line)
	}

	if line.Passed != nil {
		p.Gate = &GateSeen{Passed: *line.Passed, Head: line.Head}
	}

	if line.Label != "" {
		p.Label = line.Label
	}

	p.ScopeOff = p.ScopeOff || line.ScopeOff

	if open && (line.State == stepRunning || line.State == string(runRunning)) {
		p.Now = line.Name
	}
}

// takeHolds keeps the cap and the provider limit that hold a step of an open run back.
func (p *RunProgress) takeHolds(line StepLine) {
	if line.CapStop != nil {
		p.CapStop = line.CapStop
	}

	if line.LimitWait != nil {
		p.LimitWait = line.LimitWait
	}
}

// sleepStep is the name of the step in which the workflow library records the deadline of a receive or of a sleep, as epoch milliseconds, before the call waits.
// A wait with no timeout receives again in windows of receiveWindow, so a window of that length is no deadline.
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
	if at.Sub(last.StartedAt) >= receiveWindow-time.Minute {
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

// takeDeadline sets the deadline of an open run: the end of its wait, else the wake time of its sleep. A finished run has none.
func (p *RunProgress) takeDeadline(row dbos.WorkflowStatus, records []dbos.StepInfo, question string) {
	p.Deadline = deadlineFor(row, records, question)

	if p.Deadline.IsZero() && !(run{Status: row}).terminal() {
		p.takeCallWait(records)
	}
}

// takeCallWait sets the deadline of the sleep call or of the recv call that an open run is in.
func (p *RunProgress) takeCallWait(records []dbos.StepInfo) {
	if p.Deadline = sleepDeadline(records); !p.Deadline.IsZero() {
		p.Sleeping = true

		return
	}

	if window, waiting := receivingOf(records); waiting {
		p.Deadline, p.Receiving = window.Until, window.Topic

		return
	}

	if window, waiting := streamingOf(records); waiting {
		p.Deadline, p.Streaming = window.Until, window.Run
	}
}
