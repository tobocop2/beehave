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
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/tobocop2/beehave/internal/batch"
	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/merge"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/review"
	"github.com/tobocop2/beehave/internal/workflow"
)

// unknownReads is the number of reads of a pull request whose mergeable state stays UNKNOWN before the batch defers it.
const unknownReads = 3

// settleEvery is the time between two reads of a member whose mergeable state is UNKNOWN. The forge works the state out in seconds.
const settleEvery = 10 * time.Second

var (
	errNoBatch = &absentError{message: "this build cannot run merge. Use a build that sets a batch host", step: "merge a batch of pull requests"}
	errNoScope = errors.New("merge runs only inside a workflow run. Call it from main() of a workflow file")

	// ErrMemberNotOpen is what the batch host returns from Intent for a pull request that is not OPEN. The batch rejects that member and goes on.
	ErrMemberNotOpen = errors.New("not OPEN")
)

// batchHost is what merge needs from the build besides the gate and the merge.
// Git returns the git calls on the repository of the worktree of the item. Checkout puts a commit in that worktree and returns its tree id.
// Tip reads the tip commit of the base on the forge and its tree id. Intent reads the pull request of an item once, requires it OPEN,
// and returns the record that merge.Run needs; it writes nothing to the forge.
// Issues is the read after the batch: it reads the issue of each landed item and, when integration is not zero, the closing references of that pull request.
// It reads only, and it reports a read that fails as a finding in the report, not as an error.
type batchHost interface {
	Issues(ctx context.Context, project, base string, integration int, landed []string) (batch.Report, error)
	// Prove reads the forge to check that each landed member is on the base, merged or on the vehicle commit. It returns the item and the reason for each member it cannot prove.
	Prove(ctx context.Context, project, base string, start, end batch.Tip, landed []batch.Member) ([]batch.Outcome, error)
	Git(ctx context.Context, project, item string) (batch.Git, error)
	Checkout(ctx context.Context, project, item, commit string) (tree string, err error)
	Tip(ctx context.Context, project, item, base string) (batch.Tip, error)
	Intent(ctx context.Context, project, item, repo, base, tipTree string, now time.Time) (merge.Intent, error)
	// Pull reads the standing of the pull request of an item: its state, draft flag, head, conflict, change requests and unresolved threads. A read that fails goes into Unread.
	// A non-empty reviewed is the commit that was marked ready: a head with the tree of that commit is a rewrite. SetDraft marks it a draft, or ready for review when draft is false.
	// Both serve a hand-off and write nothing else.
	Pull(ctx context.Context, project, item, repo, reviewed string) (review.Standing, error)
	SetDraft(ctx context.Context, project, item, repo string, draft bool) error
}

// batchCall are the arguments of a merge call after the checks.
type batchCall struct {
	name     string
	members  []batch.Member
	settings MergeSettings
	gateRun  []string
	// integration is the number of the integration pull request, or 0 when the call names none.
	integration int
	// handOff is true when the owner merges: the call marks each member ready and watches it, and lands nothing.
	handOff bool
}

// batchMerge lands the pull requests of `prs` with one gate run for the whole set, and a split of the set when the gate is red.
// With `hand_off` it marks them ready and watches them until the owner merges them.
func (s *script) batchMerge(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	call, err := s.batchCall(b, args, kwargs)
	if err != nil {
		return nil, err
	}

	if call.handOff {
		return s.handOff(call)
	}

	return s.landBatch(call)
}

// landBatch runs the gate of the union of the members and merges the members that pass.
func (s *script) landBatch(call batchCall) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	git, err := s.batchGit(call)
	if err != nil {
		return nil, err
	}

	losses := &callLosses{}
	deps := batch.Deps{Git: git, Gate: batchGate{s: s, run: call.gateRun, pinBase: call.settings.Base, losses: losses}, Base: &batchBase{s: s, call: call}, Steps: stepRecorder{s: s, call: call.name}, Name: call.name}

	done, err := batch.Run(s.ctx, deps, call.members)
	if err != nil {
		return nil, fmt.Errorf("merge %q: %w", call.name, err)
	}

	return s.batchOutcome(call, done, losses.records)
}

