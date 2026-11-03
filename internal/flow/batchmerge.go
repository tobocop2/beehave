package flow

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/tobocop2/obee/internal/batch"
	"github.com/tobocop2/obee/internal/merge"
	"github.com/tobocop2/obee/internal/workflow"
)

// unknownReads is the number of reads of a pull request whose mergeable state stays UNKNOWN before the batch defers it.
const unknownReads = 3

// settleEvery is the time between two reads of a member whose mergeable state is UNKNOWN. The forge works the state out in seconds.
const settleEvery = 10 * time.Second

var (
	errNoBatch = &absentError{message: "this build cannot run batch_merge. Use a build that sets a batch host", step: "merge a batch of pull requests"}
	errNoScope = errors.New("batch_merge runs only inside a workflow run. Call it from main() of a workflow file")

	// ErrMemberNotOpen is what the batch host returns from Intent for a pull request that is not OPEN. The batch rejects that member and goes on.
	ErrMemberNotOpen = errors.New("not OPEN")
)

// batchHost is what batch_merge needs from the build besides the gate and the merge.
// Git returns the git calls on the repository of the worktree of the item. Checkout puts a commit in that worktree and returns its tree id.
// Tip reads the tip commit of the base on the forge and its tree id. Intent reads the pull request of an item once, requires it OPEN,
// and returns the record that merge.Run needs; it writes nothing to the forge.
// Issues is the read after the batch: it reads the issue of each landed item and, when integration is not zero, the closing references of that pull request.
// It reads only, and it reports a read that fails as a finding in the report, not as an error.
type batchHost interface {
	Issues(ctx context.Context, project, base string, integration int, landed []string) (batch.Report, error)
	Git(ctx context.Context, project, item string) (batch.Git, error)
	Checkout(ctx context.Context, project, item, commit string) (tree string, err error)
	Tip(ctx context.Context, project, item, base string) (batch.Tip, error)
	Intent(ctx context.Context, project, item, repo, base, tipTree string, now time.Time) (merge.Intent, error)
}

// batchCall are the arguments of a batch_merge call after the checks.
type batchCall struct {
	name     string
	members  []batch.Member
	settings MergeSettings
	gateRun  []string
	// integration is the number of the integration pull request, or 0 when the call names none.
	integration int
}

// batchMerge lands the pull requests of `prs` with one gate run for the whole set, and a split of the set when the gate is red.
func (s *script) batchMerge(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	call, err := s.batchCall(b, args, kwargs)
	if err != nil {
		return nil, err
	}

	git, err := s.batchGit(call)
	if err != nil {
		return nil, err
	}

	deps := batch.Deps{Git: git, Gate: batchGate{s: s, run: call.gateRun}, Base: &batchBase{s: s, call: call}, Steps: stepRecorder{s: s, call: call.name}, Name: call.name}

	done, err := batch.Run(s.ctx, deps, call.members)
	if err != nil {
		return nil, fmt.Errorf("batch_merge %q: %w", call.name, err)
	}

	return s.batchOutcome(call, done)
}

// batchRefusal asks whether the gate of the call may run on this machine. It is the first recorded step of the call: a fresh call is refused
// before the worktree, the union or any program, and a replay of a call that passed returns the record and is not refused.
func (s *script) batchRefusal(call batchCall) error {
	_, err := runStep(s.e, s.ctx, "batch:"+call.name+".refuse", func(context.Context) (struct{}, error) {
		return struct{}{}, s.e.refuseGate(s.in.Project, len(call.gateRun) > 0)
	})
	if err != nil {
		return fmt.Errorf("batch_merge %q: %w", call.name, err)
	}

	return nil
}

// batchOutcome reads the issues of the landed items and builds the result. The batch has landed and the merge cannot be undone,
// so a finding of the read goes into the result and never fails the call.
func (s *script) batchOutcome(call batchCall, done batch.Result) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	report, err := s.batchIssues(call, done)
	if err != nil {
		return nil, fmt.Errorf("batch_merge %q: %w", call.name, err)
	}

	return batchResult(done, report), nil
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

	dir, err := s.e.deps.Trees.Ensure(s.ctx, s.in.Project, s.in.Item)
	if err != nil {
		return nil, fmt.Errorf("find the folder of item %s: %w", s.in.Item, err)
	}

	if dir == "" {
		return nil, fmt.Errorf("item %s of project %s has no folder for the union of batch_merge", s.in.Item, s.in.Project)
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
		"prs": &prs, baseKeyword: &set.Base, "repo": &set.Repo, holdKeyword: &set.Hold, ownKeyword: &own, "forbid_text": &forbid, "gate_run": &gateRun,
		"integration": &call.integration, nameKeyword: &call.name,
	})
	if err != nil {
		return call, err
	}

	if err := s.batchScope(call.name); err != nil {
		return call, err
	}

	return call, call.fill(prs, gateRun, forbid, own)
}

// fill checks the settings of the call and reads the members, the gate program and the refusal settings from the raw arguments.
func (c *batchCall) fill(prs, gateRun, forbid, own starlark.Value) (err error) {
	set := &c.settings

	if err = errors.Join(workflow.NotEmpty("base", set.Base), workflow.NotEmpty("repo", set.Repo)); err != nil {
		return err
	}

	if c.members, err = members(prs); err != nil {
		return err
	}

	if c.gateRun, err = workflow.Words("gate_run", gateRun); err != nil {
		return err
	}

	return set.read("refuse", forbid, own)
}

