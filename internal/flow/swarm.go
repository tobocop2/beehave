package flow

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/workflow"
)

const (
	// itemsQueueName is the queue of the items that spawn starts at depth 1. Each deeper level has a queue of its own, items.<depth>.
	// Each has the cap agents.max_concurrent, so N children do not start N claims and N gates at once.
	itemsQueueName = "items"
	// rootsQueueName is the queue of the runs that a user started, at depth 0, with the same cap.
	rootsQueueName = "roots"
	// defaultEntry is the function that runs an item: main for an item that a user started, and the default entry of spawn.
	defaultEntry = "main"
	// finishEntry is the optional function that the engine calls after main on every end of the run except a cancel.
	finishEntry = "finish"
	failReturn  = "return"
	failRaise   = "raise"
	untilAll    = "all"
	untilAny    = "any"
	// nameKeyword is the keyword of a builtin call that names its step. The keywords below are those that more than one builtin takes.
	nameKeyword    = "name"
	runKeyword     = "run"
	hostKeyword    = "host"
	headKeyword    = "head"
	baseKeyword    = "base"
	pinBaseKeyword = "pin_base"
	bodyKeyword    = "body"
	titleKeyword   = "title"
	severityField  = "severity"
	numberField    = "number"
	holdKeyword    = "hold"
	ownKeyword     = "own"
	reasonKey      = "reason"
	timeoutKeyword = "timeout"
	statusKeyword  = "status"
	// maxNewProcessesKeyword is the keyword of a command call that caps the new processes of the program.
	maxNewProcessesKeyword = "max_new_processes"
	// slotsKeyword is the keyword of a command call that names the slot class of its host job.
	slotsKeyword = "slots"
)

// spawnedRun is what the spawn step records for one item: the run that gather waits for, and whether spawn starts it. A run that finished before is reused, and Start is false.
type spawnedRun struct {
	Item  string `json:"item"`
	Run   string `json:"run"`
	Login string `json:"login,omitempty"`
	Start bool   `json:"start,omitempty"`
	// Priority is the place of the run in the items queue. The plan records it, so a replay queues the run with the same place. A plan with none gives 0.
	Priority uint `json:"priority,omitempty"`
}

// childOutcome is how one spawned run ended, as the gather step records it. Error is empty for a run that succeeded.
type childOutcome struct {
	Item     string   `json:"item"`
	OK       bool     `json:"ok"`
	Error    string   `json:"error,omitempty"`
	Merge    string   `json:"merge,omitempty"`
	SentBack string   `json:"sent_back,omitempty"`
	Closed   string   `json:"closed,omitempty"`
	Stopped  *Stopped `json:"stopped,omitempty"`
	// CostUSD is the spend of the run and of every run below it, in dollars, read when the wait ended.
	CostUSD float64 `json:"cost_usd"`
	// Value is the JSON text that the entry function of the run returned. It is empty when the run returned None, and for a run that did not succeed.
	Value json.RawMessage `json:"value,omitempty"`
}

// gathered is the output of the gather step: the outcome of each run in the order of the call. Failed holds the error that fail="raise" raises after the step.
type gathered struct {
	Outcomes []childOutcome `json:"outcomes"`
	Failed   string         `json:"failed,omitempty"`
	// Canceled holds the ids of the runs that the raise canceled. A resume of the parent reopens a canceled run only when its id is here.
	Canceled []string `json:"canceled,omitempty"`
}

// spawn starts the items as runs of the same workflow file and returns their run ids. Each run executes the function entry(item) on the bytes that this run pinned.
func (s *script) spawn(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	call := spawnCall{entry: defaultEntry}

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{
		"items": &call.items, "entry": &call.entry, baseKeyword: &call.base, nameKeyword: &call.label,
	}); err != nil {
		return nil, err
	}

	plan, err := s.planRuns(call)
	if err != nil {
		return nil, err
	}

	if err := s.startRuns(plan, call.entry, s.childBase(call.base)); err != nil {
		return nil, err
	}

	s.spawned = append(s.spawned, plan...)

	return runsValue(plan), nil
}

