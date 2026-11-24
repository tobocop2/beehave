package flow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/beehave/internal/batch"
	forgepkg "github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/model"
)

// scopeStep is the recorded step that keeps the scope of a pull request. The first gate, review read or merge call of an item fixes the scope. A merge checks it for each member.
const scopeStep = "scope"

// keptStep is the kind of the recorded step that keeps an owner line: the issues that the owner kept for one member.
const keptStep = "kept"

// errScopeRefused ends an item whose pull request closes an issue that is new since the first review and has no owner line.
var errScopeRefused = errors.New("the pull request closes an issue that is new since its first review")

// issueRefText is one issue in an owner line: a number with a leading #, or a full reference owner/repo#N for an issue of another repository.
const issueRefText = `(?:[a-z0-9_.-]+/[a-z0-9_.-]+)?#\d+`

// keepForm is the one form of an owner line: the word keep, then issues, separated by commas.
var keepForm = regexp.MustCompile(`(?i)^\s*keep\s+` + issueRefText + `(?:\s*,\s*` + issueRefText + `)*\s*$`)

// issueRef finds one issue in an owner line.
var issueRef = regexp.MustCompile(`(?i)` + issueRefText)

// scopeRead is what one step of the scope records: the closing issues of the pull request, or Found false when the item has no pull request yet.
// An issue is "#N" for the repository of the pull request and "owner/repo#N" for another one. A scope that an earlier run recorded as numbers still reads.
// NoForge is true when the project has no forge, so there is no scope to fix and the scope check is off.
type scopeRead struct {
	Found   bool             `json:"found"`
	Issues  []model.IssueRef `json:"issues"`
	NoForge bool             `json:"no_forge,omitempty"`
}

// fixScope records the issues that the pull request of the item closes at the first review, which is the first gate, review read or merge call that finds a pull request.
// A later call of the run keeps that set and reads nothing. A replay reads the recorded set, so it holds the same scope.
// A project with no forge has no scope: the step records that the scope check is off, and no later call reads again.
func (s *script) fixScope(label string) error {
	if s.fixed || s.scopeOff {
		return nil
	}

	read, err := countedStep(s, scopeStep, label, s.firstRead)
	if err != nil {
		return fmt.Errorf("%s of item %s: %w", scopeStep, s.in.Item, err)
	}

	s.scopeOff = read.NoForge

	if read.Found {
		s.fixed, s.scope = true, read.Issues
	}

	return nil
}

// firstRead is the body of the scope step. A new run of the item (a restart after a cancel, a send-back round) takes the scope of the newest earlier run that fixed one,
// so the issues of the first review stay the baseline. The forge is read only when no earlier run fixed a scope.
func (s *script) firstRead(ctx context.Context) (scopeRead, error) {
	earlier, err := s.earlierScope(s.in.Item)
	if err != nil || earlier.Found {
		return earlier, err
	}

	read, err := s.readClosing(ctx, s.in.Item)
	if errors.Is(err, forgepkg.ErrNoForge) {
		return scopeRead{Issues: []model.IssueRef{}, NoForge: true}, nil
	}

	return read, err
}

// earlierScope returns the scope of the newest earlier run of the item that recorded one, or a read with Found false.
func (s *script) earlierScope(item string) (scopeRead, error) {
	ids, err := s.earlierRuns(item)
	if err != nil {
		return scopeRead{}, err
	}

	for _, id := range ids {
		if read, err := recordedScope(s.e.ctx, id); err != nil || read.Found {
			return read, err
		}
	}

	return scopeRead{}, nil
}

