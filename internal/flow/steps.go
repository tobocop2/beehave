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
	"github.com/tobocop2/beehave/internal/model"
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
	// Seconds is how long the step took, or how long it has run when it is not over. It is nil when the records give no start, or no end that is not before the start.
	Seconds *float64 `json:"duration_seconds,omitempty"`
	// WaitsOn holds the runs of the children that the step in progress waits for.
	WaitsOn  []string `json:"waits_on,omitempty"`
	Error    string   `json:"error,omitempty"`
	ExitCode *int     `json:"exit_code,omitempty"`
	// Label is the label keyword of an agent step: a free text. It is empty for a step that is not an agent and for an agent call that sets none.
	Label string `json:"label,omitempty"`
	// Login is the runner that an agent step ran on: the pool pick, or the runner that the call named. An agent that an older version recorded has none.
	Login string `json:"login,omitempty"`
	// Model is the model that an agent step set. It is empty for a step that sets none.
	Model string `json:"model,omitempty"`
	// Spend is the cost of the agent runs of the call, failed runs included, with the marks of an estimate and of an unknown cost. It is empty for a step that is not an agent.
	agent.Spend
	Stop string `json:"stop,omitempty"`
	// CapStop is set while the step waits for the owner to raise a cap.
	CapStop *CapStop `json:"cap_stop,omitempty"`
	// LimitWait is set while the step waits for a provider limit to reset.
	LimitWait *LimitWait `json:"limit_wait,omitempty"`
	Verdict   string     `json:"verdict,omitempty"`
	Head      string     `json:"head,omitempty"`
	// Rewrote is set when the commit the agent started on is not in the history of its head: the agent amended, squashed or reset.
	Rewrote bool       `json:"rewrote,omitempty"`
	Passed  *bool      `json:"passed,omitempty"`
	Gate    []GateStep `json:"gate,omitempty"`
	// NewTests holds the names of the new tests that the gate of a candidate head found. It is empty for any other step.
	NewTests []string `json:"new_tests,omitempty"`
	// Revert is the verdict of the revert run of a candidate gate: pass or fail. It is empty for any other step.
	Revert model.RevertVerdict `json:"revert,omitempty"`
	// ScopeOff is set on the line of the call that recorded a pr_closing step of a project with no forge.
	ScopeOff bool `json:"scope_off,omitempty"`
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
// It is the view of one run, so it also counts the host losses of the tree of the run.
func (c *Client) Steps(id string) (RunSteps, error) {
	view, err := c.runSteps(id)
	if err != nil {
		return view, err
	}

	return view, c.addHostLosses(id, &view.RunProgress)
}

// runSteps is the view of one run with the figures that its own records give. The view of the lanes of an item reads each run with it and walks no tree.
func (c *Client) runSteps(id string) (RunSteps, error) {
	row, err := c.runRow(id)
	if err != nil {
		return RunSteps{}, err
	}

	state := stateOf(row.Status)

	records, lines, err := c.runView(id, state, !run{Status: row}.terminal())
	if err != nil {
		return RunSteps{}, err
	}

	progress := progressOf(lines, c.maxRunBudget, !run{Status: row}.terminal())
	err = c.takeDeadline(row, records, &progress)

	return RunSteps{Run: id, State: state, Error: errorText(row.Error), Steps: lines, RunProgress: progress}, err
}

// addHostLosses counts the host losses of the item tree of the run id into the progress, from the root of the tree, with the walk of the engine (walkTree).
func (c *Client) addHostLosses(id string, progress *RunProgress) error {
	root, err := rootRun(c.store(), id)
	if err != nil {
		return err
	}

	progress.HostLosses, err = hostLossesBelow(c.store(), root)

	return err
}

// takeDeadline reads the question of an open run once and sets the deadline of its wait or its sleep. A finished run has none and reads nothing.
func (c *Client) takeDeadline(row dbos.WorkflowStatus, records []dbos.StepInfo, progress *RunProgress) error {
	if (run{Status: row}).terminal() {
		return nil
	}

	question, _, err := c.waitOf(row.ID)
	progress.takeDeadline(row, records, question)

	return err
}

// runRow reads the row of one run, with its error. The error is kept with the output of the row.
func (c *Client) runRow(id string) (dbos.WorkflowStatus, error) {
	return runRowOf(c.store(), id)
}