// batchRefusal asks whether the gate of the call may run on this machine. It is the first recorded step of the call: a fresh call is refused
// before the worktree, the union or any program, and a replay of a call that passed returns the record and is not refused.
func (s *script) batchRefusal(call batchCall) error {
	_, err := runStep(s.e, s.ctx, "batch:"+call.name+".refuse", func(context.Context) (struct{}, error) {
		return struct{}{}, s.e.refuseGate(s.in.Project, len(call.gateRun) > 0)
	})
	if err != nil {
		return fmt.Errorf("merge %q: %w", call.name, err)
	}

	return nil
}

// batchOutcome proves the landing of the merged members, reads the issues of the landed items and builds the result. The batch has landed and
// the merge cannot be undone, so an unproven member and a finding of the read go into the result and never fail the call.
// losses are the records of the lost gate jobs of the call.
func (s *script) batchOutcome(call batchCall, done batch.Result, losses []hostLoss) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	unproven, err := s.batchProof(call, done)
	if err != nil {
		return nil, fmt.Errorf("merge %q: %w", call.name, err)
	}

	report, err := s.batchIssues(call, done)
	if err != nil {
		return nil, fmt.Errorf("merge %q: %w", call.name, err)
	}

	return batchResult(done, report, unproven, losses, nil)
}

// batchProof checks as one recorded step that each merged member is on the base, so a replay returns the first proof and does not read the forge again.
// A batch that landed nothing proves nothing.
func (s *script) batchProof(call batchCall, done batch.Result) ([]batch.Outcome, error) {
	landed := make([]batch.Member, 0, len(done.Merged))

	for _, m := range call.members {
		if slices.Contains(done.Merged, m.Item) {
			landed = append(landed, m)
		}
	}

	if len(landed) == 0 {
		return []batch.Outcome{}, nil
	}

	return runStep(s.e, s.ctx, "batch:"+call.name+".prove", func(ctx context.Context) ([]batch.Outcome, error) {
		unproven, err := s.e.deps.Batch.Prove(ctx, s.in.Project, call.settings.Base, done.Start, done.Tip, landed)
		if unproven == nil {
			unproven = []batch.Outcome{}
		}

		return unproven, err
	}, dbos.WithStepMaxRetries(stepRetries))
}

// batchIssues reads the issues of the landed items as one recorded step, so a replay returns the first report and does not read the forge again.
// A batch that landed nothing reads nothing.
func (s *script) batchIssues(call batchCall, done batch.Result) (batch.Report, error) {
	if len(done.Merged) == 0 {
		return batch.Report{Findings: []string{}, Note: "issue states: no member landed, so no read ran."}, nil
	}

	return runStep(s.e, s.ctx, "batch:"+call.name+".issues", func(ctx context.Context) (batch.Report, error) {
		report, err := s.e.deps.Batch.Issues(ctx, s.in.Project, call.settings.Base, call.integration, done.Merged)
		if report.Findings == nil {
			report.Findings = []string{}
		}

		return report, err
	}, dbos.WithStepMaxRetries(stepRetries))
}

// batchGit makes the worktree of the item, then returns git over it. The worktree exists before the first git call, as it does for an agent step.
// It asks first whether the gate may run here, so a refused call touches no worktree.
func (s *script) batchGit(call batchCall) (batch.Git, error) { //nolint:ireturn // the interface of the batch host returns it
	if err := s.batchRefusal(call); err != nil {
		return nil, err
	}

	dir, err := s.tree(s.ctx)
	if err != nil {
		return nil, fmt.Errorf("find the folder of item %s: %w", s.in.Item, err)
	}

	if dir == "" {
		return nil, fmt.Errorf("item %s of project %s has no folder for the union of merge", s.in.Item, s.in.Project)
	}

	return s.e.deps.Batch.Git(s.ctx, s.in.Project, s.in.Item)
}