// earlierRuns lists the ids of the other runs of the item, the newest attempt first. It filters on no state and no attempt number, so a canceled, failed, live or newer run is in the list.
// A caller that counts work over these runs counts a concurrent run as spent: that errs on the safe side for a cap.
func (s *script) earlierRuns(item string) ([]string, error) {
	prefix := runPrefix + item + attemptMarker

	rows, err := s.e.store().List(dbos.WithFilterWorkflowIDPrefix(prefix), dbos.WithFilterName(itemName))
	if err != nil {
		return nil, fmt.Errorf("list the earlier runs of item %s: %w", item, err)
	}

	// The prefix of item 12 also matches a run of item 12-r2, whose tail is not a number: only a numbered id is an attempt of this item.
	attempts := map[string]int{}

	for _, row := range rows {
		if attempt, numbered := attemptOf(row.ID, prefix); numbered && row.ID != workflowID(s.ctx) {
			attempts[row.ID] = attempt
		}
	}

	ids := slices.Collect(maps.Keys(attempts))
	slices.SortFunc(ids, func(a, b string) int { return attempts[b] - attempts[a] })

	return ids, nil
}

// recordedScope returns the scope that the scope step of a run recorded. Found is false when the run recorded none: it never found a pull request.
func recordedScope(ctx dbos.Context, id string) (scopeRead, error) {
	steps, err := dbos.GetWorkflowSteps(ctx, id, dbos.WithStepsLoadOutput(true))
	if err != nil {
		return scopeRead{}, fmt.Errorf("read the steps of %s: %w", id, err)
	}

	for _, step := range steps {
		if read := scopeOutput(step); read.Found {
			return read, nil
		}
	}

	return scopeRead{}, nil
}

// scopeOutput returns what a scope step recorded, or a read with Found false for any other step and for a step that failed.
// The store keeps the output of a step as JSON text.
func scopeOutput(step dbos.StepInfo) scopeRead {
	text, isText := step.Output.(string)
	named := strings.HasPrefix(step.StepName, scopeStep+"#") || strings.HasPrefix(step.StepName, scopeStep+":")

	var read scopeRead
	if !isText || !named || step.Error != nil || json.Unmarshal([]byte(text), &read) != nil {
		return scopeRead{}
	}

	return read
}

// readClosing reads the closing issues of the pull request of the item, sorted and without a repeat.
func (s *script) readClosing(ctx context.Context, item string) (scopeRead, error) {
	issues, found, err := s.e.deps.Forge.ClosingIssues(ctx, s.in.Project, item)
	if err != nil {
		return scopeRead{}, fmt.Errorf("read the issues that the pull request of item %s closes: %w", item, err)
	}

	return scopeRead{Found: found, Issues: sortedIssues(issues)}, nil
}

// sortedIssues returns the issues in order, without a repeat. The result is never nil, so a recorded empty set stays a set.
func sortedIssues(issues []model.IssueRef) []model.IssueRef {
	out := append([]model.IssueRef{}, issues...)
	slices.SortFunc(out, model.CompareIssues)

	return slices.Compact(out)
}

// scopeCheckRead is the record of the scope step of a member: the issues that its pull request closes now, that are new since its first review, and that have no owner line.
type scopeCheckRead struct {
	Fresh []model.IssueRef `json:"fresh"`
	Then  []model.IssueRef `json:"then"`
	Kept  []model.IssueRef `json:"kept"`
}

// firstReview returns the issues of the first review of the item, and false when the item has none.
// The first review of the run's own item is the scope of its gate. The first review of another item is the scope of its newest earlier run.
func (s *script) firstReview(item string) (issues []model.IssueRef, known bool, err error) {
	if item == s.in.Item {
		return s.scope, s.fixed, nil
	}

	earlier, err := s.earlierScope(item)

	return earlier.Issues, earlier.Found, err
}