// runRowOf reads the row of one run from a client or from the engine.
func runRowOf(c storeView, id string) (dbos.WorkflowStatus, error) {
	rows, err := c.List(dbos.WithFilterWorkflowIDs(id), dbos.WithFilterName(itemName), dbos.WithFilterLoadOutput(true))
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

// passedLookOutput is the output that the view gives a phase that runs after its look at the caps passed: a phase output with no stop.
const passedLookOutput = "{}"

// outputsOf reads the outputs of workflows by id with outputOf.
func (c *Client) outputsOf(ids []string) (map[string]any, error) {
	rows, err := c.store().List(dbos.WithFilterWorkflowIDs(ids...), dbos.WithFilterLoadOutput(true))
	if err != nil {
		return nil, fmt.Errorf("read the child workflows of a run: %w", err)
	}

	outputs := make(map[string]any, len(rows))
	for _, row := range rows {
		if outputs[row.ID], err = c.outputOf(row); err != nil {
			return outputs, err
		}
	}

	return outputs, nil
}

// outputOf is the output of one workflow, or passedLookOutput for a phase that runs and whose look at the caps found no stop.
func (c *Client) outputOf(row dbos.WorkflowStatus) (any, error) {
	if row.Name != phaseName || row.Status != dbos.WorkflowStatusPending {
		return row.Output, nil
	}

	steps, err := dbos.GetWorkflowSteps(c.flow, row.ID, dbos.WithStepsLoadOutput(true))
	if err != nil {
		return nil, fmt.Errorf("read the look at the caps of phase %s: %w", row.ID, err)
	}

	output := row.Output
	if at := slices.IndexFunc(steps, func(step dbos.StepInfo) bool { return step.StepName == capLookStepName }); at >= 0 {
		decodeThen(steps[at].Output, func(check capCheck) {
			if check.Stop == nil {
				output = passedLookOutput
			}
		})
	}

	return output, nil
}

// failedRunsStream is the stream of an item run that holds the cost of each agent run that failed. A rewind keeps the entries of a stream,
// so beehave resume keeps the cost of the failed run that it runs again.
const failedRunsStream = "failed_runs"

// failedRun is one entry of the stream of failed runs: the agent call and the cost of the run.
type failedRun struct {
	Call string `json:"call"`
	agent.Spend
}

// runView reads the step records of a run and makes its step lines with runLines. Each view of the client reads a run here.
func (c *Client) runView(id string, state RunState, open bool) ([]dbos.StepInfo, []StepLine, error) {
	records, err := c.records(id)
	if err != nil {
		return nil, nil, err
	}

	lines, err := runLines(c.store(), id, records, state, open, time.Now())

	return records, lines, err
}

// runLines makes the step lines of a run from its records and adds the cost of each failed agent run from its stream. Every total of a cost reads
// its lines here. The read of the stream is a snapshot, because a plain read waits until the run ends. The lines come back with an error of the read too.
func runLines(ctx storeView, id string, records []dbos.StepInfo, state RunState, open bool, now time.Time) ([]StepLine, error) {
	runs, _, err := dbos.ReadStream[failedRun](ctx.flow, id, failedRunsStream, dbos.WithReadStreamSnapshot())
	if err != nil {
		err = fmt.Errorf("read the cost of the failed agent runs of %s: %w", id, err)
	}

	return withFailedRuns(stepLines(records, state, open, now), runs), err
}

// withFailedRuns adds the cost of each failed run to the line of its call. A call that the run has not reached again since a resume gets a line of its own.
func withFailedRuns(lines []StepLine, runs []failedRun) []StepLine {
	for _, run := range runs {
		at := slices.IndexFunc(lines, func(line StepLine) bool { return line.Name == run.Call })
		if at < 0 {
			lines = append(lines, StepLine{Name: run.Call, State: stepFailed})
			at = len(lines) - 1
		}

		lines[at].Spend = lines[at].Plus(run.Spend)
	}

	return lines
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
	calls, pending := callGroups(records)
	lines := make([]StepLine, 0, len(calls))

	for _, group := range calls {
		lines = append(lines, callLine(group, group[len(group)-1].StepName))
	}

	return append(lines, unfinishedLine(records, pending, state, open, now)...)
}

// callGroups splits the records of a run into the records of each finished builtin call, in order, and the records of the call that has no ending record yet.
func callGroups(records []dbos.StepInfo) (calls [][]dbos.StepInfo, pending []dbos.StepInfo) {
	for _, record := range records {
		switch {
		case endsCall(record.StepName):
			calls = append(calls, append(pending, record))
			pending = nil
		case partOfCall(record.StepName):
			pending = append(pending, record)
		default:
			calls = append(calls, []dbos.StepInfo{record})
		}
	}

	return calls, pending
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
	line.Seconds = spanSeconds(line.Started, line.Ended)

	for _, record := range group {
		line.absorb(record)
	}

	return line
}

// absorb adds the error and the decoded fields of one record to the line; a child workflow record ends a provider limit wait.
func (l *StepLine) absorb(record dbos.StepInfo) {
	if record.Error != nil && l.Error == "" {
		l.Error, l.State = record.Error.Error(), stepFailed
	}

	if record.ChildWorkflowID != "" {
		l.LimitWait = nil
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
	case !split && builtinOf(base) == prClosingKind:
		return decodeThen(record.Output, func(read recorded[closingRead]) { l.ScopeOff = !read.Outputs.Forge })
	case !split && builtinOf(base) == kindGate:
		return decodeThen(record.Output, func(gate recorded[GateResult]) { l.fromGate(gate.Outputs) })
	case !split && builtinOf(base) == builtinCommand:
		return decodeThen(record.Output, l.fromCommand)
	}

	return true
}

// decodeAgentSub takes the fields of a sub-step of an agent call: the render before the run, the read of a finished run, a stop at a cap, the cost record of a run whose limit step failed
// (and of a failed run that agent module 3 or an earlier one recorded),
// or the limit record of a run that a provider limit stopped. Any other sub-step decodes nothing.
func (l *StepLine) decodeAgentSub(sub string, output any) bool {
	switch {
	case strings.HasPrefix(sub, "render"):
		return decodeThen(output, l.fromRender)
	case strings.HasPrefix(sub, "read"):
		// The cost comes through the reader of the run cap, so a cost that cannot be read marks the line, and the sum of a tree, unknown.
		l.Spend = l.Plus(recordedSpend(output))

		return decodeThen(output, func(read recorded[agentRead]) { l.fromAgentRead(read.Outputs) })
	case strings.HasPrefix(sub, "budget"):
		return decodeThen(output, func(check capCheck) { l.CapStop = check.Stop })
	case strings.HasPrefix(sub, "spent"):
		spend := recordedSpend(output)
		l.Spend = l.Plus(spend)

		return !spend.Unreadable
	case strings.HasPrefix(sub, "limit"):
		return l.fromLimit(output)
	}

	return true
}

// fromLimit takes the wait and the cost of a limited run from its limit record. A record that does not decode marks the cost of the line unknown and unreadable,
// as a cost record that cannot be read does, so the caps never count it as 0.
func (l *StepLine) fromLimit(output any) bool {
	read := decodeThen(output, func(record limitRecord) {
		l.Spend = l.Plus(record.Spend)
		l.LimitWait = &record.LimitWait
	})
	if !read {
		l.Spend = l.Plus(agent.Spend{Unknown: true, Unreadable: true})
	}

	return read
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

// fromPhase takes the stop at a cap and the exit code of the agent from the output of a phase; a gate's child output has no outcome.
func (l *StepLine) fromPhase(phase PhaseOutput) {
	l.CapStop = phase.CapStop

	if phase.Outcome != "" {
		l.ExitCode = &phase.ExitCode
	}
}

// renderFields are the label, the login and the model that the render step of an agent call records.
type renderFields struct {
	Label string `json:"label"`
	Login string `json:"login"`
	Model string `json:"model"`
}

// fromRender takes the label, the login and the model of an agent call from its render step, which runs before the agent starts.
func (l *StepLine) fromRender(read recorded[renderFields]) {
	l.Label, l.Login, l.Model = read.Outputs.Label, read.Outputs.Login, read.Outputs.Model
}

// fromAgentRead takes the head, the cap that stopped the agent and the verdict from the read of its outputs.
func (l *StepLine) fromAgentRead(read agentRead) {
	l.Head, l.Stop, l.Verdict, l.Rewrote = read.Head, read.Stop, read.Verdict, read.Rewrote
}

// fromGate takes the result of a gate: pass or fail, the head, the status of each gate step, the names of the new tests and the revert verdict.
func (l *StepLine) fromGate(result GateResult) {
	l.Passed, l.Head, l.Gate = &result.Passed, cmp.Or(result.Head, l.Head), result.Steps

	if result.Checks != nil {
		l.NewTests, l.Revert = result.Checks.NewTests, result.Checks.Revert
	}
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

		line := runningLine(unrecordedStep, records[len(records)-1].CompletedAt, now)
		line.WaitsOn = openChildren(records)

		return []StepLine{line}
	}

	line := callLine(pending, openName(pending))

	// A failed step ended with its error. Any other step has not ended: the run is in it, or the run stopped in it, and the step takes the state of the run.
	// A run that has records is never queued, so an open run is running.
	if line.State != stepFailed {
		line.Ended = time.Time{}
		line.State, line.Seconds = string(state), spanSeconds(line.Started, now)
	}

	return []StepLine{line}
}

// runningLine is the line of a step that runs and has recorded nothing yet.
func runningLine(name string, started, now time.Time) StepLine {
	return StepLine{Name: name, State: stepRunning, Started: started, Seconds: spanSeconds(started, now)}
}

// spanSeconds is the time from start to end in seconds. It is nil when either time is the zero time or the end is before the start: no such span is a fact.
func spanSeconds(start, end time.Time) *float64 {
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return nil
	}

	seconds := end.Sub(start).Seconds()

	return &seconds
}

// openChildren lists the runs of the child workflow records that have not ended: the children that an open run waits for.
func openChildren(records []dbos.StepInfo) []string {
	var runs []string

	for _, record := range records {
		if record.ChildWorkflowID != "" && record.CompletedAt.IsZero() {
			runs = append(runs, record.ChildWorkflowID)
		}
	}

	return runs
}

// openName names the call that the records belong to, from the first record that carries the name of the call. A gate that waits for its queue has no such record.
func openName(pending []dbos.StepInfo) string {
	for _, record := range pending {
		if base, _, _ := strings.Cut(callOf(record.StepName), "/"); callName.MatchString(base) {
			return base
		}
	}

	if pending[0].StepName == gateTipName {
		return kindGate
	}

	return pending[0].StepName
}