// batchCall unpacks and checks the arguments of the call.
func (s *script) batchCall(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (batchCall, error) {
	var (
		call                      batchCall
		prs, own, forbid, gateRun starlark.Value
		set                       = &call.settings
	)

	err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{
		"prs": &prs, baseKeyword: &set.Base, repoKeyword: &set.Repo, holdKeyword: &set.Hold, ownKeyword: &own, "forbid_text": &forbid, "gate_run": &gateRun,
		"integration": &call.integration, nameKeyword: &call.name, handOffKeyword: &call.handOff,
	})
	if err != nil {
		return call, err
	}

	if err := s.batchScope(call.name); err != nil {
		return call, err
	}

	return call, call.fill(prs, gateRun, forbid, own)
}

// handOffKeyword is the keyword of merge that hands the landing to the owner.
const handOffKeyword = "hand_off"

// refuseLanding refuses a keyword that only a landing reads. A call with `hand_off` lands nothing, so each of them would do nothing.
func (c *batchCall) refuseLanding(gateRun, forbid, own starlark.Value) error {
	if !c.handOff {
		return nil
	}

	set := map[string]bool{"hold": c.settings.Hold != "", "own": own != nil, "forbid_text": forbid != nil, "gate_run": gateRun != nil, "integration": c.integration != 0}

	for _, keyword := range workflow.HandOffLanding() {
		if set[keyword] {
			return workflow.HandOffTakes(keyword)
		}
	}

	return nil
}

// fill checks the settings of the call and reads the members, the gate program and the refusal settings from the raw arguments.
func (c *batchCall) fill(prs, gateRun, forbid, own starlark.Value) (err error) {
	set := &c.settings

	if err = c.refuseLanding(gateRun, forbid, own); err != nil {
		return err
	}

	if err = errors.Join(workflow.NotEmpty("base", set.Base), workflow.NotEmpty("repo", set.Repo)); err != nil {
		return err
	}

	if c.members, err = members(prs); err != nil {
		return err
	}

	if c.gateRun, err = workflow.Words("gate_run", gateRun); err != nil {
		return err
	}

	return set.read(forbid, own)
}

// batchScope refuses a call outside a workflow run, a call with no name, a name that an earlier call of the run used, and a build with no batch host or merger.
func (s *script) batchScope(name string) error {
	switch {
	case workflowID(s.ctx) == "":
		return errNoScope
	case name == "":
		return errors.New("merge needs a `name` that is not empty. The name makes the step names of the call unique")
	case s.batches[name]:
		return fmt.Errorf("merge name %q is used twice in this run. Give each call its own `name`", name)
	case s.e.deps.Batch == nil:
		return errNoBatch
	case s.e.deps.Merge == nil:
		return errNoMerger
	}

	if s.batches == nil {
		s.batches = map[string]bool{}
	}

	s.batches[name] = true

	return nil
}

// members reads the `prs` argument: a list of structs with the text fields `item` and `head`, and the optional bool field `approved`.
func members(v starlark.Value) ([]batch.Member, error) {
	prs, err := workflow.PullRequests(v)
	if err != nil {
		return nil, err
	}

	list := make([]batch.Member, len(prs))
	for i, pr := range prs {
		list[i] = batch.Member{Item: pr.Item, Head: pr.Head, Approved: pr.Approved, OwnDraft: pr.OwnDraft}
	}

	return list, nil
}

