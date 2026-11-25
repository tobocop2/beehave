package flow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/tobocop2/beehave/internal/batch"
	"github.com/tobocop2/beehave/internal/landing"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/workflow"
)

// gateRecord is what a run keeps of a gate it recorded: the verdict, the head and the tree that the gate ran on, and its steps.
type gateRecord struct {
	Passed bool       `json:"passed"`
	Head   string     `json:"head"`
	Tree   string     `json:"tree"`
	Steps  []GateStep `json:"steps"`
}

// decodeGate reads the record of a gate from the outputs of its step.
func decodeGate(outputs map[string]any) (record gateRecord, err error) {
	data, err := json.Marshal(outputs)
	if err != nil {
		return record, err
	}

	return record, json.Unmarshal(data, &record)
}

// keepGate remembers the gate that a call recorded, by its step name, so a merge can check a gate value against it. A replay of the call keeps it again.
// A record that does not decode is not kept, so a merge that names its step finds no gate and refuses: the trust fails closed.
func (s *script) keepGate(outputs map[string]any) {
	step, isText := outputs[stepField].(string)
	if !isText || step == "" {
		return
	}

	record, err := decodeGate(outputs)
	if err != nil {
		return
	}

	if s.gates == nil {
		s.gates = map[string]gateRecord{}
	}

	s.gates[step] = record
}

// trustedGate returns the record of the gate that the `gate` value of a merge call names. Only a gate that this run recorded counts, so a value that a workflow made up has no record.
// The merge reads the verdict and the tree from the record of the run and never from the value, so a value that carries other fields changes nothing.
// The gate must have passed with every step a pass, on a tree that it read.
func (s *script) trustedGate(gate starlark.Value) (gateRecord, error) {
	step, err := gateStepOf(gate)
	if err != nil {
		return gateRecord{}, err
	}

	record, recorded := s.gates[step]
	if !recorded {
		return gateRecord{}, fmt.Errorf("`gate` names the step %s, and no gate call of this run recorded it. Pass the value that gate() returned in this run", step)
	}

	if !record.Passed || !everyStepIsAPass(record.Steps) {
		return gateRecord{}, fmt.Errorf("the gate of the step %s did not pass, so the merge posts no status and merges nothing", step)
	}

	if record.Tree == "" {
		return gateRecord{}, fmt.Errorf("the gate of the step %s recorded no tree, so no union can be compared with it. Gate the union in the worktree of the item", step)
	}

	return record, nil
}

// gateStepOf reads the step name that a gate value carries in its field step.
func gateStepOf(gate starlark.Value) (string, error) {
	result, isStruct := gate.(*starlarkstruct.Struct)
	if !isStruct {
		return "", fmt.Errorf("`gate` must be the result of a gate call, not %s. Pass the value that gate() returned", gate.Type())
	}

	field, err := result.Attr(stepField)
	step, isText := starlark.AsString(field)

	if err != nil || !isText || step == "" {
		return "", errors.New("`gate` is not the result of a gate call. Pass the value that gate() returned")
	}

	return step, nil
}

// statuses lists the statuses of a member of a union whose gate passed: beehave/gate on its head, and beehave/review on its head when a review approved it.
// A status stands for the head it names, so a later push to the member leaves its new head with none. A call with no gate has no record and posts none.
func (r *gateRecord) statuses(m workflow.PullRequest) []model.CommitStatus {
	if r == nil {
		return nil
	}

	var statuses []model.CommitStatus
	if m.Approved {
		statuses = append(statuses, model.CommitStatus{SHA: m.Head, Context: model.ContextReview, State: model.StatusSuccess, Description: "The review approved this head."})
	}

	return append(statuses, model.CommitStatus{SHA: m.Head, Context: model.ContextGate, State: model.StatusSuccess, Description: "The gate passed on the union commit " + r.Head + "."})
}

// unionPlan is the record of the plan step of a union: the base tip that the fold started on, the tree after each member in order, and why the fold stopped, if it did.
type unionPlan struct {
	Start   landing.Tip `json:"start"`
	Trees   []string    `json:"trees"`
	Refused string      `json:"refused,omitempty"`
}

// expect is the tree that the base tip must have before member k merges: the tree of the start tip, then the tree after the member before it.
// A call with no gate has no plan and expects no tree.
func (p *unionPlan) expect(k int) string {
	switch {
	case p == nil:
		return ""
	case k == 0:
		return p.Start.Tree
	}

	return p.Trees[k-1]
}

