package flow

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/tobocop2/beehest/internal/landing"
	"github.com/tobocop2/beehest/internal/merge"
	"github.com/tobocop2/beehest/internal/model"
	"github.com/tobocop2/beehest/internal/review"
	"github.com/tobocop2/beehest/internal/workflow"
)

// unknownReads is the number of reads of a pull request whose mergeable state stays UNKNOWN before the merge defers it.
const unknownReads = 3

// settleEvery is the time between two reads of a member whose mergeable state is UNKNOWN. The forge works the state out in seconds.
const settleEvery = 10 * time.Second

// reasonMoved is the reason of a member that the merge left unlanded because the base moved.
const reasonMoved = "the base moved while the batch ran; run the batch again"

var (
	errNoBatch = &absentError{message: "this build cannot run merge. Use a build that sets a batch host", step: "merge a batch of pull requests"}
	errNoScope = errors.New("merge runs only inside a workflow run. Call it from main() of a workflow file")

	// ErrMemberNotOpen is what the batch host returns from Intent for a pull request that is not OPEN. The merge rejects that member.
	ErrMemberNotOpen = errors.New("not OPEN")
)

// batchHost is what merge, hand_off and the reads of the batch library need from the build besides the gate and the merge.
// Git returns the git calls on the repository of the worktree of the item. Tip reads the tip commit of the base on the forge and its tree id.
// Intent reads the pull request of an item once, requires it OPEN, and returns the record that merge.Run needs; it writes nothing to the forge.
type batchHost interface {
	// Prove reads the forge to check that each landed member is on the base, merged or on the vehicle commit. It returns the item and the reason for each member it cannot prove.
	Prove(ctx context.Context, project, base string, start, end landing.Tip, landed []landing.Member) ([]landing.Outcome, error)
	Git(ctx context.Context, project, item string) (landing.GitCLI, error)
	Tip(ctx context.Context, project, item, base string) (landing.Tip, error)
	Intent(ctx context.Context, project, item, repo, base, tipTree string, now time.Time) (merge.Intent, error)
	// Pull reads the standing of the pull request of an item: its state, draft flag, head, conflict, change requests and unresolved threads. A read that fails goes into Unread.
	// A non-empty reviewed is the commit that was marked ready: a head with the tree of that commit is a rewrite. SetDraft marks it a draft, or ready for review when draft is false.
	// Both serve a hand-off and write nothing else.
	Pull(ctx context.Context, project, item, repo, reviewed string) (review.Standing, error)
	SetDraft(ctx context.Context, project, item, repo string, draft bool) error
}

// batchCall are the arguments of a merge or a hand_off call after the checks.
type batchCall struct {
	name     string
	members  []workflow.PullRequest
	settings MergeSettings
	// handOff is true when the owner merges: the call marks each member ready and watches it, and lands nothing.
	handOff bool
	// gate is the `gate` argument of a merge call. A call with no gate has none.
	gate starlark.Value
}

// mergeCall merges the members of one gated union in order, with the gate that an earlier call of the run recorded on that union.
func (s *script) mergeCall(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	call, err := s.batchCall(b, args, kwargs)
	if err != nil {
		return nil, err
	}

	if err := s.trustKept(call); err != nil {
		return nil, err
	}

	return s.mergeUnion(call)
}

// itemGit makes the worktree of the item, then returns git over it.
func (s *script) itemGit(ctx context.Context) (landing.GitCLI, error) {
	dir, err := s.tree(ctx)
	if err != nil {
		return landing.GitCLI{}, fmt.Errorf("find the folder of item %s: %w", s.in.Item, err)
	}

	if dir == "" {
		return landing.GitCLI{}, fmt.Errorf("item %s of project %s has no folder for the union of merge", s.in.Item, s.in.Project)
	}

	return s.e.deps.Batch.Git(ctx, s.in.Project, s.in.Item)
}

// batchCall unpacks and checks the arguments of a merge call. A keyword of the batch of the library is refused with the name of batch_merge,
// also when a splat of a dict brings it.
func (s *script) batchCall(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (batchCall, error) {
	var call batchCall

	var members, own, forbid starlark.Value

	if err := refuseBatchKeywords(kwargs); err != nil {
		return call, err
	}

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{
		membersKeyword: &members, baseKeyword: &call.settings.Base, repoKeyword: &call.settings.Repo, holdKeyword: &call.settings.Hold, ownKeyword: &own,
		"forbid_text": &forbid, nameKeyword: &call.name, kindGate: &call.gate,
	}); err != nil {
		return call, err
	}

	if err := s.batchScope(call.name); err != nil {
		return call, err
	}

	return call, call.fillUnion(members, forbid, own)
}

// refuseBatchKeywords fails on the first keyword of the batch of the library, with the name of batch_merge.
func refuseBatchKeywords(kwargs []starlark.Tuple) error {
	for _, pair := range kwargs {
		if key, isText := pair[0].(starlark.String); isText && slices.Contains(workflow.BatchKeywords(), key.GoString()) {
			return errors.New(workflow.MergeBatchRefusal(key.GoString()))
		}
	}

	return nil
}