// freshIssues reads the closing issues of the pull request of the item and returns the ones that are new since the first review and have no owner line, with the issues of the first review.
// An item with no recorded scope has nothing to compare, so it reads nothing. A read that fails, or a pull request that is gone, is an error.
func (s *script) freshIssues(ctx context.Context, item string) (scopeCheckRead, error) {
	baseline, known, err := s.firstReview(item)
	if err != nil || !known {
		return scopeCheckRead{Fresh: []model.IssueRef{}, Then: []model.IssueRef{}}, err
	}

	read, err := s.readPull(ctx, item)
	if err != nil {
		return scopeCheckRead{}, err
	}

	kept, err := s.keptOf(item)
	if err != nil {
		return scopeCheckRead{}, err
	}

	fresh := slices.DeleteFunc(slices.Clone(read.Issues), func(issue model.IssueRef) bool {
		return slices.Contains(baseline, issue) || slices.Contains(kept, issue)
	})

	return scopeCheckRead{Fresh: fresh, Then: baseline, Kept: kept}, nil
}

// readPull reads the closing issues of the pull request of the item. A pull request that is gone is an error.
func (s *script) readPull(ctx context.Context, item string) (scopeRead, error) {
	read, err := s.readClosing(ctx, item)
	if err == nil && !read.Found {
		err = fmt.Errorf("read the issues that the pull request of item %s closes: the item has no pull request", item)
	}

	return read, err
}

// holdScope runs before the merge of a member. A new issue makes the run ask the owner, and the answer is the owner line: it must name each new issue.
// It returns the reason to refuse the member, or an empty text when the member may merge, with the issues that the owner kept for the member. The read is a recorded step of the member.
func (b *batchBase) holdScope(m batch.Member) (refusal string, kept []model.IssueRef, err error) {
	read, err := b.recordedFresh(m)
	if err != nil {
		return "", nil, fmt.Errorf("scope check of item %s: %w", m.Item, err)
	}

	if len(read.Fresh) == 0 {
		return "", read.Kept, nil
	}

	answer, err := b.s.waitOwner(b.scopeQuestion(m.Item, read.Fresh, read.Then))
	if err != nil {
		return "", nil, err
	}

	refusal, err = b.judge(m.Item, read.Fresh, answer)
	if refusal != "" || err != nil {
		return refusal, nil, err
	}

	return "", sortedIssues(append(slices.Clone(read.Kept), read.Fresh...)), nil
}

// judge reads the answer of the owner. When it keeps every new issue of the member, judge records them and returns no reason. Otherwise it returns the reason to reject the member.
func (b *batchBase) judge(item string, fresh []model.IssueRef, answer string) (string, error) {
	if missing := unnamed(fresh, answer); len(missing) > 0 {
		return b.scopeRefusal(item, missing), nil
	}

	return "", b.keep(item, fresh)
}

// unnamed returns the issues that the answer of the owner does not keep. An answer that is not the keep form keeps none.
func unnamed(fresh []model.IssueRef, answer string) []model.IssueRef {
	named, _ := keptIssues(answer)

	return slices.DeleteFunc(slices.Clone(fresh), func(issue model.IssueRef) bool { return slices.Contains(named, issue) })
}

// recordedFresh is the scope step of the member: the new issues of its pull request, recorded so a replay asks the forge nothing.
func (b *batchBase) recordedFresh(m batch.Member) (scopeCheckRead, error) {
	return runStep(b.s.e, b.s.ctx, b.stepName("scope", m.Item, 1), func(ctx context.Context) (scopeCheckRead, error) {
		return b.s.freshIssues(ctx, m.Item)
	}, dbos.WithStepMaxRetries(stepRetries))
}

// scopeQuestion is the question to the owner about the new issues of a member. It lists the issues of the first review and names the run that the answer goes to.
func (b *batchBase) scopeQuestion(item string, fresh, then []model.IssueRef) string {
	return fmt.Sprintf("The pull request of item %s closes %s now, and did not at its first review (then: %s). Answer in the form \"keep #N[, #N ...]\", for example \"keep %s\". Any other answer rejects item %s from this merge. The other members still land. Answer with beehave answer %s.",
		item, issueList(fresh), issueList(then), keepList(fresh), item, b.s.in.Item)
}