// batchResult turns the result of a batch and the report of the read after it into the struct of the builtin.
// `findings` holds one line for each problem of the issues, `note` says why no read ran, and `unproven` holds an item and a reason for each merged member
// that is neither in the history of the base nor on the vehicle commit. An item of `merged` that is also named in `unproven` has not landed.
// `host_loss` lists the record of each lost gate job of the call, and `host_losses` is the count of the host losses of the item tree that the last record read, or 0.
// `moved` holds the item and the new head of each member of a hand-off that went back to draft because its head moved. It is empty for a landing.
func batchResult(done batch.Result, report batch.Report, unproven []batch.Outcome, losses []hostLoss, moved []batch.Member) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	recorded, count, err := lossFields(losses)
	if err != nil {
		return nil, err
	}

	return starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{
		"merged":      textList(done.Merged),
		"rejected":    outcomes(done.Rejected),
		"deferred":    outcomes(done.Deferred),
		"tip":         starlark.String(done.Tip.Commit),
		treeName:      starlark.String(done.Tip.Tree),
		"gates":       starlark.MakeInt(done.Gates),
		"findings":    textList(report.Findings),
		"note":        starlark.String(report.Note),
		"unproven":    outcomes(unproven),
		"moved":       movedList(moved),
		hostLossKey:   recorded,
		hostLossesKey: count,
	}), nil
}

// lossFields is the list of the loss records of a call and the count that the last record read, or 0 when the call lost nothing.
func lossFields(losses []hostLoss) (starlark.Value, starlark.Value, error) { //nolint:ireturn // the values are any of the Starlark types
	records, count := make([]any, len(losses)), 0
	for i, loss := range losses {
		records[i], count = loss.Loss, loss.Losses
	}

	recorded, err := toStarlark(records)
	if err != nil {
		return nil, nil, fmt.Errorf("the host loss records of merge: %w", err)
	}

	return recorded, starlark.MakeInt(count), nil
}

// textList turns lines of text into a Starlark list of strings.
func textList(lines []string) *starlark.List {
	out := make([]starlark.Value, len(lines))
	for i, line := range lines {
		out[i] = starlark.String(line)
	}

	return starlark.NewList(out)
}

func outcomes(list []batch.Outcome) *starlark.List {
	out := make([]starlark.Value, len(list))
	for i, o := range list {
		out[i] = starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{itemName: starlark.String(o.Item), reasonKey: starlark.String(o.Reason)})
	}

	return starlark.NewList(out)
}

// stepRetries is the number of times DBOS runs a step of a batch again after it returned an error, before it records the error.
// DBOS records a step that ends in an error, and a replay returns that record without running the step. A transient failure of a git
// call, the forge or the gate would then end the run for good, so the step retries first and records only a failure that stays.
const stepRetries = 2

// stepRecorder records the steps of a batch as DBOS steps of the run. The steps that read retry. The gate step runs once: a red or failed gate
// is a result, and a second run of a gate costs the build host a full check.
type stepRecorder struct {
	s    *script
	call string
}

func (r stepRecorder) Step(_ context.Context, name string, run func() ([]byte, error)) ([]byte, error) {
	// The gate step is a child run on the exclusive queue, and a child run cannot start inside a step. The child run is the record of the gate:
	// DBOS gives it the same id on a replay and returns its result. It runs once, as the step did.
	if strings.HasPrefix(name, "batch:"+r.call+".gate#") {
		return run()
	}

	return runStep(r.s.e, r.s.ctx, name, func(context.Context) ([]byte, error) { return run() }, dbos.WithStepMaxRetries(stepRetries))
}

// batchGate gates a union commit: it puts the commit in the worktree of the item, then runs the project gate or the program of gate_run.
// pinBase is the base of the merge call: the project gate reads its pin file there. losses holds the proven losses of the whole call, over every gate of it.
type batchGate struct {
	s       *script
	run     []string
	pinBase string
	losses  *callLosses
}

// callLosses holds each lost gate job of one merge call that has a proof, in order, and its record for the result of merge.
// The runner gates one union at a time, and a replay calls the gates again in the same order and reads each recorded <call>/host-loss step,
// so a restart rebuilds the same list and keeps the count of the call.
type callLosses struct {
	jobs    []LostJob
	records []hostLoss
}

