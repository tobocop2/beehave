package flow

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/workflow"
)

const (
	// itemsQueueName is the queue of the items that spawn starts. It has the cap agents.max_concurrent, so N children do not start N claims and N gates at once.
	itemsQueueName = "items"
	// defaultEntry is the function that runs an item: main for an item that a user started, and the default entry of spawn.
	defaultEntry = "main"
	failReturn   = "return"
	failRaise    = "raise"
)

// spawnedRun is what the spawn step records for one item: the run that gather waits for, and whether spawn starts it. A run that finished before is reused, and Start is false.
type spawnedRun struct {
	Item  string `json:"item"`
	Run   string `json:"run"`
	Login string `json:"login,omitempty"`
	Start bool   `json:"start,omitempty"`
}

// childOutcome is how one spawned run ended, as the gather step records it. Error is empty for a run that succeeded.
type childOutcome struct {
	Item     string   `json:"item"`
	OK       bool     `json:"ok"`
	Error    string   `json:"error,omitempty"`
	Merge    string   `json:"merge,omitempty"`
	SentBack string   `json:"sent_back,omitempty"`
	Stopped  *Stopped `json:"stopped,omitempty"`
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

	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "items", &call.items, "entry?", &call.entry, "name?", &call.label); err != nil {
		return nil, err
	}

	plan, err := s.planRuns(call)
	if err != nil {
		return nil, err
	}

	if err := s.startRuns(plan, call.entry); err != nil {
		return nil, err
	}

	return runsValue(plan), nil
}

// spawnCall is the arguments of a spawn call.
type spawnCall struct {
	items        starlark.Value
	entry, label string
}

// planRuns checks the arguments of a spawn call, then records the plan of the runs as one step.
func (s *script) planRuns(call spawnCall) ([]spawnedRun, error) {
	ids, err := s.spawnIDs(call.items)
	if err != nil {
		return nil, err
	}

	if !s.sources.Defines(call.entry) {
		return nil, fmt.Errorf("entry %q is not a function of the workflow file. Add def %s(item)", call.entry, call.entry)
	}

	return runStep(s.e, s.ctx, s.name("spawn", call.label), func(c context.Context) ([]spawnedRun, error) { return s.e.planSpawn(c, s.in, ids) })
}

// spawnIDs reads the items argument. A run that spawn started cannot spawn: the parent would wait for a child that holds a slot of the items queue.
func (s *script) spawnIDs(list starlark.Value) ([]string, error) {
	if s.in.Entry != "" {
		return nil, fmt.Errorf("item %s was started by spawn and cannot start items. Call spawn from the item that a user started", s.in.Item)
	}

	ids, err := words("items", list)
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

	for _, id := range ids {
		one, err := e.planOne(ctx, in, id)
		if err != nil {
			return nil, err
		}

		plan = append(plan, one)
	}

	return plan, nil
}

// planOne plans one item. An open run is refused, as a joined run has no parent row and a cancel of the parent would not reach it.
// A failed last attempt is refused until obee resume continues it. A run that finished with success is reused. A canceled run or no run starts the next attempt.
func (e *Engine) planOne(ctx context.Context, in ItemInput, id string) (spawnedRun, error) {
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

	login, err := e.pickLogin(ctx, ItemInput{Item: id, Project: in.Project})
	if err != nil {
		return spawnedRun{}, err
	}

	return spawnedRun{Item: id, Run: runID(id, last.Attempt+1), Login: login, Start: true}, nil
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

	return fmt.Errorf("item %s has an open run, %s. Run obee status to see it, or obee cancel %s to end it, then start this item again", item, open.Status.ID, item)
}

// startRuns starts the runs of the plan as child workflows of this run, on the items queue. The call is outside a step, so the library records each child.
// A replay starts nothing that the first run started: the library returns the recorded child.
func (s *script) startRuns(plan []spawnedRun, entry string) error {
	for _, one := range plan {
		if !one.Start {
			continue
		}

		in := ItemInput{Item: one.Item, Project: s.in.Project, File: s.in.File, Files: s.in.Files, Login: one.Login, Entry: entry}

		if _, err := dbos.RunWorkflow(s.ctx, s.e.item, in, dbos.WithQueue(s.e.itemQueue), dbos.WithWorkflowID(one.Run)); err != nil {
			return fmt.Errorf("start the run of item %s: %w", one.Item, err)
		}
	}

	return nil
}

// runsValue makes the struct that spawn returns: runs, a list of structs with the fields item and run.
func runsValue(plan []spawnedRun) *starlarkstruct.Struct {
	runs := make([]starlark.Value, len(plan))

	for i, one := range plan {
		runs[i] = starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{"item": starlark.String(one.Item), "run": starlark.String(one.Run)})
	}

	return starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{"runs": starlark.NewList(runs)})
}