// proof is the tree that the base must have after member k merged. A call with no gate has no plan and proves no tree.
func (p *unionPlan) proof(k int) string {
	if p == nil {
		return ""
	}

	return p.Trees[k]
}

// refusal says why the union of the plan is not the union that the gate ran on, or gives an empty text.
func (p *unionPlan) refusal(gateTree string) string {
	if p.Refused != "" {
		return p.Refused
	}

	made := p.Trees[len(p.Trees)-1]
	if made == gateTree {
		return ""
	}

	return fmt.Sprintf("the members on the base tip %s make the tree %s, and the gate ran on %s. Gate the union of these members on this tip", p.Start.Commit, made, gateTree)
}

// unionOutcome collects how the members of a union ended. rest holds the members after a refused one, which the call did not ask the forge to merge.
// moved is true when the members on the base tip do not make the tree that the gate ran on, and reason then says why.
type unionOutcome struct {
	merged             []string
	rejected, deferred []landing.Outcome
	rest               []string
	moved              bool
	reason             string
}

// add keeps the end of a member and reports whether the next member may merge. A refusal stops the loop and returns the members after it in rest.
// An unsettled answer defers the member and every member after it.
func (o *unionOutcome) add(m workflow.PullRequest, got batch.MergeOutcome, after []workflow.PullRequest) bool {
	switch got.Status {
	case batch.StatusMerged:
		o.merged = append(o.merged, m.Item)

		return true
	case batch.StatusRefused:
		o.rejected = append(o.rejected, landing.Outcome{Item: m.Item, Reason: got.Reason})

		for _, next := range after {
			o.rest = append(o.rest, next.Item)
		}
	case batch.StatusUnsettled:
		for _, left := range append([]workflow.PullRequest{m}, after...) {
			o.deferred = append(o.deferred, landing.Outcome{Item: left.Item, Reason: got.Reason})
		}
	}

	return false
}

// mergeUnion merges the members of a call with `members` in order. With a gate, the engine first folds the member heads onto the base tip and merges nothing
// unless that fold makes the tree that the gate ran on. Each member then merges only on the tip that the members before it made, and proves the tree after it.
// A gate that this run did not record, or one that did not pass, ends the call before any step.
func (s *script) mergeUnion(call batchCall) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	base := &batchBase{s: s, call: call, prefix: kindMerge}

	out, err := s.landUnion(call, base)
	if err != nil {
		return nil, fmt.Errorf("merge %q: %w", call.name, err)
	}

	tip, err := runStep(s.e, s.ctx, kindMerge+":"+call.name+".tip", base.tip, dbos.WithStepMaxRetries(stepRetries))
	if err != nil {
		return nil, fmt.Errorf("merge %q: %w", call.name, err)
	}

	return unionResult(out, tip), nil
}

// landUnion checks the gate and the fold of a union, then merges its members in order. A call with no gate merges them with no status and no tree proof.
func (s *script) landUnion(call batchCall, base *batchBase) (unionOutcome, error) {
	if !given(call.gate) {
		return s.mergeInOrder(call, base, nil, nil)
	}

	record, err := s.trustedGate(call.gate)
	if err != nil {
		return unionOutcome{}, err
	}

	plan, err := s.planUnion(call, base)
	if err != nil {
		return unionOutcome{}, err
	}

	if reason := plan.refusal(record.Tree); reason != "" {
		return unionOutcome{moved: true, reason: reason}, nil
	}

	return s.mergeInOrder(call, base, &plan, &record)
}

// planUnion folds the member heads onto the base tip in the recorded step merge:<call>.plan, so a replay compares the same fold.
func (s *script) planUnion(call batchCall, base *batchBase) (unionPlan, error) {
	return runStep(s.e, s.ctx, kindMerge+":"+call.name+".plan", func(ctx context.Context) (unionPlan, error) {
		return s.foldUnion(ctx, base)
	}, dbos.WithStepMaxRetries(stepRetries))
}

// mergeInOrder merges each member on the tip that the plan expects, until one does not merge.
func (s *script) mergeInOrder(call batchCall, base *batchBase, plan *unionPlan, record *gateRecord) (unionOutcome, error) {
	var out unionOutcome

	for k, m := range call.members {
		got, err := base.mergeMember(s.ctx, m, plan.expect(k), plan.proof(k), record.statuses(m))
		if err != nil {
			return out, fmt.Errorf("merge %s: %w", m.Item, err)
		}

		if !out.add(m, got, call.members[k+1:]) {
			break
		}
	}

	return out, nil
}

