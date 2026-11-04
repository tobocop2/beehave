package flow

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/workflow"
)

// errNoSuchRun is returned when the store holds no run of the given id.
var errNoSuchRun = errors.New("no such run")

// Step states of the per-run view. A step that the run did not finish takes the state of the run, so it can show any state of RunState.
const (
	stepDone    = "done"
	stepFailed  = "failed"
	stepRunning = "running"
)

// The builtins whose records the view decodes. The record of an agent phase is named as its builtin.
const (
	builtinAgent   = "agent"
	builtinCommand = "command"
	builtinWrite   = "write"
)

// stopOnRC is the stop reason of a command that ended the item on a code of its stop_on_rc.
const stopOnRC = "stop_on_rc"

// unrecordedStep names the step that an open run is in when it has recorded no row for it yet: a command records its row when it ends.
const unrecordedStep = "step in progress"

// callName matches the name of the DBOS step that a builtin call records: `<builtin>[:label]#<n>`.
var callName = regexp.MustCompile(`^([a-z_]+)(:.*)?#[0-9]+$`)

// StepLine is one step of a run, as the per-run view shows it. The fields after Error are the ones that decide the flow. A field the step does not have is empty.
type StepLine struct {
	Name    string    `json:"name"`
	State   string    `json:"state"`
	Started time.Time `json:"started,omitzero"`
	// Ended is the zero time while the step runs.
	Ended time.Time `json:"ended,omitzero"`
	// Seconds is how long the step took, or how long it has run when it is not over.
	Seconds  float64 `json:"duration_seconds"`
	Error    string  `json:"error,omitempty"`
	ExitCode *int    `json:"exit_code,omitempty"`
	// Kind is the kind of the lane of an agent step, as the ledger of the method names it: fix, rework, relay, review or design. A step that is not an agent, and an agent that an older version recorded, have none.
	Kind string `json:"kind,omitempty"`
	// Rounds is the cap of the loop that an agent step named with its rounds keyword. It is 0 for a step that named none.
	Rounds int `json:"rounds,omitempty"`
	// Spend is the cost of the agent runs of the call, failed runs included, with the marks of an estimate and of an unknown cost. It is empty for a step that is not an agent.
	agent.Spend
	Stop    string `json:"stop,omitempty"`
	Verdict string `json:"verdict,omitempty"`
	Head    string `json:"head,omitempty"`
	// Rewrote is set when the commit the agent started on is not in the history of its head: the agent amended, squashed or reset.
	Rewrote bool       `json:"rewrote,omitempty"`
	Passed  *bool      `json:"passed,omitempty"`
	Gate    []GateStep `json:"gate,omitempty"`
	// OutputUnreadable is set when a recorded output of the step did not decode into the type the engine wrote. The other fields are then incomplete.
	OutputUnreadable bool `json:"output_unreadable,omitempty"`
}

// RunSteps is the per-run view: the run, its state, the error of the run and its steps in order.
// Error is the error that the workflow library keeps for the run. It is empty for a run that did not fail.
// A run that fails outside a step has this error and no failed step.
type RunSteps struct {
	Run   string     `json:"run"`
	State RunState   `json:"state"`
	Error string     `json:"error,omitempty"`
	Steps []StepLine `json:"steps"`

	// RunProgress holds the same figures that the list of runs shows: the cost against the cap, the step that runs now, the deadline, the gate and the rounds.
	RunProgress
}

// Steps lists the steps of one run from the store, with the outputs decoded. It reads the step listing of the workflow library and copies nothing.
// A builtin call is one line, however many records it left. A step that runs now shows its elapsed time.
func (c *Client) Steps(id string) (RunSteps, error) {
	row, err := c.runRow(id)
	if err != nil {
		return RunSteps{}, err
	}

	records, err := c.records(id)
	if err != nil {
		return RunSteps{}, err
	}

	state := stateOf(row.Status)
	lines := stepLines(records, state, !run{Status: row}.terminal(), time.Now())

	progress := progressOf(lines, c.maxRunBudget, !run{Status: row}.terminal())
	progress.Deadline, err = c.waitDeadlineOf(row, records)

	return RunSteps{Run: id, State: state, Error: errorText(row.Error), Steps: lines, RunProgress: progress}, err
}

// waitDeadlineOf reads the question of an open run once and returns the deadline of its wait. A finished run has none and reads nothing.
func (c *Client) waitDeadlineOf(row dbos.WorkflowStatus, records []dbos.StepInfo) (time.Time, error) {
	if (run{Status: row}).terminal() {
		return time.Time{}, nil
	}

	question, _, err := c.waitOf(row.ID)

	return deadlineFor(row, records, question), err
}