// childRuns reads the arguments of a gather call and refuses a run that this run did not spawn.
func (s *script) childRuns(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (gatherCall, []spawnedRun, error) {
	call, refs, err := unpackGather(b, args, kwargs)
	if err != nil {
		return call, nil, err
	}

	return call, refs, s.checkChildren(refs)
}

// checkChildren refuses a run that no spawn call of this run returned. It reads no store: the plan of each spawn is a recorded step, so a replay of this run lists the same runs.
func (s *script) checkChildren(refs []spawnedRun) error {
	for _, ref := range refs {
		if !slices.ContainsFunc(s.spawned, func(one spawnedRun) bool { return one.Item == ref.Item && one.Run == ref.Run }) {
			return fmt.Errorf("run %s is not a child of this run. Pass the runs that spawn returned", ref.Run)
		}
	}

	return nil
}

// spawnCall is the arguments of a spawn call.
type spawnCall struct {
	items starlark.Value
	// base is the round base that the settings of the workflow name. Empty means that the call names none, and nothing is checked.
	base         string
	entry, label string
}

// checkBase refuses a spawn whose base is not the base of the round item that calls it. The children are cut at the base of the round item, so another base would cut them at one branch and merge them into another.
// An item that is no round item has no base of its own to compare, so the base of the call is the base of its children.
func (s *script) checkBase(base string) error {
	round, isRound := model.RoundBase(s.in.Item)
	if base == "" || !isRound || round == base {
		return nil
	}

	return fmt.Errorf("the round item %s has the base %s, and the base given to spawn is %s. Start the round with the item round/%s, or change the round base setting to %s", s.in.Item, round, base, base, round)
}

// childBase is the base that the worktree of each child is cut at, and the workflow passes the same text to the gate of the child: the base of the call, else the base of the round item, else none.
func (s *script) childBase(base string) string {
	round, _ := model.RoundBase(s.in.Item)

	return cmp.Or(base, round)
}

// planRuns checks the arguments of a spawn call, then records the plan of the runs as one step.
func (s *script) planRuns(call spawnCall) ([]spawnedRun, error) {
	ids, err := s.spawnIDs(call.items)
	if err != nil {
		return nil, err
	}

	if err := s.checkBase(call.base); err != nil {
		return nil, err
	}

	if !s.sources.Defines(call.entry) {
		return nil, fmt.Errorf("entry %q is not a function of the workflow file. Add def %s(item)", call.entry, call.entry)
	}

	return countedStep(s, "spawn", call.label, func(c context.Context) ([]spawnedRun, error) { return s.e.planSpawn(c, s.in, ids) })
}

// itemsQueueOf is the name of the items queue of the runs at a depth. A run that a user started is at depth 0 and has the queue roots, so no parent shares a queue with its children.
func itemsQueueOf(depth int) string {
	switch {
	case depth <= 0:
		return rootsQueueName
	case depth == 1:
		return itemsQueueName
	}

	return fmt.Sprintf("%s.%d", itemsQueueName, depth)
}

// spawnIDs reads the items argument.
func (s *script) spawnIDs(list starlark.Value) ([]string, error) {
	ids, err := workflow.Words("items", list)
	if err != nil {
		return nil, err
	}

	seen := map[string]bool{}

	for _, id := range ids {
		if err := s.checkItem(id, seen); err != nil {
			return nil, err
		}

		seen[id] = true
	}

	return ids, nil
}

// checkItem refuses an item id that is not an item, the parent itself and an item that was listed before.
func (s *script) checkItem(id string, seen map[string]bool) error {
	switch {
	case model.ItemKind(id) == "":
		return fmt.Errorf("%w %q: use an item id such as 12 or fix-login", ErrBadItem, id)
	case id == s.in.Item:
		return fmt.Errorf("item %s cannot spawn itself. Leave it out of items", id)
	case seen[id]:
		return fmt.Errorf("item %s is in items twice. List each item once", id)
	}

	return nil
}

// planSpawn decides, for each item, which run gather waits for. It is one recorded step, so a replay reads the plan and picks no login again.
func (e *Engine) planSpawn(ctx context.Context, in ItemInput, ids []string) ([]spawnedRun, error) {
	plan := make([]spawnedRun, 0, len(ids))

	var taken []string

	for _, id := range ids {
		one, err := e.planOne(ctx, in, id, taken)
		if err != nil {
			return nil, err
		}

		if one.Start {
			taken = append(taken, one.Login)
		}

		plan = append(plan, one)
	}

	return plan, nil
}

// spawnCap is the deepest level at which a run of the project may start: its max_spawn_depth, at most the depth of the deepest items queue of the engine.
func (e *Engine) spawnCap(project string) int {
	return min(cmp.Or(e.facts(project).MaxSpawnDepth, config.DefaultMaxSpawnDepth), len(e.itemQueues)-1)
}

// planOne plans one item. An open run is refused, as a joined run has no parent row and a cancel of the parent would not reach it.
// A failed last attempt is refused until beehave resume continues it. A run that finished with success is reused. A canceled run or no run starts the next attempt.
// taken lists the logins of the runs that this plan already starts: no run has them yet, so the pick must be told.
func (e *Engine) planOne(ctx context.Context, in ItemInput, id string, taken []string) (spawnedRun, error) {
	last, found, err := latestRun(e.ctx, id)

	switch {
	case err != nil:
		return spawnedRun{}, err
	case found && !last.terminal():
		return spawnedRun{}, openRunError(last, id, in.Project)
	case found && last.failed():
		return spawnedRun{}, failedError(last, id)
	case found && last.Status.Status == dbos.WorkflowStatusSuccess:
		return spawnedRun{Item: id, Run: last.Status.ID}, nil
	}

	return e.startOf(ctx, in, id, runID(id, last.Attempt+1), taken)
}

// startOf plans the start of the run run of an item: its login and its place in the items queue. A start one level below in past the cap of the project is refused.
func (e *Engine) startOf(ctx context.Context, in ItemInput, id, run string, taken []string) (spawnedRun, error) {
	if limit := e.spawnCap(in.Project); in.Depth+1 > limit {
		return spawnedRun{}, fmt.Errorf("item %s would run at depth %d, and the project has a cap of %d. Raise max_spawn_depth in the project file, or spawn fewer levels", id, in.Depth+1, limit)
	}

	login, err := e.pickLogin(ctx, ItemInput{Item: id, Project: in.Project}, false, taken...)
	if err != nil {
		return spawnedRun{}, err
	}

	place, err := e.itemsPlace(ctx, in.Project, id)
	if err != nil {
		return spawnedRun{}, err
	}

	return spawnedRun{Item: id, Run: run, Login: login, Start: true, Priority: place}, nil
}

// itemsPlace is the place of the run of an item in the items queue. It comes from the severity in the item fields, and a round item has no severity: no standing is at hand.
func (e *Engine) itemsPlace(ctx context.Context, project, id string) (uint, error) {
	fields, err := e.itemFields(ctx, project, id)
	if err != nil {
		return 0, fmt.Errorf("read the fields of item %s: %w", id, err)
	}

	return placeOf(severityOfItem(fields), false), nil
}

// openRunError tells why spawn does not start an item that has an open run, and how to end that run.
func openRunError(open run, item, project string) error {
	held, err := open.project()
	if err != nil {
		return err
	}

	if held != project {
		return conflictError(item, held, project)
	}

	return fmt.Errorf("item %s has an open run, %s. Run beehave status to see it, or beehave cancel %s to end it, then start this item again", item, open.Status.ID, item)
}

// startRuns starts the runs of the plan as child workflows of this run, one level deeper, on the items queue of that depth. The call is outside a step, so the library records each child.
// A replay starts nothing that the first run started: the library returns the recorded child. The plan step refused a start past the deepest queue, so each start has a queue.
func (s *script) startRuns(plan []spawnedRun, entry, base string) error {
	parent, err := dbos.GetWorkflowID(s.ctx)
	if err != nil {
		return fmt.Errorf("read the id of the run of item %s: %w", s.in.Item, err)
	}

	for _, one := range plan {
		if !one.Start {
			continue
		}

		in := ItemInput{Item: one.Item, Project: s.in.Project, File: s.in.File, Files: s.in.Files, Login: one.Login, Entry: entry, Base: base, Depth: s.in.Depth + 1, Parent: parent}

		if _, err := dbos.RunWorkflow(s.ctx, s.e.item, in, dbos.WithQueue(s.e.itemQueues[s.in.Depth+1]), dbos.WithWorkflowID(one.Run), dbos.WithPriority(one.Priority)); err != nil {
			return fmt.Errorf("start the run of item %s: %w", one.Item, err)
		}
	}

	return nil
}

// runsValue makes the struct that spawn returns: runs, a list of structs with the fields item and run.
func runsValue(plan []spawnedRun) *starlarkstruct.Struct {
	runs := make([]starlark.Value, len(plan))

	for i, one := range plan {
		runs[i] = starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{itemName: starlark.String(one.Item), runField: starlark.String(one.Run)})
	}

	return starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{runsField: starlark.NewList(runs)})
}