// Run queues the checkout and the gate on the exclusive queue, so no merge of the repository starts while the batch gate runs.
// A gate whose job was lost and has a proof is recorded in the item run, then runs once more on the same commit, so the verdict that never came
// is replaced and the batch counts one gate. A second proven loss in one call, on this gate or on an earlier gate of the call, is a void outcome
// on the tree of the union: the runner ends the call with ErrGateVoid and blames no member.
func (g batchGate) Run(ctx context.Context, in batch.GateInput) (batch.GateOutcome, error) {
	queued := ExclusiveInput{Kind: kindBatchGate, Project: g.s.in.Project, Item: g.s.in.Item, BuildHost: g.s.hostChosen, Commit: in.Commit, GateRun: g.run, PinBase: g.pinBase}

	for run := 1; ; run++ {
		ran, err := g.s.e.queueExclusive(ctx, queued)
		if err != nil || ran.Lost == nil {
			return ran.Batch, err
		}

		if err := g.loss(queued, gateRunName(in.Step, run), *ran.Lost); err != nil {
			return batch.GateOutcome{}, err
		}

		if proven := g.losses.jobs; len(proven) == lostJobsOfACall {
			return batch.GateOutcome{Tree: in.Tree, Void: twoLosses(proven[0], proven[1])}, nil
		}
	}
}

// lostJobsOfACall is the number of proven losses of one merge call that end the call: the first loss runs its gate again, and the second ends the call.
const lostJobsOfACall = 2

// gateRunName names the records of run number n of the batch gate of the step: the step itself for the first run, and step@n for a rerun.
func gateRunName(step string, n int) string {
	if n == 1 {
		return step
	}

	return step + readSeparator + strconv.Itoa(n)
}

// loss waits for the proof of a lost job of the batch gate, in the item run, records it as the step <call>/host-loss and keeps the job and the record
// in the losses of the call.
func (g batchGate) loss(in ExclusiveInput, call string, lost LostJob) error {
	proven, record, err := g.s.e.provenLoss(g.s.ctx, in, call, lost)
	if err != nil {
		return fmt.Errorf("the batch gate lost its job: %w", err)
	}

	g.losses.jobs = append(g.losses.jobs, proven)
	g.losses.records = append(g.losses.records, record)

	return nil
}

// twoLosses is the reason of a merge call whose gates lost two jobs with a proof. It names the host and the proof of each job.
func twoLosses(first, second LostJob) string {
	return fmt.Sprintf("the host lost two gate jobs of this batch: the gate job %s was lost on %s and the proof is %s; the gate job %s was lost on %s and the proof is %s",
		first.Job, first.Host, first.Proof.Kind, second.Job, second.Host, second.Proof.Kind)
}

// batchStart is what the first step of the project gate of a batch records: the tree of the checked-out union commit and the host job that the gate submitted.
// A replay reads it and checks out and submits nothing.
type batchStart struct {
	Tree string  `json:"tree"`
	Job  GateJob `json:"job"`
}

// batchFinish is the record of the step that waits for the host job of a batch gate: the outcome, or the job that the wait lost.
// A loss is a value and not an error, because a step error is replayed as text and a later step could not read it.
type batchFinish struct {
	Outcome batch.GateOutcome `json:"outcome"`
	Lost    *LostJob          `json:"lost,omitempty"`
}

