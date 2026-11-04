package batch

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

const (
	reasonBase = "base"
	// halves is the number of parts a red set splits into. The first half takes the extra member of an odd set, so the split tree is as shallow as log2 N.
	halves = 2
)

// runner is the state of one batch call. A batch call runs one gate at a time, so the runner has no lock.
type runner struct {
	deps    Deps
	tip     Tip
	counts  map[string]int
	red     map[string]string
	with    map[string]string
	merged  map[string]bool
	decided map[string]bool
	held    []Held
	retry   bool
	stopped bool
	// stopReason is why the members not decided are deferred.
	stopReason string
	result     Result
}

func newRunner(deps Deps) *runner {
	return &runner{
		deps:    deps,
		counts:  map[string]int{},
		red:     map[string]string{},
		with:    map[string]string{},
		merged:  map[string]bool{},
		decided: map[string]bool{},
	}
}

func (r *runner) stepName(kind string) string {
	r.counts[kind]++

	return fmt.Sprintf("batch:%s.%s#%d", r.deps.Name, kind, r.counts[kind])
}

// run lands the members. After an error the result still holds what landed and what was rejected.
func (r *runner) run(ctx context.Context, sorted []Member) (Result, error) {
	if err := r.runAll(ctx, sorted); err != nil {
		r.result.Tip = r.tip

		return r.result, err
	}

	return r.finish(sorted), nil
}

// runAll rejects the heads that are not in the repository, reads the tip, lands the first round, and retries the held-out members once.
func (r *runner) runAll(ctx context.Context, sorted []Member) error {
	present, err := r.checkHeads(ctx, sorted)
	if err != nil {
		return err
	}

	start, err := r.readTip(ctx)
	if err != nil {
		return err
	}

	r.result.Start = start

	if err := r.land(ctx, present); err != nil {
		return err
	}

	return r.retryHeld(ctx)
}

// checkHeads is a recorded step, because the answer depends on the repository. It rejects each member whose head is not there.
func (r *runner) checkHeads(ctx context.Context, sorted []Member) ([]Member, error) {
	var missing []string

	err := step(ctx, r.deps.Steps, r.stepName("heads"), sorted, func() ([]string, error) {
		return r.missingHeads(ctx, sorted)
	}, &missing)
	if err != nil {
		return nil, err
	}

	present := make([]Member, 0, len(sorted))

	for _, m := range sorted {
		if slices.Contains(missing, m.Item) {
			r.reject(m.Item, fmt.Sprintf("head %s of %s is not in the repository; fetch it", m.Head, m.Item), "")
		} else {
			present = append(present, m)
		}
	}

	return present, nil
}

// readTip reads the base tip as a recorded step, so a replay never asks the forge for a decision.
func (r *runner) readTip(ctx context.Context) (Tip, error) {
	var tip Tip

	err := step(ctx, r.deps.Steps, r.stepName("tip"), struct{}{}, func() (Tip, error) {
		return r.deps.Base.Tip(ctx)
	}, &tip)
	if err != nil {
		return Tip{}, err
	}

	r.tip = tip

	return tip, nil
}

// missingHeads lists the items whose head commit the repository does not hold.
func (r *runner) missingHeads(ctx context.Context, sorted []Member) ([]string, error) {
	var gone []string

	for _, m := range sorted {
		found, err := r.deps.Git.HasCommit(ctx, m.Head)
		if err != nil {
			return nil, err
		}

		if !found {
			gone = append(gone, m.Item)
		}
	}

	return gone, nil
}

type unionInput struct {
	TipCommit string   `json:"tipCommit"`
	TipTree   string   `json:"tipTree"`
	Members   []Member `json:"members"`
}

func (r *runner) union(ctx context.Context, set []Member) (Union, error) {
	var built Union

	in := unionInput{TipCommit: r.tip.Commit, TipTree: r.tip.Tree, Members: set}

	err := step(ctx, r.deps.Steps, r.stepName("union"), in, func() (Union, error) {
		return BuildUnion(ctx, r.deps.Git, r.tip, set)
	}, &built)

	return built, err
}

type gateInput struct {
	Tree    string   `json:"tree"`
	Members []Member `json:"members"`
}

// verdict gates a union, unless a union with the same tree id was gated red before.
// The gate output must carry the tree id of the union: a recorded output with another tree id is an error, never a pass.
func (r *runner) verdict(ctx context.Context, u Union) (GateOutcome, error) {
	if report, known := r.red[u.Tree]; known {
		return GateOutcome{Tree: u.Tree, Report: report}, nil
	}

	r.result.Gates++

	out, err := r.gate(ctx, u)
	if err != nil {
		return GateOutcome{}, err
	}

	if !out.Passed {
		r.red[u.Tree] = out.Report
	}

	return out, nil
}

func (r *runner) gate(ctx context.Context, u Union) (GateOutcome, error) {
	var out GateOutcome

	in := gateInput{Tree: u.Tree, Members: u.Included}

	err := step(ctx, r.deps.Steps, r.stepName("gate"), in, func() (GateOutcome, error) {
		return r.deps.Gate.Run(ctx, GateInput{Commit: u.Commit, Tree: u.Tree})
	}, &out)
	if err != nil {
		return GateOutcome{}, err
	}

	if out.Tree != u.Tree {
		return GateOutcome{}, fmt.Errorf("the recorded gate ran on tree %s, and the union is tree %s", out.Tree, u.Tree)
	}

	return out, nil
}

// land gates a set and merges it, or splits it in halves when the gate is red.
func (r *runner) land(ctx context.Context, set []Member) error {
	if r.stopped || len(set) == 0 {
		return nil
	}

	u, err := r.union(ctx, set)
	if err != nil {
		return err
	}

	r.sort(u)

	if len(u.Included) == 0 {
		return nil
	}

	return r.gateAndLand(ctx, u)
}

