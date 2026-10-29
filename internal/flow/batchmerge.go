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
	errNoBatch  = errors.New("this build cannot run batch_merge. Use a build that sets a batch host")
	errNoScope  = errors.New("batch_merge runs only inside a workflow run. Call it from main() of a workflow file")
	errNoMember = errors.New("`prs` is empty. Pass a list of struct(item = ..., head = ...), one for each pull request to land")

	// ErrMemberNotOpen is what the batch host returns from Intent for a pull request that is not OPEN. The batch rejects that member and goes on.
	ErrMemberNotOpen = errors.New("not OPEN")
)

// batchHost is what batch_merge needs from the build besides the gate and the merge.
// Git returns the git calls on the repository of the worktree of the item. Checkout puts a commit in that worktree and returns its tree id.
// Tip reads the tip commit of the base on the forge and its tree id. Intent reads the pull request of an item once, requires it OPEN,
// and returns the record that merge.Run needs; it writes nothing to the forge.
type batchHost interface {
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
}

// batchMerge lands the pull requests of `prs` with one gate run for the whole set, and a split of the set when the gate is red.
func (s *script) batchMerge(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	call, err := s.batchCall(b, args, kwargs)
	if err != nil {
		return nil, err
	}

	git, err := s.batchGit()
	if err != nil {
		return nil, err
	}

	deps := batch.Deps{Git: git, Gate: batchGate{s: s, run: call.gateRun}, Base: &batchBase{s: s, call: call}, Steps: stepRecorder{s: s, call: call.name}, Name: call.name}

	done, err := batch.Run(s.ctx, deps, call.members)
	if err != nil {
		return nil, fmt.Errorf("batch_merge %q: %w", call.name, err)
	}

	return batchResult(done), nil
}

// batchGit makes the worktree of the item, then returns git over it. The worktree exists before the first git call, as it does for an agent step.
func (s *script) batchGit() (batch.Git, error) { //nolint:ireturn // the interface of the batch host returns it
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
		"prs": &prs, "base": &set.Base, "repo": &set.Repo, "hold": &set.Hold, "own": &own, "forbid_text": &forbid, "gate_run": &gateRun,
		nameKeyword: &call.name,
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

	if err = errors.Join(need("base", set.Base), need("repo", set.Repo)); err != nil {
		return err
	}

	if c.members, err = members(prs); err != nil {
		return err
	}

	if c.gateRun, err = words("gate_run", gateRun); err != nil {
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
	notList := errors.New("`prs` must be a list of struct(item = ..., head = ...)")

	list, err := listOfEach(v, member, notList)
	if err != nil {
		return nil, err
	}

	if len(list) == 0 {
		return nil, errNoMember
	}

	return list, nil
}

// member reads one entry of `prs`.
func member(entry starlark.Value) (batch.Member, error) {
	pr, isStruct := entry.(*starlarkstruct.Struct)
	if !isStruct {
		return batch.Member{}, fmt.Errorf("`prs` holds a %s. Use struct(item = ..., head = ...)", entry.Type())
	}

	item, err := field(pr, itemName)
	if err != nil {
		return batch.Member{}, err
	}

	head, err := field(pr, "head")

	return batch.Member{Item: item, Head: head}, err
}

// field reads a text field of a member struct.
func field(pr *starlarkstruct.Struct, name string) (string, error) {
	value, err := pr.Attr(name)
	if err != nil {
		return "", fmt.Errorf("a member of `prs` has no `%s`. Use struct(item = ..., head = ...)", name)
	}

	text, ok := starlark.AsString(value)
	if !ok || text == "" {
		return "", fmt.Errorf("`%s` of a member of `prs` must be text that is not empty", name)
	}

	return text, nil
}

// batchResult turns the result of a batch into the struct of the builtin.
func batchResult(done batch.Result) starlark.Value { //nolint:ireturn // the builtin contract of starlark-go
	merged := make([]starlark.Value, len(done.Merged))
	for i, item := range done.Merged {
		merged[i] = starlark.String(item)
	}

	return starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{
		"merged":   starlark.NewList(merged),
		"rejected": outcomes(done.Rejected),
		"deferred": outcomes(done.Deferred),
		"tip":      starlark.String(done.Tip.Commit),
		treeName:   starlark.String(done.Tip.Tree),
		"gates":    starlark.MakeInt(done.Gates),
	})
}

func outcomes(list []batch.Outcome) *starlark.List {
	out := make([]starlark.Value, len(list))
	for i, o := range list {
		out[i] = starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{itemName: starlark.String(o.Item), "reason": starlark.String(o.Reason)})
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
	var opts []dbos.StepOption
	if !strings.HasPrefix(name, "batch:"+r.call+".gate#") {
		opts = append(opts, dbos.WithStepMaxRetries(stepRetries))
	}

	return runStep(r.s.e, r.s.ctx, name, func(context.Context) ([]byte, error) { return run() }, opts...)
}

// batchGate gates a union commit: it puts the commit in the worktree of the item, then runs the project gate or the program of gate_run.
type batchGate struct {
	s   *script
	run []string
}

func (g batchGate) Run(ctx context.Context, in batch.GateInput) (batch.GateOutcome, error) {
	tree, err := g.s.e.deps.Batch.Checkout(ctx, g.s.in.Project, g.s.in.Item, in.Commit)
	if err != nil {
		return batch.GateOutcome{}, err
	}

	if len(g.run) == 0 {
		gated, err := g.s.e.deps.Gate.Run(ctx, g.s.in.Project, g.s.in.Item)

		return batch.GateOutcome{Tree: gated.Tree, Passed: gated.Passed, Report: gated.Report}, err
	}

	out, err := g.s.e.deps.Commands.Run(ctx, CommandRequest{Project: g.s.in.Project, Item: g.s.in.Item, Run: g.run})

	return batch.GateOutcome{Tree: tree, Passed: out.RC == 0, Report: out.Stdout}, err
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
func (b *batchBase) Merge(_ context.Context, m batch.Member, prefixTree string) (batch.MergeOutcome, error) {
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

	return b.mergeReads(m, settings)
}

// mergeReads merges the member and reads again after a durable sleep while the mergeable state stays UNKNOWN, up to unknownReads reads.
func (b *batchBase) mergeReads(m batch.Member, settings MergeSettings) (batch.MergeOutcome, error) {
	for read := 1; ; read++ {
		got, err := b.mergeRead(m, settings, read)
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

// mergeRead runs one merge of the member as a step. The step name carries the number of the read.
func (b *batchBase) mergeRead(m batch.Member, settings MergeSettings, read int) (merge.Result, error) {
	return runStep(b.s.e, b.s.ctx, b.stepName("merge", m.Item, read), func(ctx context.Context) (merge.Result, error) {
		return b.s.e.deps.Merge.Merge(ctx, b.s.in.Project, m.Item, m.Head, settings)
	})
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