// foldUnion reads the base tip and folds the member heads onto it in order with git, in the worktree of the item. It compares trees only, so the commits
// that a workflow made for the checkout of its gate do not matter.
func (s *script) foldUnion(ctx context.Context, base *batchBase) (unionPlan, error) {
	git, err := s.itemGit(ctx)
	if err != nil {
		return unionPlan{}, err
	}

	if err := landing.CheckGit(ctx, git); err != nil {
		return unionPlan{}, err
	}

	tip, err := base.tip(ctx)
	if err != nil {
		return unionPlan{}, err
	}

	return foldMembers(ctx, git, tip, base.call.members)
}

// foldMembers folds the heads of the members onto the tip in order. A head that the repository does not hold, or that conflicts, stops the fold with the reason.
func foldMembers(ctx context.Context, git landing.GitCLI, tip landing.Tip, members []workflow.PullRequest) (unionPlan, error) {
	plan, prev := unionPlan{Start: tip, Trees: []string{}}, tip.Commit

	for _, m := range members {
		next, err := foldOne(ctx, git, prev, m)
		if err != nil || next.refused != "" {
			plan.Refused = next.refused

			return plan, err
		}

		prev, plan.Trees = next.commit, append(plan.Trees, next.tree)
	}

	return plan, nil
}

// folded is one member on top of the members before it: the commit and tree of the fold, or the reason that it does not fold.
type folded struct {
	commit, tree, refused string
}

// foldOne merges the head of the member into prev without a working copy and makes the commit of that merge.
func foldOne(ctx context.Context, git landing.GitCLI, prev string, m workflow.PullRequest) (folded, error) {
	found, err := git.HasCommit(ctx, m.Head)
	if err != nil || !found {
		return folded{refused: fmt.Sprintf("head %s of item %s is not in the repository; fetch it, then gate the union again", m.Head, m.Item)}, err
	}

	made, err := git.MergeTree(ctx, prev, m.Head)
	if err != nil || made.Conflict {
		return folded{refused: fmt.Sprintf("item %s conflicts with the base tip and the members before it, so no gate ran on this union. Gate a union of members that merge cleanly", m.Item)}, err
	}

	commit, err := git.NewCommit(ctx, made.Tree, prev, m.Head)

	return folded{commit: commit, tree: made.Tree}, err
}

// unionResult is the struct of a merge of a union: the merged items, the rejected and deferred members with their reason, the members after a refusal,
// whether the fold was not the gated union and why, and the base tip after the call.
func unionResult(out unionOutcome, tip landing.Tip) starlark.Value { //nolint:ireturn // the builtin contract of starlark-go
	return starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{
		mergedField: textList(out.merged), rejectedField: outcomes(out.rejected), deferredField: outcomes(out.deferred), restField: textList(out.rest),
		movedField: starlark.Bool(out.moved), reasonKey: starlark.String(out.reason),
		tipField: starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{commitField: starlark.String(tip.Commit), treeName: starlark.String(tip.Tree)}),
	})
}

// The result fields of a merge and a hand-off: the members that landed, were refused or were deferred, the members after a refusal, the base tip after
// the call, the note of the audit and the members that moved.
const (
	mergedField   = "merged"
	rejectedField = "rejected"
	deferredField = "deferred"
	restField     = "rest"
	tipField      = "tip"
	noteField     = "note"
	movedField    = "moved"
)

// handOffKeyword is the name of the builtin that hands the landing to the owner.
const handOffKeyword = "hand_off"

// handOffCall marks the pull requests of `prs` ready and watches them until the owner merges them. It lands nothing.
func (s *script) handOffCall(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	call := batchCall{handOff: true}

	var prs starlark.Value

	set := &call.settings

	err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{"prs": &prs, baseKeyword: &set.Base, repoKeyword: &set.Repo, nameKeyword: &call.name})
	if err != nil {
		return nil, err
	}

	if err := s.batchScope(call.name); err != nil {
		return nil, err
	}

	if err := call.fill(prs, nil, nil, nil); err != nil {
		return nil, err
	}

	return s.handOff(call)
}