// gather waits for the runs that spawn returned and returns how each ended. It reads the rows of all the runs in one recorded step and makes no GetResult call in
// the workflow, so the step ids of this run do not depend on how a child ended. With fail="raise" the step cancels the runs that are still open when it sees a run that failed,
// returns all the outcomes, and the call raises after the step.
func (s *script) gather(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	call, refs, err := s.childRuns(b, args, kwargs)
	if err != nil {
		return nil, err
	}

	done, err := countedStep(s, "gather", call.label, func(c context.Context) (gathered, error) {
		return s.e.gather(c, s.in, refs, call.fail == failRaise, call.until == untilAny)
	})
	if err != nil {
		return nil, err
	}

	if done.Failed != "" {
		return nil, errors.New(done.Failed)
	}

	return outcomesValue(done.Outcomes)
}

// gatherCall is the arguments of a gather call.
type gatherCall struct {
	runs               starlark.Value
	fail, until, label string
}

// unpackGather reads and checks the arguments of a gather call, and returns the runs it names.
func unpackGather(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (gatherCall, []spawnedRun, error) {
	call := gatherCall{fail: failReturn, until: untilAll}

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{
		"runs": &call.runs, "fail": &call.fail, "until": &call.until, nameKeyword: &call.label,
	}); err != nil {
		return call, nil, err
	}

	if !slices.Contains(workflow.UntilModes(), call.until) {
		return call, nil, fmt.Errorf("`until` is %q. Use \"all\" or \"any\"", call.until)
	}

	if !slices.Contains(workflow.FailModes(), call.fail) {
		return call, nil, fmt.Errorf("`fail` is %q. Use \"return\" or \"raise\"", call.fail)
	}

	refs, err := runRefs(call.runs)

	return call, refs, err
}