func (r *runner) gateAndLand(ctx context.Context, u Union) error {
	out, err := r.verdict(ctx, u)
	if err != nil {
		return err
	}

	if out.Void != "" {
		return fmt.Errorf("%w: %s. No member is blamed. Fix the cause and run the batch again", ErrGateVoid, strings.TrimRight(strings.TrimSpace(out.Void), "."))
	}

	if out.Passed {
		return r.mergeAll(ctx, u)
	}

	return r.split(ctx, u.Included, out.Report)
}

func (r *runner) split(ctx context.Context, set []Member, report string) error {
	if len(set) == 1 {
		r.reject(set[0].Item, r.redReason(), report)

		return nil
	}

	half := (len(set) + 1) / halves

	if err := r.land(ctx, set[:half]); err != nil {
		return err
	}

	return r.land(ctx, set[half:])
}

func (r *runner) redReason() string {
	if len(r.result.Merged) > 0 {
		return ReasonRedOnTop
	}

	return ReasonRed
}

// mergeAll lands the members of a green union in order. Before each merge it reads the tip and requires the tree of the members merged so far.
// A tip with another tree means someone else pushed, so the rest is deferred.
// A member the forge refuses is rejected, and the members after it are gated again on the tip that the refusal left.
// An unsettled answer stops the call.
func (r *runner) mergeAll(ctx context.Context, u Union) error {
	expected := r.tip.Tree

	for i, m := range u.Included {
		then, err := r.mergeOne(ctx, m, expected, u.Prefix[i])
		if err != nil {
			return err
		}

		if then != nextMember {
			return r.afterMember(ctx, then, u.Included[i+1:])
		}

		expected = u.Prefix[i]
	}

	_, err := r.readTip(ctx)

	return err
}

// afterMember ends the loop of mergeAll: a stop ends the batch, and a refusal gates the rest again.
func (r *runner) afterMember(ctx context.Context, then next, rest []Member) error {
	if then == nextRegate {
		return r.land(ctx, rest)
	}

	return nil
}

// next says what mergeAll does after one member.
type next int

const (
	nextMember next = iota
	nextRegate
	nextStop
)

// mergeOne merges a member when the tip holds the expected tree.
// A tip with another tree stops the call without a merge, and so does an unsettled answer of the forge.
func (r *runner) mergeOne(ctx context.Context, m Member, expected, prefix string) (next, error) {
	tip, err := r.readTip(ctx)
	if err != nil {
		return nextStop, fmt.Errorf("before merging %s: %w", m.Item, err)
	}

	if tip.Tree != expected {
		r.stop(ReasonMoved)

		return nextStop, nil
	}

	out, err := r.deps.Base.Merge(ctx, m, prefix)
	if err != nil {
		return nextStop, fmt.Errorf("merge %s: %w", m.Item, err)
	}

	return r.answer(m, out)
}

// answer acts on the forge's answer for one member.
func (r *runner) answer(m Member, out MergeOutcome) (next, error) {
	switch out.Status {
	case StatusMerged:
		r.merged[m.Item], r.decided[m.Item] = true, true
		r.result.Merged = append(r.result.Merged, m.Item)

		return nextMember, nil
	case StatusRefused:
		r.reject(m.Item, out.Reason, "")

		return nextRegate, nil
	case StatusUnsettled:
		r.stop(out.Reason)

		return nextStop, nil
	}

	return nextStop, fmt.Errorf("merge %s: the forge answered with the unknown status %d", m.Item, out.Status)
}

// stop ends the batch. The members not decided so far are deferred with the reason.
func (r *runner) stop(reason string) {
	r.stopped, r.stopReason = true, reason
}

func (r *runner) reject(item, reason, report string) {
	r.decided[item] = true
	r.result.Rejected = append(r.result.Rejected, Outcome{Item: item, Reason: reason, Report: report})
}

// sort routes the members a union left out. In the first round a conflict with the base is rejected and a conflict with an earlier member is held for the retry.
// In the retry pass no member is held again.
func (r *runner) sort(u Union) {
	for _, m := range u.Conflicts {
		r.reject(m.Item, r.conflictReason(m), "")
	}

	for _, h := range u.Held {
		if r.retry {
			r.reject(h.Member.Item, "conflicts with "+h.With, "")

			continue
		}

		r.with[h.Member.Item] = h.With
		r.held = append(r.held, h)
	}
}

// conflictReason names the member to blame for a conflict with the tip. In the retry pass the blamed member is the earlier member that landed, or the base.
func (r *runner) conflictReason(m Member) string {
	if !r.retry {
		return ReasonConflict
	}

	blame := r.with[m.Item]
	if !r.merged[blame] {
		blame = reasonBase
	}

	return "conflicts with " + blame
}

// retryHeld folds the held-out members once on the new tip and lands them with the same bisect. There is no second retry.
func (r *runner) retryHeld(ctx context.Context) error {
	if r.stopped || len(r.held) == 0 {
		return nil
	}

	r.retry = true

	slices.SortFunc(r.held, func(a, b Held) int { return strings.Compare(a.Member.Item, b.Member.Item) })

	members := make([]Member, 0, len(r.held))
	for _, h := range r.held {
		members = append(members, h.Member)
	}

	r.held = nil

	return r.land(ctx, members)
}

func (r *runner) finish(sorted []Member) Result {
	for _, m := range sorted {
		if !r.decided[m.Item] {
			r.result.Deferred = append(r.result.Deferred, Outcome{Item: m.Item, Reason: r.stopReason})
		}
	}

	r.result.Tip = r.tip

	return r.result
}