// runRow reads the row of one run, with its error. The error is kept with the output of the row.
func (c *Client) runRow(id string) (dbos.WorkflowStatus, error) {
	rows, err := dbos.ListWorkflows(c.flow, dbos.WithFilterWorkflowIDs(id), dbos.WithFilterName(itemName), dbos.WithFilterLoadOutput(true))
	if err != nil {
		return dbos.WorkflowStatus{}, fmt.Errorf("read the run %s: %w", id, err)
	}

	if len(rows) == 0 {
		return dbos.WorkflowStatus{}, fmt.Errorf("%w: %q. Run beehave status to see the run ids", errNoSuchRun, id)
	}

	return rows[0], nil
}

// records reads the step records of a run. A record of a child workflow call carries the output of that child workflow in place of its own:
// the step listing names the child in ChildWorkflowID, and the workflow listing returns its output.
func (c *Client) records(id string) ([]dbos.StepInfo, error) {
	records, err := dbos.GetWorkflowSteps(c.flow, id, dbos.WithStepsLoadOutput(true))
	if err != nil {
		return nil, fmt.Errorf("read the steps of %s: %w", id, err)
	}

	var ids []string

	for _, record := range records {
		if record.ChildWorkflowID != "" {
			ids = append(ids, record.ChildWorkflowID)
		}
	}

	if len(ids) == 0 {
		return records, nil
	}

	outputs, err := c.outputsOf(ids)

	return withOutputs(records, outputs), err
}

// withOutputs puts the output of the child workflow on each record of a child workflow call.
func withOutputs(records []dbos.StepInfo, outputs map[string]any) []dbos.StepInfo {
	for i, record := range records {
		if record.ChildWorkflowID != "" {
			records[i].Output = outputs[record.ChildWorkflowID]
		}
	}

	return records
}

// outputsOf reads the outputs of workflows by id.
func (c *Client) outputsOf(ids []string) (map[string]any, error) {
	rows, err := dbos.ListWorkflows(c.flow, dbos.WithFilterWorkflowIDs(ids...), dbos.WithFilterLoadOutput(true))
	if err != nil {
		return nil, fmt.Errorf("read the child workflows of a run: %w", err)
	}

	outputs := make(map[string]any, len(rows))
	for _, row := range rows {
		outputs[row.ID] = row.Output
	}

	return outputs, nil
}

// errorText is the text of an error, and empty for no error.
func errorText(err error) string {
	if err == nil {
		return ""
	}

	return err.Error()
}

// stepLines groups the records of a run into lines. The records of one builtin call end with its own record, so the records before it belong to it.
// A group that has no ending record is the step that the run is in, or the one that failed.
func stepLines(records []dbos.StepInfo, state RunState, open bool, now time.Time) []StepLine {
	var (
		lines   []StepLine
		pending []dbos.StepInfo
	)

	for _, record := range records {
		switch {
		case endsCall(record.StepName):
			lines = append(lines, callLine(append(pending, record), record.StepName))
			pending = nil
		case partOfCall(record.StepName):
			pending = append(pending, record)
		default:
			lines = append(lines, callLine([]dbos.StepInfo{record}, record.StepName))
		}
	}

	return append(lines, unfinishedLine(records, pending, state, open, now)...)
}

// endsCall reports whether the record is the last one of a builtin call: the record of a command or a gate or another builtin, or the read of an agent.
func endsCall(name string) bool {
	base, sub, split := strings.Cut(name, "/")
	if split {
		return strings.HasPrefix(sub, "read")
	}

	match := callName.FindStringSubmatch(base)

	return match != nil && (match[1] == loginStepName || slices.Contains(workflow.Builtins(), match[1]))
}

// partOfCall reports whether the record belongs to a builtin call that ends in a later record: a sub-step, the child workflow of a phase or a gate,
// or a record of the library.
func partOfCall(name string) bool {
	return strings.ContainsAny(name, "/:#") || name == phaseName || name == exclusiveName || strings.HasPrefix(name, "DBOS.")
}

// callLine makes the line of one call from its records. The name of an agent call has no sub-step.
func callLine(group []dbos.StepInfo, name string) StepLine {
	base, _, _ := strings.Cut(name, "/")
	last := group[len(group)-1]
	line := StepLine{Name: base, State: stepDone, Started: group[0].StartedAt, Ended: last.CompletedAt}
	line.Seconds = line.Ended.Sub(line.Started).Seconds()

	for _, record := range group {
		line.absorb(record)
	}

	return line
}

// absorb adds the error and the decoded fields of one record to the line. The kind of the step picks the type that the record decodes into.
func (l *StepLine) absorb(record dbos.StepInfo) {
	if record.Error != nil && l.Error == "" {
		l.Error, l.State = record.Error.Error(), stepFailed
	}

	if record.Output != nil && !l.decode(record) {
		l.OutputUnreadable = true
	}
}