// runRefs reads the runs argument: the struct that spawn returned, or a list of its run structs.
func runRefs(v starlark.Value) ([]spawnedRun, error) {
	if holder, ok := v.(starlark.HasAttrs); ok {
		if inner, err := holder.Attr("runs"); err == nil && inner != nil {
			v = inner
		}
	}

	return workflow.ListOfEach(v, oneRef, errors.New("runs must be the value that spawn returned"))
}

// oneRef reads one run struct: its item and its run id.
func oneRef(v starlark.Value) (spawnedRun, error) {
	holder, ok := v.(starlark.HasAttrs)
	if !ok {
		return spawnedRun{}, fmt.Errorf("runs must hold the runs that spawn returned, and %s is a %s", v, v.Type())
	}

	item, run := textAttr(holder, itemName), textAttr(holder, "run")
	if item == "" || run == "" {
		return spawnedRun{}, fmt.Errorf("runs must hold the runs that spawn returned, and %s has no item and run", v)
	}

	return spawnedRun{Item: item, Run: run}, nil
}

// textAttr reads a field of a struct as text. A missing field and a field that is not text give "".
func textAttr(holder starlark.HasAttrs, name string) string {
	value, err := holder.Attr(name)
	if err != nil || value == nil {
		return ""
	}

	text, _ := starlark.AsString(value)

	return text
}

// outcomesValue makes the struct that gather returns: results, a list of structs with the fields item, ok, error, merge, sent_back, closed, stopped, cost_usd and value.
// The value is what the entry function of the run returned, or None.
func outcomesValue(outcomes []childOutcome) (*starlarkstruct.Struct, error) {
	results := make([]starlark.Value, len(outcomes))

	for i, one := range outcomes {
		value, err := valueOf(one.Value)
		if err != nil {
			return nil, fmt.Errorf("read the value of the run of item %s: %w", one.Item, err)
		}

		results[i] = starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{
			itemName: starlark.String(one.Item), "ok": starlark.Bool(one.OK), "error": starlark.String(one.Error),
			mergeKey: starlark.String(one.Merge), "sent_back": starlark.String(one.SentBack), "closed": starlark.String(one.Closed), "stopped": stoppedValue(one.Stopped), "cost_usd": starlark.Float(one.CostUSD), valueField: value,
		})
	}

	return starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{"results": starlark.NewList(results)}), nil
}

// stoppedValue makes the struct of the command step that ended a run, or None.
func stoppedValue(stopped *Stopped) starlark.Value { //nolint:ireturn // the value is a struct or None
	if stopped == nil {
		return starlark.None
	}

	return starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{
		"step": starlark.String(stopped.Step), "rc": starlark.MakeInt(stopped.RC), "output": starlark.String(stopped.Output),
	})
}