// scopeRefusal is the reason to reject a member whose new issues have no owner line. It tells how to keep them.
func (b *batchBase) scopeRefusal(item string, missing []model.IssueRef) string {
	return fmt.Sprintf("%v: item %s: %s has no owner line, so this merge rejects it and the other members still land. To keep it, run the merge again and answer with beehave answer %s \"keep %s\". Or remove the closing keyword",
		errScopeRefused, item, issueList(missing), b.s.in.Item, keepList(missing))
}

// keepList shows issues as "#12, other-org/other-repo#5", the list of the keep form.
func keepList(issues []model.IssueRef) string {
	return strings.Join(strings.Fields(issueList(issues)), ", ")
}

// keep records the issues that the owner kept for a member as a step of the run. A later merge call of the member, in this run or in a new run of the item, asks no second question.
func (b *batchBase) keep(item string, issues []model.IssueRef) error {
	_, err := runStep(b.s.e, b.s.ctx, b.stepName(keptStep, item, 1), func(context.Context) (keptRead, error) {
		return keptRead{Item: item, Issues: issues}, nil
	}, dbos.WithStepMaxRetries(stepRetries))
	if err != nil {
		return fmt.Errorf("record the issues kept for item %s: %w", item, err)
	}

	return nil
}

// keptRead is what a kept step records: the member and the issues that the owner kept for it.
type keptRead struct {
	Item   string           `json:"item"`
	Issues []model.IssueRef `json:"issues"`
}

// keptOf returns the issues that the owner kept for the member in this run and in the earlier runs of the item, sorted and without a repeat.
func (s *script) keptOf(member string) ([]model.IssueRef, error) {
	ids, err := s.earlierRuns(s.in.Item)
	if err != nil {
		return nil, err
	}

	kept := []model.IssueRef{}

	var failed error

	for _, id := range append(ids, workflowID(s.ctx)) {
		recorded, err := recordedKept(s.e.ctx, id, member)
		kept, failed = append(kept, recorded...), errors.Join(failed, err)
	}

	return sortedIssues(kept), failed
}

// recordedKept returns the issues that the kept steps of a run recorded for the member.
func recordedKept(ctx dbos.Context, id, member string) ([]model.IssueRef, error) {
	steps, err := dbos.GetWorkflowSteps(ctx, id, dbos.WithStepsLoadOutput(true))
	if err != nil {
		return nil, fmt.Errorf("read the steps of %s: %w", id, err)
	}

	var kept []model.IssueRef

	for _, step := range steps {
		if read := keptOutput(step); read.Item == member {
			kept = append(kept, read.Issues...)
		}
	}

	return kept, nil
}

// keptOutput returns what a kept step recorded, or an empty read for any other step and for a step that failed.
func keptOutput(step dbos.StepInfo) keptRead {
	text, isText := step.Output.(string)

	var read keptRead
	if !isText || !strings.Contains(step.StepName, "."+keptStep+"#") || step.Error != nil || json.Unmarshal([]byte(text), &read) != nil {
		return keptRead{}
	}

	return read
}

// keptIssues returns the issues of an owner line in the form "keep #N[, owner/repo#N ...]". Any other text, or a number too large for int, gives false.
func keptIssues(text string) ([]model.IssueRef, bool) {
	if !keepForm.MatchString(text) {
		return nil, false
	}

	kept := []model.IssueRef{}

	for _, found := range issueRef.FindAllString(text, -1) {
		issue, ok := model.ParseIssue(found)
		if !ok {
			return nil, false
		}

		kept = append(kept, issue)
	}

	return kept, true
}

// issueList shows issues as "#12 other-org/other-repo#5", or "no issue" for none.
func issueList(issues []model.IssueRef) string {
	if len(issues) == 0 {
		return "no issue"
	}

	parts := make([]string, len(issues))
	for i, issue := range issues {
		parts[i] = string(issue)
	}

	return strings.Join(parts, " ")
}