// batchGateCall runs the gate of a union as recorded steps. A gate_run program has no host job and is one step, batch_gate.
// The project gate is two, as for a gate call: batch_gate:job checks out the commit and submits the host job, and batch_gate waits for that job.
// A crash between the two leaves the job on record, so the host gets one job. A wait that ended as lost is followed by the wait for its proof (holdLost),
// in this run, so the host slot and the exclusive slot of the project stay held while the job may still run.
func (e *Engine) batchGateCall(ctx dbos.Context, in ExclusiveInput, out *ExclusiveOutput) (batch.GateOutcome, error) {
	if len(in.GateRun) > 0 {
		return runStep(e, ctx, kindBatchGate, func(c context.Context) (batch.GateOutcome, error) { return e.programBatchOutcome(c, in) })
	}

	start, err := runStep(e, ctx, batchGateJobName, func(c context.Context) (batchStart, error) { return e.startBatchGate(c, in) })
	if err != nil {
		return batch.GateOutcome{}, err
	}

	finished, err := runStep(e, ctx, kindBatchGate, func(c context.Context) (batchFinish, error) { return e.finishBatchGate(c, in, start) })
	if err != nil || finished.Lost == nil {
		return finished.Outcome, err
	}

	return e.endBatchLost(ctx, in, start, *finished.Lost, out)
}

// endBatchLost waits for the proof that the lost job of a batch gate can no longer run, and stores the job in out. A proof gives the void outcome
// of the gate. A wait that reaches host.lost_job_wait with no proof, or that fails, gives no outcome, as endLost does for a gate.
func (e *Engine) endBatchLost(ctx dbos.Context, in ExclusiveInput, start batchStart, lost LostJob, out *ExclusiveOutput) (batch.GateOutcome, error) {
	held, err := e.holdLost(ctx, in, lost)
	out.Lost = &held

	if err != nil || held.Limit {
		return batch.GateOutcome{}, err
	}

	return lostOutcome(start.Tree, held), nil
}

// startBatchGate puts the union commit in the worktree of the item, then submits the host job of the project gate on it.
func (e *Engine) startBatchGate(ctx context.Context, in ExclusiveInput) (batchStart, error) {
	tree, err := e.deps.Batch.Checkout(ctx, in.Project, in.Item, in.Commit)
	if err != nil {
		return batchStart{}, err
	}

	job, err := e.deps.Gate.Submit(ctx, in.Project, in.Item, in.BuildHost, "", in.PinBase, "", in.gateSlot())

	return batchStart{Tree: tree, Job: job}, err
}

// finishBatchGate waits for the host job of the batch gate and judges it. A wait that ended as lost while the run still runs gives the lost job and no outcome.
func (e *Engine) finishBatchGate(ctx context.Context, in ExclusiveInput, start batchStart) (batchFinish, error) {
	gated, err := e.deps.Gate.Run(ctx, in.Project, in.Item, in.BuildHost, start.Job)
	if errors.Is(err, host.ErrLost) && ctx.Err() == nil {
		return batchFinish{Lost: batchLostJobOf(in, start.Job, err)}, nil
	}

	void := voidReason(gated)
	if void != "" && gated.Tree == "" {
		// A voided gate reads no tree. The checked-out tree is the tree of the union, so the batch reads the void and not a mismatch.
		gated.Tree = start.Tree
	}

	return batchFinish{Outcome: batch.GateOutcome{Tree: gated.Tree, Passed: gated.Passed && everyStepIsAPass(gated.Steps), Report: gated.Report, Void: void}}, err
}

// programBatchOutcome puts the union commit in the worktree of the item, then runs the program of gate_run on it.
func (e *Engine) programBatchOutcome(ctx context.Context, in ExclusiveInput) (batch.GateOutcome, error) {
	tree, err := e.deps.Batch.Checkout(ctx, in.Project, in.Item, in.Commit)
	if err != nil {
		return batch.GateOutcome{}, err
	}

	out, err := e.deps.Commands.Run(ctx, CommandRequest{Project: in.Project, Item: in.Item, Run: in.GateRun, Gate: true})

	return batch.GateOutcome{Tree: tree, Passed: out.RC == 0, Report: out.Stdout}, err
}