// look is one read of a run: open while it can still change, and its outcome once it ended.
type look struct {
	ref      spawnedRun
	open     bool
	canceled bool
	out      childOutcome
}

// gather waits until every run ended, or until ctx ends. It reads the rows of the runs once for each poll.
// The first read that shows a failed run, with raise set, cancels the runs that are still open. The wait goes on until they ended, so the outcomes are final.
// With first set, the wait ends at the first read that shows a run no longer open, and the outcomes are those of the runs that ended. A raise that fired overrides it.
func (e *Engine) gather(ctx context.Context, in ItemInput, refs []spawnedRun, raise, first bool) (gathered, error) {
	fired := raiseResult{trigger: -1}

	for {
		looks, err := e.readRuns(refs)
		if err == nil && raise && fired.trigger < 0 {
			fired, err = e.cancelOnFailure(in.Project, looks)
		}

		if err != nil {
			return gathered{}, err
		}

		if used, over := waitOver(looks, fired, first); over {
			return e.priced(in.Item, used, fired)
		}

		if err := proc.Sleep(ctx, cmp.Or(e.deps.Clock.Poll, tickInterval)); err != nil {
			return gathered{}, err
		}
	}
}

// waitOver reports whether the wait of gather is over, and the reads that the output of the step holds when it is. It is over when no run is open. With first set and no raise fired,
// it is also over when one run is no longer open, and the output holds only the runs that ended.
func waitOver(looks []look, fired raiseResult, first bool) ([]look, bool) {
	open := func(l look) bool { return l.open }
	ended := func(l look) bool { return !l.open }

	switch {
	case !slices.ContainsFunc(looks, open):
		return looks, true
	case first && fired.trigger < 0 && slices.ContainsFunc(looks, ended):
		return slices.DeleteFunc(slices.Clone(looks), open), true
	default:
		return nil, false
	}
}

// priced builds the output of the gather step from the reads of runs that ended, with the spend of each run and of every run below it.
// The spend is read once, when the wait is over, so a poll of the wait walks no tree.
func (e *Engine) priced(parent string, used []look, fired raiseResult) (gathered, error) {
	priced := slices.Clone(used)

	for i := range priced {
		spent, err := runAndBelowSpend(e.ctx, priced[i].ref.Run)
		if err != nil {
			return gathered{}, fmt.Errorf("read the spend of run %s: %w", priced[i].ref.Run, err)
		}

		priced[i].out.CostUSD = spent.Cost
	}

	return settled(parent, priced, fired), nil
}

// raiseResult is what a raise did: trigger is the index of the first run that ended without success, or -1, and canceled holds the ids of the runs the raise canceled.
type raiseResult struct {
	trigger  int
	canceled []string
}

// cancelOnFailure cancels the runs that are still open when a run ended without success. It cancels nothing, and its trigger is -1, when no run failed.
func (e *Engine) cancelOnFailure(project string, looks []look) (raiseResult, error) {
	trigger := slices.IndexFunc(looks, func(l look) bool { return !l.open && !l.out.OK })
	if trigger < 0 {
		return raiseResult{trigger: trigger}, nil
	}

	canceled, err := e.cancelOpen(project, looks)

	return raiseResult{trigger: trigger, canceled: canceled}, err
}

// settled builds the output of the gather step from the reads of runs that all ended. fired.trigger is the index of the run that made fail="raise" cancel the others, or -1.
func settled(parent string, looks []look, fired raiseResult) gathered {
	done := gathered{Outcomes: make([]childOutcome, len(looks)), Canceled: fired.canceled}

	for i, l := range looks {
		done.Outcomes[i] = l.out
	}

	if fired.trigger >= 0 {
		failed := looks[fired.trigger]
		done.Failed = fmt.Sprintf("item %s did not finish: %s. Run beehave resume %s to reopen item %s and run the spawn again", failed.out.Item, failed.out.Error, parent, failed.out.Item)

		if failed.canceled {
			done.Failed = fmt.Sprintf("item %s did not finish: %s. It was canceled by hand, and beehave resume %s does not reopen a child canceled by hand", failed.out.Item, failed.out.Error, parent)
		}
	}

	return done
}