// membersKeyword is the keyword of merge that lists the members of one gated union.
const membersKeyword = "members"

// given is true for an argument that the call passed and did not leave as None.
func given(v starlark.Value) bool {
	return v != nil && v != starlark.None
}

// fillUnion checks the settings of a merge call and reads the members.
func (c *batchCall) fillUnion(members, forbid, own starlark.Value) (err error) {
	if members == nil {
		return errors.New("merge needs `members`: the members of the gated union, one struct(item = ..., head = ..., closes = [...]) for each, in the order to land them")
	}

	if err = errors.Join(workflow.NotEmpty("base", c.settings.Base), workflow.NotEmpty("repo", c.settings.Repo)); err != nil {
		return err
	}

	if c.members, err = workflow.Members(members); err != nil {
		return err
	}

	return c.settings.read(forbid, own)
}

// fillHandOff checks the settings of a hand_off call and reads its pull requests.
func (c *batchCall) fillHandOff(prs starlark.Value) (err error) {
	if err = errors.Join(workflow.NotEmpty("base", c.settings.Base), workflow.NotEmpty("repo", c.settings.Repo)); err != nil {
		return err
	}

	if c.members, err = workflow.PullRequests(prs); err != nil {
		return err
	}

	return c.settings.read(nil, nil)
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

// textList turns lines of text into a Starlark list of strings.
func textList(lines []string) *starlark.List {
	out := make([]starlark.Value, len(lines))
	for i, line := range lines {
		out[i] = starlark.String(line)
	}

	return starlark.NewList(out)
}

// outcomes turns members with a reason into a Starlark list of struct(item, reason).
func outcomes(list []landing.Outcome) *starlark.List {
	out := make([]starlark.Value, len(list))
	for i, o := range list {
		out[i] = starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{itemName: starlark.String(o.Item), reasonKey: starlark.String(o.Reason)})
	}

	return starlark.NewList(out)
}

// stepRetries is the number of times DBOS runs a step of a merge again after it returned an error, before it records the error.
// DBOS records a step that ends in an error, and a replay returns that record without running the step. A transient failure of a git
// call or the forge would then end the run for good, so the step retries first and records only a failure that stays.
const stepRetries = 2

// everyStepIsAPass is true when the gate recorded at least one step and every step passed. An unknown step, or no step, is not a pass.
func everyStepIsAPass(steps []GateStep) bool {
	for _, s := range steps {
		if s.Status != passStatus {
			return false
		}
	}

	return len(steps) > 0
}

// memberStatus is how the forge answered the merge of one member.
type memberStatus int

const (
	// memberMerged means the member is on the base.
	memberMerged memberStatus = iota
	// memberRefused means a guard of the member refused the merge for good: its own work, a hold, its checks or its review.
	memberRefused
	// memberUnsettled means the forge could not answer yet, for example when mergeable stayed unknown after three reads.
	memberUnsettled
)

// memberOutcome is the answer of the merge of one member. Reason is set for a refusal or an unsettled answer.
type memberOutcome struct {
	Status memberStatus
	Reason string
}

// batchBase reads the base tip and merges the members of a merge call.
type batchBase struct {
	s    *script
	call batchCall
}

// tip reads the base tip of the call on the forge.
func (b *batchBase) tip(ctx context.Context) (landing.Tip, error) {
	return b.s.e.deps.Batch.Tip(ctx, b.s.in.Project, b.s.in.Item, b.call.settings.Base)
}

// mergeMember records the intent of the member, then merges through merge.Run with that intent. The intent step comes first, so a merge that the forge
// made before a crash counts as ours on the replay. A mergeable state that stays UNKNOWN is read again after a durable sleep.
// A non-empty expect is the tree that the base tip must have: the intent step of a tip with another tree ends the member as unsettled with no merge.
// The statuses go to the merge run in its settings, and the run posts them after its guards and right before its merge call.
func (b *batchBase) mergeMember(ctx context.Context, m workflow.PullRequest, expect, prefixTree string, statuses []model.CommitStatus) (memberOutcome, error) {
	read, err := b.recordedIntent(m, expect)
	if err != nil {
		return memberOutcome{}, err
	}

	if out, stopped := read.stop(); stopped {
		return out, nil
	}

	return b.mergeReads(ctx, m, b.memberSettings(m, prefixTree, &read.Intent, statuses))
}

// recordedIntent is the intent step of the member.
func (b *batchBase) recordedIntent(m workflow.PullRequest, expect string) (intentRead, error) {
	return runStep(b.s.e, b.s.ctx, b.stepName("intent", m.Item), func(ctx context.Context) (intentRead, error) {
		return b.intent(ctx, m, expect)
	}, dbos.WithStepMaxRetries(stepRetries))
}

// memberSettings are the settings of the merge of one member. The approval, the tree, the intent, the kept issues, the closed issues and the statuses are facts about this
// member only, so the call settings never carry them.
func (b *batchBase) memberSettings(m workflow.PullRequest, prefixTree string, intent *merge.Intent, statuses []model.CommitStatus) MergeSettings {
	settings := b.call.settings
	settings.GatedTree, settings.Intent, settings.Approved, settings.Kept, settings.Closing = prefixTree, intent, m.Approved, m.Kept, m.Closing
	settings.OwnDraft, settings.Statuses, settings.Closes = m.OwnDraft, statuses, m.Closes

	return settings
}