// voidReason is the report of a gate that gave no verdict about the code: no step failed and at least one step is unknown.
// The pinned tool at another version leaves every step unknown. A command that passed with no test result leaves one.
// A failed step is a real red gate, so it is never void. A gate with no step has no result either, but it stays a red gate as before.
func voidReason(gated GateResult) string {
	unknown := false

	for _, s := range gated.Steps {
		if s.Status == failStatus {
			return ""
		}

		unknown = unknown || s.Status == unknownStatus
	}

	if !unknown {
		return ""
	}

	if strings.TrimSpace(gated.Report) == "" {
		return "a step of the gate is unknown"
	}

	return gated.Report
}

// everyStepIsAPass is true when the gate recorded at least one step and every step passed. An unknown step, or no step, is not a pass.
func everyStepIsAPass(steps []GateStep) bool {
	for _, s := range steps {
		if s.Status != passStatus {
			return false
		}
	}

	return len(steps) > 0
}

// batchBase reads the base tip and merges the members.
type batchBase struct {
	s    *script
	call batchCall
}

func (b *batchBase) Tip(ctx context.Context) (batch.Tip, error) {
	return b.s.e.deps.Batch.Tip(ctx, b.s.in.Project, b.s.in.Item, b.call.settings.Base)
}

// Merge records the intent of the member, then merges through merge.Run with that intent. The intent step comes first, so a merge that the forge
// made before a crash counts as ours on the replay. A mergeable state that stays UNKNOWN is read again after a durable sleep.
// The statuses go to the merge run in its settings, and the run posts them after its guards and right before its merge call.
func (b *batchBase) Merge(ctx context.Context, m batch.Member, prefixTree string, statuses []model.CommitStatus) (batch.MergeOutcome, error) {
	refused, kept, err := b.holdScope(m)
	if err != nil {
		return batch.MergeOutcome{}, err
	}

	if refused != "" {
		return batch.MergeOutcome{Status: batch.StatusRefused, Reason: refused}, nil
	}

	read, err := b.recordedIntent(m)
	if err != nil {
		return batch.MergeOutcome{}, err
	}

	if read.Refused != "" {
		return batch.MergeOutcome{Status: batch.StatusRefused, Reason: read.Refused}, nil
	}

	return b.mergeReads(ctx, m, b.memberSettings(m, prefixTree, &read.Intent, kept, statuses))
}

// recordedIntent is the intent step of the member.
func (b *batchBase) recordedIntent(m batch.Member) (intentRead, error) {
	return runStep(b.s.e, b.s.ctx, b.stepName("intent", m.Item, 1), func(ctx context.Context) (intentRead, error) {
		return b.intent(ctx, m)
	}, dbos.WithStepMaxRetries(stepRetries))
}

// memberSettings are the settings of the merge of one member. The approval, the tree, the intent, the kept issues and the statuses are facts about this
// member only, so the call settings never carry them.
func (b *batchBase) memberSettings(m batch.Member, prefixTree string, intent *merge.Intent, kept []model.IssueRef, statuses []model.CommitStatus) MergeSettings {
	settings := b.call.settings
	settings.GatedTree, settings.Intent, settings.Approved, settings.Kept = prefixTree, intent, m.Approved, kept
	settings.OwnDraft, settings.Statuses = m.OwnDraft, statuses

	return settings
}

// mergeReads merges the member and reads again after a durable sleep while the mergeable state stays UNKNOWN, up to unknownReads reads.
func (b *batchBase) mergeReads(ctx context.Context, m batch.Member, settings MergeSettings) (batch.MergeOutcome, error) {
	for read := 1; ; read++ {
		got, err := b.mergeRead(ctx, m, settings)
		if err != nil {
			return batch.MergeOutcome{}, err
		}

		out, unsettled, err := verdictOf(got)
		if err != nil || !unsettled {
			return out, err
		}

		if read == unknownReads {
			return batch.MergeOutcome{Status: batch.StatusUnsettled, Reason: fmt.Sprintf("%s after %d reads of %s", out.Reason, read, m.Item)}, nil
		}

		if _, err := dbos.Sleep(b.s.ctx, cmp.Or(b.s.e.deps.Clock.Settle, settleEvery)); err != nil {
			return batch.MergeOutcome{}, err
		}
	}
}