// decode takes the fields of the output of one record. A call of a child workflow decodes as the phase output of the child; any other record is picked by its name.
// It reports false for an output of a kind the view decodes that is not in the shape the engine wrote. A kind the view does not decode is not an error.
func (l *StepLine) decode(record dbos.StepInfo) bool {
	base, sub, split := strings.Cut(record.StepName, "/")

	switch {
	case record.ChildWorkflowID != "":
		return decodeThen(record.Output, l.fromPhase)
	case split && builtinOf(base) == builtinAgent:
		return l.decodeAgentSub(sub, record.Output)
	case !split && builtinOf(base) == kindGate:
		return decodeThen(record.Output, func(gate recorded[GateResult]) { l.fromGate(gate.Outputs) })
	case !split && builtinOf(base) == builtinCommand:
		return decodeThen(record.Output, l.fromCommand)
	}

	return true
}

// decodeAgentSub takes the fields of a sub-step of an agent call: the read of a finished run, or the cost record of a failed run. Any other sub-step decodes nothing.
func (l *StepLine) decodeAgentSub(sub string, output any) bool {
	switch {
	case strings.HasPrefix(sub, "read"):
		return decodeThen(output, func(read recorded[agentRead]) { l.fromAgentRead(read.Outputs) })
	case strings.HasPrefix(sub, "spent"):
		return decodeThen(output, func(spent recorded[agentFields]) { l.Spend = l.Plus(spent.Outputs.Spend) })
	}

	return true
}

// builtinOf is the builtin that a step name belongs to: `command` for `command:lint#2`.
func builtinOf(name string) string {
	builtin, _, _ := strings.Cut(name, ":")
	builtin, _, _ = strings.Cut(builtin, "#")

	return builtin
}

// recorded is the form of a step that a builtin call records: its outputs, and the command that stopped the item.
type recorded[T any] struct {
	Outputs T        `json:"outputs"`
	Stopped *Stopped `json:"stopped"`
}

// agentRead is what the read of an agent records: the fields of every agent call and the output verdict. A test checks that the tag is outputVerdict.
type agentRead struct {
	agentFields

	Verdict string `json:"verdict"`
}

// decodeThen decodes the recorded output of a step into the type that the engine wrote, and passes it to use. It reports false, and does not call use,
// for an output that is not text or does not decode.
func decodeThen[T any](output any, use func(T)) bool {
	text, ok := output.(string)
	if !ok {
		return false
	}

	var value T

	if json.Unmarshal([]byte(text), &value) != nil {
		return false
	}

	use(value)

	return true
}

// fromPhase takes the exit code of the agent from the result of its phase, a child workflow. The result of the child workflow of a gate has no outcome.
func (l *StepLine) fromPhase(phase PhaseOutput) {
	if phase.Outcome != "" {
		l.ExitCode = &phase.ExitCode
		l.Kind, l.Rounds = phase.Kind, phase.Rounds
	}
}

// fromAgentRead takes the head, the cap that stopped the agent and the verdict from the read of its outputs.
func (l *StepLine) fromAgentRead(read agentRead) {
	l.Head, l.Stop, l.Verdict, l.Rewrote = read.Head, read.Stop, read.Verdict, read.Rewrote
	l.Spend = l.Plus(read.Spend)
}

// fromGate takes the result of a gate: pass or fail, the head and the status of each gate step.
func (l *StepLine) fromGate(result GateResult) {
	l.Passed, l.Head, l.Gate = &result.Passed, cmp.Or(result.Head, l.Head), result.Steps
}

// fromCommand takes the exit code of a command, and the stop of the item when the command ended it.
func (l *StepLine) fromCommand(result recorded[CommandOutput]) {
	l.ExitCode = &result.Outputs.RC

	if result.Stopped != nil {
		l.Stop = stopOnRC
	}
}

// unfinishedLine makes the line of the step that has no ending record. It is empty when the run is at a clean point between two steps and is not open.
// An open run with no record for its step shows a line that starts when the last record ended.
func unfinishedLine(records, pending []dbos.StepInfo, state RunState, open bool, now time.Time) []StepLine {
	if len(pending) == 0 {
		if !open || len(records) == 0 {
			return nil
		}

		return []StepLine{runningLine(unrecordedStep, records[len(records)-1].CompletedAt, now)}
	}

	line := callLine(pending, openName(pending))

	// A failed step ended with its error. Any other step has not ended: the run is in it, or the run stopped in it, and the step takes the state of the run.
	// A run that has records is never queued, so an open run is running.
	if line.State != stepFailed {
		line.Ended = time.Time{}
		line.State, line.Seconds = string(state), now.Sub(line.Started).Seconds()
	}

	return []StepLine{line}
}

// runningLine is the line of a step that runs and has recorded nothing yet.
func runningLine(name string, started, now time.Time) StepLine {
	return StepLine{Name: name, State: stepRunning, Started: started, Seconds: now.Sub(started).Seconds()}
}

// openName names the call that the records belong to, from the first record that carries the name of the call. A gate that waits for its queue has no such record.
func openName(pending []dbos.StepInfo) string {
	for _, record := range pending {
		if base, _, _ := strings.Cut(record.StepName, "/"); callName.MatchString(base) {
			return base
		}
	}

	if pending[0].StepName == gateTipName {
		return kindGate
	}

	return pending[0].StepName
}