// mergeReads merges the member and reads again after a durable sleep while the mergeable state stays UNKNOWN, up to unknownReads reads.
func (b *batchBase) mergeReads(ctx context.Context, m workflow.PullRequest, settings MergeSettings) (memberOutcome, error) {
	for read := 1; ; read++ {
		got, err := b.mergeRead(ctx, m, settings)
		if err != nil {
			return memberOutcome{}, err
		}

		out, unsettled, err := verdictOf(got)
		if err != nil || !unsettled {
			return out, err
		}

		if read == unknownReads {
			return memberOutcome{Status: memberUnsettled, Reason: fmt.Sprintf("%s after %d reads of %s", out.Reason, read, m.Item)}, nil
		}

		if _, err := dbos.Sleep(b.s.ctx, cmp.Or(b.s.e.deps.Clock.Settle, settleEvery)); err != nil {
			return memberOutcome{}, err
		}
	}
}

// mergeRead runs one merge of the member as a child run on the exclusive queue.
func (b *batchBase) mergeRead(ctx context.Context, m workflow.PullRequest, settings MergeSettings) (merge.Result, error) {
	ran, err := b.s.e.queueExclusive(ctx, ExclusiveInput{Kind: kindMerge, Run: workflowID(b.s.ctx), RunItem: b.s.in.Item, Project: b.s.in.Project, Item: m.Item, Head: m.Head, Settings: settings})
	if err != nil {
		return ran.Merge, err
	}

	return b.s.e.awaitQueued(b.s.ctx, b.stepName("poll", m.Item), queuedRead{project: b.s.in.Project, item: m.Item, head: m.Head, settings: settings}, ran.Merge) //nolint:contextcheck // the batch session holds the context of the run in a field, and every step of the file uses it
}

// intentRead is the record of the intent step: the intent, the reason the member cannot be merged because its pull request is not OPEN,
// or Moved when the base tip has another tree than the members before it make.
type intentRead struct {
	Intent  merge.Intent
	Refused string
	Moved   bool `json:",omitempty"`
}

// stop is the outcome of a member whose intent step ended it: a pull request that is not OPEN is refused, and a tip that moved is unsettled.
func (r intentRead) stop() (out memberOutcome, stopped bool) {
	switch {
	case r.Refused != "":
		return memberOutcome{Status: memberRefused, Reason: r.Refused}, true
	case r.Moved:
		return memberOutcome{Status: memberUnsettled, Reason: reasonMoved}, true
	}

	return out, false
}

// intent reads the base tip, then the pull request of the member against that tip. A pull request that is not OPEN is a refusal of the member,
// recorded as a result: DBOS keeps the text of a recorded error and not its type, so an error would end the run for good.
// A tip whose tree is not a non-empty expect is recorded as moved, and the pull request is not read.
func (b *batchBase) intent(ctx context.Context, m workflow.PullRequest, expect string) (intentRead, error) {
	tip, err := b.tip(ctx)
	if err != nil {
		return intentRead{}, err
	}

	if expect != "" && tip.Tree != expect {
		return intentRead{Moved: true}, nil
	}

	intent, err := b.s.e.deps.Batch.Intent(ctx, b.s.in.Project, m.Item, b.call.settings.Repo, b.call.settings.Base, tip.Tree, b.s.e.deps.Clock.now())
	if errors.Is(err, ErrMemberNotOpen) {
		return intentRead{Refused: err.Error()}, nil
	}

	return intentRead{Intent: intent}, err
}

// stepName names a step of one member: merge:<name>.<kind>#<item>.
func (b *batchBase) stepName(kind, item string) string {
	return fmt.Sprintf("%s:%s.%s#%s", kindMerge, b.call.name, kind, item)
}

// verdictOf maps the result of merge.Run to the answer of a member. A refusal of the member rejects it, and a transient one defers it, with every line
// of the refusal as the reason. A mergeable state that is UNKNOWN is unsettled, and the caller reads again. A stop on the tree proof is an error.
func verdictOf(got merge.Result) (out memberOutcome, unsettled bool, err error) {
	if got.Stop == merge.Done {
		return memberOutcome{Status: memberMerged}, false, nil
	}

	reason := strings.Join(got.Refusal, "\n")

	switch got.Reason {
	case merge.TreeProof:
		return out, false, fmt.Errorf("%w: %s", errMergeRefused, strings.Join(got.Lines, "; "))
	case merge.Unsettled:
		return memberOutcome{Reason: "mergeable is UNKNOWN"}, true, nil
	case merge.Transient:
		// A transient refusal defers the member without a second read. The merge call may have reached the forge, and a second merge step is never run in one attempt.
		return memberOutcome{Status: memberUnsettled, Reason: reason}, false, nil
	case merge.Plain, merge.Queued:
	}

	return memberOutcome{Status: memberRefused, Reason: reason}, false, nil
}