// readRuns reads the rows of the runs in one call. It reads on a context that the stop of the engine does not end: the store is open while the runs drain,
// and the caller ends the wait itself.
func (e *Engine) readRuns(refs []spawnedRun) ([]look, error) {
	byID, err := e.rowsOf(refs)
	if err != nil {
		return nil, err
	}

	looks := make([]look, len(refs))

	for i, ref := range refs {
		row, found := byID[ref.Run]
		looks[i] = look{ref: ref, open: found && !(run{Status: row}).terminal(), canceled: found && row.Status == dbos.WorkflowStatusCancelled, out: e.outcome(ref, row, found)}
	}

	return looks, nil
}

// rowsOf reads the rows of the runs in one call and returns them by run id.
func (e *Engine) rowsOf(refs []spawnedRun) (map[string]dbos.WorkflowStatus, error) {
	if len(refs) == 0 {
		return map[string]dbos.WorkflowStatus{}, nil // a filter with no id would list every run
	}

	ids := make([]string, len(refs))
	for i, ref := range refs {
		ids[i] = ref.Run
	}

	rows, err := dbos.ListWorkflows(dbos.WithoutCancel(e.ctx), dbos.WithFilterWorkflowIDs(ids...), dbos.WithFilterLoadOutput(true))
	if err != nil {
		return nil, fmt.Errorf("read the runs of the spawned items: %w", err)
	}

	byID := make(map[string]dbos.WorkflowStatus, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}

	return byID, nil
}

// outcome reads how a run ended from its row. Only a run that finished with success is ok. The error text passes the redactor, as the error of a step does.
func (e *Engine) outcome(ref spawnedRun, row dbos.WorkflowStatus, found bool) childOutcome {
	out := childOutcome{Item: ref.Item}

	switch {
	case !found:
		out.Error = "run " + ref.Run + " is not in the store"
	case row.Status == dbos.WorkflowStatusSuccess:
		out = succeeded(ref, row)
	case row.Error != nil:
		out.Error = row.Error.Error()
	default:
		out.Error = "the run ended as " + string(row.Status)
	}

	out.Error = e.redact(out.Error)

	return out
}

// succeeded reads the result of a run that finished with success. A result that cannot be read is a failed outcome.
func succeeded(ref spawnedRun, row dbos.WorkflowStatus) childOutcome {
	result, err := resultOf(row)
	if err != nil {
		return childOutcome{Item: ref.Item, Error: err.Error()}
	}

	return childOutcome{Item: ref.Item, OK: true, Merge: result.Merge, SentBack: result.SentBack, Closed: result.Closed, Stopped: result.Stopped, Value: result.Value}
}

// cancelOpen cancels the runs that are still open, as beehave cancel does: the cancel marker first, so an engine that stops before the release leaves it for the next start.
// The cancel reaches the phases of each run, so none waits in the queue. The tick of the engine ends each local run and gives its claim back.
// It cancels the children of a canceled run again, and returns the ids of the open runs it canceled.
func (e *Engine) cancelOpen(project string, looks []look) ([]string, error) {
	var (
		errs     []error
		canceled []string
	)

	for _, l := range looks {
		if err := e.cancelLook(project, l); err != nil {
			errs = append(errs, err)

			continue
		}

		if l.open {
			canceled = append(canceled, l.ref.Run)
		}
	}

	return canceled, errors.Join(errs...)
}

// cancelLook cancels an open run, and the children of a canceled run: a cancel that stopped between two levels of the tree left them open.
func (e *Engine) cancelLook(project string, l look) error {
	switch {
	case l.open:
		if err := e.cancelRun(project, l.ref); err != nil {
			return fmt.Errorf("cancel run %s: %w", l.ref.Run, err)
		}
	case l.canceled:
		if err := e.cancelTree(l.ref.Run); err != nil {
			return fmt.Errorf("cancel the children of run %s: %w", l.ref.Run, err)
		}
	}

	return nil
}

// cancelRun writes the cancel marker of one run and cancels the run with its children.
func (e *Engine) cancelRun(project string, ref spawnedRun) error {
	if _, err := markRunCancel(e.ctx, e.dir, ref.Run, ItemInput{Item: ref.Item, Project: project}); err != nil {
		return err
	}

	return e.cancelTree(ref.Run)
}

// cancelTree cancels a run and its children. The store walks the tree, a canceled run included, so a second call reaches the children that a first one left open.
func (e *Engine) cancelTree(id string) error {
	return dbos.CancelWorkflow(dbos.WithoutCancel(e.ctx), id, dbos.WithCancelChildren())
}