// mergeRead runs one merge of the member as a child run on the exclusive queue.
func (b *batchBase) mergeRead(ctx context.Context, m batch.Member, settings MergeSettings) (merge.Result, error) {
	ran, err := b.s.e.queueExclusive(ctx, ExclusiveInput{Kind: kindMerge, Run: workflowID(b.s.ctx), RunItem: b.s.in.Item, Project: b.s.in.Project, Item: m.Item, Head: m.Head, Settings: settings})
	if err != nil {
		return ran.Merge, err
	}

	return b.s.e.awaitQueued(b.s.ctx, b.stepName("poll", m.Item, 1), queuedRead{project: b.s.in.Project, item: m.Item, head: m.Head, settings: settings}, ran.Merge) //nolint:contextcheck // the batch session holds the context of the run in a field, and every step of the file uses it
}

// intentRead is the record of the intent step: the intent, or the reason the member cannot be merged because its pull request is not OPEN.
type intentRead struct {
	Intent  merge.Intent
	Refused string
}

// intent reads the base tip, then the pull request of the member against that tip. A pull request that is not OPEN is a refusal of the member,
// recorded as a result: DBOS keeps the text of a recorded error and not its type, so an error would end the run for good.
func (b *batchBase) intent(ctx context.Context, m batch.Member) (intentRead, error) {
	tip, err := b.Tip(ctx)
	if err != nil {
		return intentRead{}, err
	}

	intent, err := b.s.e.deps.Batch.Intent(ctx, b.s.in.Project, m.Item, b.call.settings.Repo, b.call.settings.Base, tip.Tree, b.s.e.deps.Clock.now())
	if errors.Is(err, ErrMemberNotOpen) {
		return intentRead{Refused: err.Error()}, nil
	}

	return intentRead{Intent: intent}, err
}

// readSeparator comes between a step name and the number of a later read. An item id has no "@", so the name of a later read of one item
// never equals the name of the first read of another item.
const readSeparator = "@"

// stepName names a step of one member: batch:<name>.<kind>#<item>, and a later read of the same member adds @<read>.
func (b *batchBase) stepName(kind, item string, read int) string {
	name := fmt.Sprintf("batch:%s.%s#%s", b.call.name, kind, item)
	if read > 1 {
		name += fmt.Sprintf("%s%d", readSeparator, read)
	}

	return name
}

// verdictOf maps the result of merge.Run to the answer of a batch. A refusal of the member rejects it, and a transient one defers it, with every line
// of the refusal as the reason. A mergeable state that is UNKNOWN is unsettled, and the caller reads again. A stop on the tree proof is an error, as it is for the merge builtin.
func verdictOf(got merge.Result) (out batch.MergeOutcome, unsettled bool, err error) {
	if got.Stop == merge.Done {
		return batch.MergeOutcome{Status: batch.StatusMerged}, false, nil
	}

	reason := strings.Join(got.Refusal, "\n")

	switch got.Reason {
	case merge.TreeProof:
		return out, false, fmt.Errorf("%w: %s", errMergeRefused, strings.Join(got.Lines, "; "))
	case merge.Unsettled:
		return batch.MergeOutcome{Reason: "mergeable is UNKNOWN"}, true, nil
	case merge.Transient:
		// A transient refusal defers the member without a second read. The merge call may have reached the forge, and a second merge step is never run in one attempt.
		return batch.MergeOutcome{Status: batch.StatusUnsettled, Reason: reason}, false, nil
	case merge.Plain, merge.Queued:
	}

	return batch.MergeOutcome{Status: batch.StatusRefused, Reason: reason}, false, nil
}