// gather waits for the runs that spawn returned and returns how each ended. It reads the rows of all the runs in one recorded step and makes no GetResult call in
// the workflow, so the step ids of this run do not depend on how a child ended. With fail="raise" the step cancels the runs that are still open when it sees a run that failed,
// returns all the outcomes, and the call raises after the step.
func (s *script) gather(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	call, refs, err := unpackGather(b, args, kwargs)
	if err != nil {
		return nil, err
	}

	done, err := runStep(s.e, s.ctx, s.name("gather", call.label), func(c context.Context) (gathered, error) {
		return s.e.gather(c, s.in, refs, call.fail == failRaise)
	})
	if err != nil {
		return nil, err
	}

	if done.Failed != "" {
		return nil, errors.New(done.Failed)
	}

	return outcomesValue(done.Outcomes), nil
}

// gatherCall is the arguments of a gather call.
type gatherCall struct {
	runs        starlark.Value
	fail, label string
}

// unpackGather reads and checks the arguments of a gather call, and returns the runs it names.
func unpackGather(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (gatherCall, []spawnedRun, error) {
	call := gatherCall{fail: failReturn}

	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "runs", &call.runs, "fail?", &call.fail, "name?", &call.label); err != nil {
		return call, nil, err
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

	return listOfEach(v, oneRef, errors.New("runs must be the value that spawn returned"))
}

// oneRef reads one run struct: its item and its run id.
func oneRef(v starlark.Value) (spawnedRun, error) {
	holder, ok := v.(starlark.HasAttrs)
	if !ok {
		return spawnedRun{}, fmt.Errorf("runs must hold the runs that spawn returned, and %s is a %s", v, v.Type())
	}

	item, run := textAttr(holder, "item"), textAttr(holder, "run")
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

// outcomesValue makes the struct that gather returns: results, a list of structs with the fields item, ok, error, merge, sent_back and stopped.
func outcomesValue(outcomes []childOutcome) *starlarkstruct.Struct {
	results := make([]starlark.Value, len(outcomes))

	for i, one := range outcomes {
		var stopped starlark.Value = starlark.None

		if one.Stopped != nil {
			stopped = starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{
				"step": starlark.String(one.Stopped.Step), "rc": starlark.MakeInt(one.Stopped.RC), "output": starlark.String(one.Stopped.Output),
			})
		}

		results[i] = starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{
			"item": starlark.String(one.Item), "ok": starlark.Bool(one.OK), "error": starlark.String(one.Error),
			"merge": starlark.String(one.Merge), "sent_back": starlark.String(one.SentBack), "stopped": stopped,
		})
	}

	return starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{"results": starlark.NewList(results)})
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
func (e *Engine) gather(ctx context.Context, in ItemInput, refs []spawnedRun, raise bool) (gathered, error) {
	fired := raiseResult{trigger: -1}

	for {
		looks, err := e.readRuns(refs)
		if err == nil && raise && fired.trigger < 0 {
			fired, err = e.cancelOnFailure(in.Project, looks)
		}

		if err != nil {
			return gathered{}, err
		}

		if !slices.ContainsFunc(looks, func(l look) bool { return l.open }) {
			return settled(in.Item, looks, fired), nil
		}

		if err := pause(ctx, cmp.Or(e.deps.Clock.Poll, tickInterval)); err != nil {
			return gathered{}, err
		}
	}
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
		done.Failed = fmt.Sprintf("item %s did not finish: %s. Run obee resume %s to reopen item %s and run the spawn again", failed.out.Item, failed.out.Error, parent, failed.out.Item)

		if failed.canceled {
			done.Failed = fmt.Sprintf("item %s did not finish: %s. It was canceled by hand, and obee resume %s does not reopen a child canceled by hand", failed.out.Item, failed.out.Error, parent)
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

	return childOutcome{Item: ref.Item, OK: true, Merge: result.Merge, SentBack: result.SentBack, Stopped: result.Stopped}
}

// cancelOpen cancels the runs that are still open, as obee cancel does: the cancel marker first, so an engine that stops before the release leaves it for the next start.
// The cancel reaches the phases of each run, so none waits in the queue. The tick of the engine ends each local run and gives its claim back.
// It returns the ids of the runs it canceled.
func (e *Engine) cancelOpen(project string, looks []look) ([]string, error) {
	var (
		errs     []error
		canceled []string
	)

	for _, l := range looks {
		if !l.open {
			continue
		}

		if err := e.cancelRun(project, l.ref); err != nil {
			errs = append(errs, fmt.Errorf("cancel run %s: %w", l.ref.Run, err))

			continue
		}

		canceled = append(canceled, l.ref.Run)
	}

	return canceled, errors.Join(errs...)
}

// cancelRun writes the cancel marker of one run and cancels the run with its children.
func (e *Engine) cancelRun(project string, ref spawnedRun) error {
	if err := markCancel(e.dir, ref.Run, ItemInput{Item: ref.Item, Project: project}); err != nil {
		return err
	}

	return dbos.CancelWorkflow(dbos.WithoutCancel(e.ctx), ref.Run, dbos.WithCancelChildren())
}