// batchScope refuses a call outside a workflow run, a call with no name, a name that an earlier call of the run used, and a build with no batch host or merger.
func (s *script) batchScope(name string) error {
	switch {
	case workflowID(s.ctx) == "":
		return errNoScope
	case name == "":
		return errors.New("batch_merge needs a `name` that is not empty. The name makes the step names of the call unique")
	case s.batches[name]:
		return fmt.Errorf("batch_merge name %q is used twice in this run. Give each call its own `name`", name)
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

// members reads the `prs` argument: a list of structs with the text fields `item` and `head`.
func members(v starlark.Value) ([]batch.Member, error) {
	prs, err := workflow.PullRequests(v)
	if err != nil {
		return nil, err
	}

	list := make([]batch.Member, len(prs))
	for i, pr := range prs {
		list[i] = batch.Member{Item: pr.Item, Head: pr.Head}
	}

	return list, nil
}

// batchResult turns the result of a batch and the report of the read after it into the struct of the builtin.
// `findings` holds one line for each problem of the issues, and `note` says why no read ran.
func batchResult(done batch.Result, report batch.Report) starlark.Value { //nolint:ireturn // the builtin contract of starlark-go
	merged := make([]starlark.Value, len(done.Merged))
	for i, item := range done.Merged {
		merged[i] = starlark.String(item)
	}

	findings := make([]starlark.Value, len(report.Findings))
	for i, line := range report.Findings {
		findings[i] = starlark.String(line)
	}

	return starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{
		"merged":   starlark.NewList(merged),
		"rejected": outcomes(done.Rejected),
		"deferred": outcomes(done.Deferred),
		"tip":      starlark.String(done.Tip.Commit),
		treeName:   starlark.String(done.Tip.Tree),
		"gates":    starlark.MakeInt(done.Gates),
		"findings": starlark.NewList(findings),
		"note":     starlark.String(report.Note),
	})
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
type batchGate struct {
	s   *script
	run []string
}

// Run queues the checkout and the gate on the exclusive queue, so no merge of the repository starts while the batch gate runs.
func (g batchGate) Run(ctx context.Context, in batch.GateInput) (batch.GateOutcome, error) {
	ran, err := g.s.e.queueExclusive(ctx, ExclusiveInput{Kind: kindBatchGate, Project: g.s.in.Project, Item: g.s.in.Item, Commit: in.Commit, GateRun: g.run})

	return ran.Batch, err
}

// batchGateOutcome puts the commit in the worktree of the item, then runs the project gate or the program of gate_run.
func (e *Engine) batchGateOutcome(ctx context.Context, in ExclusiveInput) (batch.GateOutcome, error) {
	tree, err := e.deps.Batch.Checkout(ctx, in.Project, in.Item, in.Commit)
	if err != nil {
		return batch.GateOutcome{}, err
	}

	if len(in.GateRun) == 0 {
		gated, err := e.deps.Gate.Run(ctx, in.Project, in.Item, GateJob{})

		void := voidReason(gated)
		if void != "" && gated.Tree == "" {
			// A voided gate reads no tree. The checked-out tree is the tree of the union, so the batch reads the void and not a mismatch.
			gated.Tree = tree
		}

		return batch.GateOutcome{Tree: gated.Tree, Passed: gated.Passed && everyStepIsAPass(gated.Steps), Report: gated.Report, Void: void}, err
	}

	out, err := e.deps.Commands.Run(ctx, CommandRequest{Project: in.Project, Item: in.Item, Run: in.GateRun})

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
func (b *batchBase) Merge(ctx context.Context, m batch.Member, prefixTree string) (batch.MergeOutcome, error) {
	read, err := runStep(b.s.e, b.s.ctx, b.stepName("intent", m.Item, 1), func(ctx context.Context) (intentRead, error) {
		return b.intent(ctx, m)
	}, dbos.WithStepMaxRetries(stepRetries))
	if err != nil {
		return batch.MergeOutcome{}, err
	}

	if read.Refused != "" {
		return batch.MergeOutcome{Status: batch.StatusRefused, Reason: read.Refused}, nil
	}

	settings := b.call.settings
	settings.GatedTree, settings.Intent = prefixTree, &read.Intent

	return b.mergeReads(ctx, m, settings)
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
	ran, err := b.s.e.queueExclusive(ctx, ExclusiveInput{Kind: kindMerge, Project: b.s.in.Project, Item: m.Item, Head: m.Head, Settings: settings})

	return ran.Merge, err
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

// verdictOf maps the result of merge.Run to the answer of a batch. A refusal of the member rejects it. A mergeable state that is UNKNOWN is unsettled,
// and the caller reads again. A stop on the tree proof is an error, as it is for the merge builtin.
func verdictOf(got merge.Result) (out batch.MergeOutcome, unsettled bool, err error) {
	if got.Stop == merge.Done {
		return batch.MergeOutcome{Status: batch.StatusMerged}, false, nil
	}

	last := ""
	if len(got.Lines) > 0 {
		last = got.Lines[len(got.Lines)-1]
	}

	switch got.Reason {
	case merge.TreeProof:
		return out, false, fmt.Errorf("%w: %s", errMergeRefused, strings.Join(got.Lines, "; "))
	case merge.Unsettled:
		return batch.MergeOutcome{Reason: "mergeable is UNKNOWN"}, true, nil
	case merge.Transient:
		// A transient refusal defers the member without a second read. The merge call may have reached the forge, and a second merge step is never run in one attempt.
		return batch.MergeOutcome{Status: batch.StatusUnsettled, Reason: last}, false, nil
	case merge.Plain:
	}

	return batch.MergeOutcome{Status: batch.StatusRefused, Reason: last}, false, nil
}
