// Package forgetest holds the conformance suite for forge.Forge. An adapter test runs the suite against the adapter with a fake command line.
// Every new forge backend must call the suite from its tests.
package forgetest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"testing"

	"github.com/tobocop2/beehest/internal/forge"
	"github.com/tobocop2/beehest/internal/model"
)

const pageSize = 10

// Reporter is the part of testing.T the suite uses. A test of the suite passes a recorder to see a failure.
type Reporter interface {
	Helper()
	Errorf(format string, args ...any)
}

// Factory returns a new forge over a fake that serves the pull request that Seven describes. When broken is true, every command fails.
type Factory func(t *testing.T, broken bool) forge.Forge

// SevenReviewer, SevenBody and SevenTitle describe pull request 7, which the fake behind a Factory serves.
const (
	Seven = 7
	// SevenIssue is the number of the issue that the body of pull request 7 links.
	SevenIssue    = 5
	SevenReviewer = "alice"
	SevenBody     = "Closes #5"
	SevenTitle    = "Add the adapters"
	SevenCheck    = "build"
	// SevenCommits and SevenFiles are the counts the fake serves for pull request 7.
	SevenCommits = 2
	SevenFiles   = 3
	// SevenState, SevenBase and SevenAssignee are the state, base branch and assignee that the fake serves for pull request 7. Its author is SevenReviewer and its head is SevenTip.
	SevenState    = "OPEN"
	SevenBase     = "main"
	SevenAssignee = "bob"
	// SevenHold is the one label that the typed read of pull request 7 gives, and SevenAsked the login that was asked to review it. Pull request 7 is a draft, and its body links issue 5.
	SevenHold  = "hold"
	SevenAsked = "carol"
	// NoHeadPull, NoStatePull, NoBasePull, NoDraftPull, NoLabelsPull and NoAssigneesPull are the pull requests that the fake serves with no head commit, no state, no base branch,
	// no draft flag, no label list and no assignee list. The typed read of each must be a *model.IncompletePull that names the field, never a partial answer.
	NoHeadPull      = 8
	NoStatePull     = 9
	NoBasePull      = 10
	NoDraftPull     = 11
	NoLabelsPull    = 12
	NoAssigneesPull = 13
)

// The open pull requests that the fake serves, with the base that each is on: OtherPull is on OtherBase. A list of HoleBase holds a pull request with no head branch,
// which the typed list read reports as a *model.IncompletePull that names the branch, and still lists. SevenLabel is the one label of pull request 7.
const (
	OtherPull  = 11
	OtherBase  = "dev"
	HoleBase   = "hole"
	EmptyBase  = "empty"
	SevenLabel = "round"
)

// What the closing read serves. Pull request 7 closes issue SevenClosing of its own repository and SevenClosingElsewhere of another one. NoClosingPull closes none.
const (
	SevenClosing                         = 12
	SevenClosingElsewhere model.IssueRef = "other-org/other-repo#3"
	NoClosingPull                        = 14
)

// What the issue read serves. Issue SevenIssue is open and ClosedIssue is closed.
const (
	ClosedIssue = 6
	IssueOpen   = "OPEN"
	IssueClosed = "CLOSED"
)

// SevenClosingRefs is the closing read of pull request 7 as every forge gives it.
func SevenClosingRefs() model.ClosingRefs {
	return model.ClosingRefs{BaseBranch: SevenBase, DefaultBranch: DefaultBranch, Issues: []int{SevenClosing}, Elsewhere: []model.IssueRef{SevenClosingElsewhere}}
}

// NoClosingRefs is the closing read of a pull request that closes no issue. Both lists are empty and never nil.
func NoClosingRefs() model.ClosingRefs {
	return model.ClosingRefs{BaseBranch: SevenBase, DefaultBranch: DefaultBranch, Issues: []int{}, Elsewhere: []model.IssueRef{}}
}

// SevenOpen is pull request 7 as the typed list read gives it.
func SevenOpen() model.OpenPull {
	return model.OpenPull{Number: Seven, Branch: SevenBranch, Base: SevenBase, Author: SevenReviewer, Title: SevenTitle, Labels: []string{SevenLabel}, ReviewDecision: "APPROVED"}
}

// OtherOpen is the draft pull request on OtherBase as the typed list read gives it.
func OtherOpen() model.OpenPull {
	return model.OpenPull{Number: OtherPull, Branch: "other", Base: OtherBase, Author: SevenAssignee, Draft: true, Title: "Other work"}
}

// What the fake behind a Factory also serves: the comment and the inline comment of pull request 7, the default branch,
// the branch of pull request 7 with its tip, and a branch that does not exist.
const (
	SevenComment  = "looks good"
	SevenInline   = "rename this"
	DefaultBranch = "main"
	SevenBranch   = "feature"
	SevenTip      = "0123456789abcdef0123456789abcdef01234567"
	GoneBranch    = "gone"
	NewBranch     = "next"
	ScopeRepo     = "repo"
	ScopeWorkflow = "workflow"
	SearchSince   = "2026-01-01"
	SearchUser    = "alice"
	// SevenRun is the link of a check run that the fake serves with no annotation.
	SevenRun = "https://github.com/owner/name/runs/9"
)

// TestForge runs the conformance suite against the forge that make returns, as golang.org/x/net/nettest.TestConn does for a connection.
func TestForge(t *testing.T, make Factory) {
	t.Helper()
	t.Run("declares_the_item_fields", func(t *testing.T) {
		CheckItemFields(t, make(t, false))
	})
	t.Run("names_the_clone_url", func(t *testing.T) {
		CheckCloneURL(t, make(t, false))
	})
	t.Run("item_returns_the_declared_fields", func(t *testing.T) {
		CheckItem(t, make(t, false))
	})
	t.Run("reads_the_pull_request", func(t *testing.T) {
		CheckForgeReads(t, make(t, false))
	})
	t.Run("a_failed_command_is_an_error_everywhere", func(t *testing.T) {
		CheckForgeFails(t, make(t, true))
	})
}

// check records a failed expectation.
type check struct {
	r Reporter
}

func (c check) noError(err error, what string) {
	c.r.Helper()

	if err != nil {
		c.r.Errorf("%s: unexpected error: %v", what, err)
	}
}

func (c check) isError(err error, what string) {
	c.r.Helper()

	if err == nil {
		c.r.Errorf("%s: want an error from a failed command, got none", what)
	}
}

func (c check) equal(got, want any, what string) {
	c.r.Helper()

	if fmt.Sprintf("%#v", got) != fmt.Sprintf("%#v", want) {
		c.r.Errorf("%s: got %v, want %v", what, got, want)
	}
}

// call is one method of the contract with its arguments filled in.
type call struct {
	name string
	run  func(ctx context.Context) error
}

func (c check) allFail(calls []call) {
	c.r.Helper()

	for _, one := range calls {
		c.isError(one.run(context.Background()), one.name)
	}
}

// CheckForgeReads runs the read rules on a forge that serves pull request 7.
func CheckForgeReads(r Reporter, target forge.Forge) {
	r.Helper()

	c, ctx := check{r: r}, context.Background()

	reviews, err := target.Reviews(ctx, Seven)
	c.noError(err, "Reviews")
	c.equal(pick(reviews, func(v model.Review) string { return v.User.Login + " " + string(v.State) }), []string{SevenReviewer + " APPROVED"}, "reviews")

	body, err := target.PRBody(ctx, Seven)
	c.noError(err, "PRBody")
	c.equal(body, SevenBody, "body")

	view, err := target.PRView(ctx, Seven, "title")
	c.noError(err, "PRView")
	c.equal(view.Title, SevenTitle, "title")

	checkLists(c, target)
	checkConversation(c, target)
	checkBranches(c, target)
	checkStanding(c, target)
}

// SevenFacts is the typed read that every forge serves for pull request 7.
func SevenFacts() model.PullFacts {
	return model.PullFacts{
		Number: Seven, State: SevenState, Head: SevenTip, Base: SevenBase, Author: SevenReviewer, Assignees: []string{SevenAssignee},
		Draft: true, Labels: []string{SevenHold}, Links: []model.IssueLink{{Number: SevenIssue}}, Reviewers: []string{SevenAsked},
	}
}

// checkPullRead runs the typed read of pull request 7.
func checkPullRead(c check, target forge.Forge) {
	c.r.Helper()

	facts, err := target.PullRead(context.Background(), Seven)
	c.noError(err, "PullRead")
	c.equal(facts, SevenFacts(), "pull facts")

	for pr, field := range map[int]string{NoHeadPull: "head", NoStatePull: "state", NoBasePull: "base", NoDraftPull: "draft", NoLabelsPull: "labels", NoAssigneesPull: "assignees"} {
		_, err = target.PullRead(context.Background(), pr)

		var hole *model.IncompletePull

		if !errors.As(err, &hole) || !slices.Equal(hole.Missing, []string{field}) {
			c.r.Errorf("PullRead of a pull request with no %s: want an IncompletePull that names it, got %v", field, err)
		}
	}
}

// CheckOpenPulls runs the typed list read of the open pull requests: no base lists every one, a base lists only the pull requests on it, a base with none lists none,
// and a pull request with no head branch is an IncompletePull that names the branch.
func CheckOpenPulls(r Reporter, target forge.OpenPullsRead) {
	r.Helper()

	all, err := target.OpenPulls(context.Background(), "")
	if err != nil {
		r.Errorf("OpenPulls: unexpected error: %v", err)

		return
	}

	check{r: r}.equal(all, []model.OpenPull{SevenOpen(), OtherOpen()}, "OpenPulls of no base")

	checkOpenBases(r, target)
	checkOpenHole(r, target)
}

// checkOpenBases checks that a base lists only the pull requests on it, and a base with none lists none.
func checkOpenBases(r Reporter, target forge.OpenPullsRead) {
	r.Helper()

	c := check{r: r}

	for _, one := range []struct {
		what, base string
		want       []int
	}{
		{"a base", SevenBase, []int{Seven}},
		{"another base", OtherBase, []int{OtherPull}},
		{"a base with no pull request", EmptyBase, []int{}},
	} {
		pulls, err := target.OpenPulls(context.Background(), one.base)
		c.noError(err, "OpenPulls")

		if got := pick(pulls, func(v model.OpenPull) int { return v.Number }); !slices.Equal(got, one.want) {
			r.Errorf("OpenPulls of %s: got %v, want %v", one.what, got, one.want)
		}
	}
}

// checkOpenHole checks that a pull request with no head branch is an IncompletePull that names the branch.
func checkOpenHole(r Reporter, target forge.OpenPullsRead) {
	r.Helper()

	pulls, err := target.OpenPulls(context.Background(), HoleBase)

	var hole *model.IncompletePull

	if !errors.As(err, &hole) || !slices.Equal(hole.Missing, []string{"branch"}) {
		r.Errorf("OpenPulls of a pull request with no branch: want an IncompletePull that names it, got %v", err)
	}

	if got := pick(pulls, func(v model.OpenPull) int { return v.Number }); !slices.Equal(got, []int{OtherPull}) {
		r.Errorf("OpenPulls of a pull request with no branch: want the list to hold the pull request with the error, got %v", got)
	}
}

// checkCounts runs the read of the forge's own counts.
func checkCounts(c check, target forge.Forge) {
	c.r.Helper()

	commits, files, err := target.PullCounts(context.Background(), Seven)
	c.noError(err, "PullCounts")
	c.equal([]int{commits, files}, []int{SevenCommits, SevenFiles}, "counts")

	committers, err := target.Committers(context.Background(), Seven)
	c.noError(err, "Committers")
	c.equal(len(committers), SevenCommits, "committers")
}

// checkReadSuites runs the reads that have a check of their own: the open pull requests, the closing references and the state of an issue.
func checkReadSuites(c check, target forge.Forge) {
	c.r.Helper()

	CheckOpenPulls(c.r, target)
	CheckClosingRefs(c.r, target)
	CheckIssueState(c.r, target)
}

// checkStanding runs the batched read of our pull requests, the title of an item and the annotations of a check run.
func checkStanding(c check, target forge.Forge) {
	c.r.Helper()

	checkCounts(c, target)
	checkPullRead(c, target)
	checkReadSuites(c, target)

	ctx := context.Background()

	title, err := target.Title(ctx, Seven)
	c.noError(err, "Title")
	c.equal(title, SevenTitle, "item title")

	own, err := target.OwnPulls(ctx, SearchUser)
	c.noError(err, "OwnPulls")
	c.equal(pick(own, func(v model.OwnPull) int { return v.Number }), []int{Seven}, "own numbers")

	notStarted, err := target.CheckNotStarted(ctx, SevenRun)
	c.noError(err, "CheckNotStarted")
	c.equal(notStarted, false, "check not started")
}

func checkConversation(c check, target forge.Forge) {
	c.r.Helper()

	ctx := context.Background()
	body := func(v model.Comment) string { return v.Body }

	comments, err := target.Comments(ctx, Seven)
	c.noError(err, "Comments")
	c.equal(pick(comments, body), []string{SevenComment}, "comments")

	inline, err := target.ReviewComments(ctx, Seven)
	c.noError(err, "ReviewComments")
	c.equal(pick(inline, body), []string{SevenInline}, "review comments")

	found, err := target.Search(ctx, model.SearchRequest{Kind: model.SearchPulls, Involves: SearchUser, Since: SearchSince, Limit: pageSize})
	c.noError(err, "Search")
	c.equal(found, []int{Seven}, "searched numbers")

	checkWrites(c, target)
}

func checkWrites(c check, target forge.Forge) {
	c.r.Helper()

	ctx := context.Background()

	c.noError(target.PostComment(ctx, Seven, "hello"), "PostComment")
	c.noError(target.SetDraft(ctx, Seven, true), "SetDraft to draft")
	c.noError(target.SetDraft(ctx, Seven, false), "SetDraft to ready")
	c.noError(target.MergePR(ctx, Seven, SevenTip), "MergePR")
	c.noError(target.CreateBranch(ctx, NewBranch, SevenTip), "CreateBranch")
	c.noError(target.SetBase(ctx, Seven, NewBranch), "SetBase")
	c.noError(target.SetBody(ctx, Seven, "new body"), "SetBody")
}

func checkBranches(c check, target forge.Forge) {
	c.r.Helper()

	ctx := context.Background()

	name, err := target.DefaultBranch(ctx)
	c.noError(err, "DefaultBranch")
	c.equal(name, DefaultBranch, "default branch")

	tip, found, err := target.BranchRef(ctx, SevenBranch)
	c.noError(err, "BranchRef")
	c.equal([]any{tip, found}, []any{SevenTip, true}, "branch tip")

	tip, found, err = target.BranchRef(ctx, GoneBranch)
	c.noError(err, "BranchRef of a missing branch")
	c.equal([]any{tip, found}, []any{"", false}, "missing branch")

	answer, err := target.CanWriteCI(ctx)
	c.noError(err, "CanWriteCI")
	c.equal(answer, model.CIWriteYes, "the token holds the scopes repo and workflow, so it can change CI")
}

func checkLists(c check, target forge.Forge) {
	c.r.Helper()

	ctx := context.Background()

	list, err := target.PRList(ctx, pageSize, "--json", "number,title")
	c.noError(err, "PRList")
	c.equal(pick(list, func(v model.PullRequest) int { return v.Number }), []int{Seven}, "listed numbers")

	checks, err := target.PRChecks(ctx, Seven)
	c.noError(err, "PRChecks")
	c.equal(checks, []model.Check{{Name: SevenCheck, Bucket: model.CheckPass}}, "checks")
}

// CheckCloneURL checks that a forge names the URL to clone its repository from.
func CheckCloneURL(r Reporter, target forge.Forge) {
	r.Helper()

	if target.CloneURL() == "" {
		r.Errorf("CloneURL: want the URL to clone the repository from, got none")
	}
}

// CheckClosingRefs checks the closing read on a forge that serves pull requests 7 and NoClosingPull: the issues of the pull request, in its repository and in another one,
// and an empty list, never nil, for a pull request that closes none. The failed command is checked by CheckForgeFails.
func CheckClosingRefs(r Reporter, target forge.Lookup) {
	r.Helper()

	c := check{r: r}

	for _, one := range []struct {
		what string
		pr   int
		want model.ClosingRefs
	}{
		{"a pull request that closes two issues", Seven, SevenClosingRefs()},
		{"a pull request that closes none", NoClosingPull, NoClosingRefs()},
	} {
		got, err := target.ClosingRefs(context.Background(), one.pr)
		c.noError(err, "ClosingRefs")
		c.equal(got, one.want, "ClosingRefs of "+one.what)
	}
}

// CheckIssueState checks the state read on a forge that serves issue SevenIssue as open and issue ClosedIssue as closed. The state is the name that the forge gives,
// OPEN or CLOSED. The failed command is checked by CheckForgeFails.
func CheckIssueState(r Reporter, target forge.Lookup) {
	r.Helper()

	c := check{r: r}

	for _, one := range []struct {
		what   string
		number int
		want   string
	}{
		{"an open issue", SevenIssue, IssueOpen},
		{"a closed issue", ClosedIssue, IssueClosed},
	} {
		got, err := target.IssueState(context.Background(), one.number)
		c.noError(err, "IssueState")
		c.equal(got, one.want, "IssueState of "+one.what)
	}
}

// CheckItemFields checks that a forge declares at least one item field.
func CheckItemFields(r Reporter, target forge.Forge) {
	r.Helper()

	if len(target.ItemFields()) == 0 {
		r.Errorf("ItemFields: want at least one declared field, got none")
	}
}

// CheckItem checks the success path of Item on a forge that serves pull request 7: the result holds one value for each declared field, and the pr field is the number.
func CheckItem(r Reporter, target forge.Forge) {
	r.Helper()

	got, err := target.Item(context.Background(), Seven)
	if err != nil {
		r.Errorf("Item: unexpected error: %v", err)

		return
	}

	declared := target.ItemFields()
	checkDeclared(r, got, declared)

	if want := strconv.Itoa(Seven); slices.Contains(declared, "pr") && got["pr"] != want {
		r.Errorf("Item: want pr %q, got %q", want, got["pr"])
	}
}

// checkDeclared checks that got holds one value for each declared field and no other field.
func checkDeclared(r Reporter, got map[string]string, declared []string) {
	r.Helper()

	for _, name := range declared {
		if _, found := got[name]; !found {
			r.Errorf("Item: want a value for the declared field %q, got %v", name, got)
		}
	}

	for name := range got {
		if !slices.Contains(declared, name) {
			r.Errorf("Item: got the field %q, which ItemFields does not declare (%v)", name, declared)
		}
	}
}

// CheckForgeFails runs every method of a forge whose commands all fail. Each must return an error and never a guess.
func CheckForgeFails(r Reporter, target forge.Forge) {
	r.Helper()

	check{r: r}.allFail(forgeCalls(target))
}

// FailedCommandMethods names every method that a failed-command check runs: forge.Forge, Syncer, Statuses, AssigneeReads, ReadyReads and Boarder.
// A method of those interfaces that is not in the list has no failed-command case.
// Opener is left out: its three methods only write, so no read can turn an error into an empty answer, and the adapter tests prove each write.
func FailedCommandMethods() []string {
	var names []string

	for _, calls := range [][]call{forgeCalls(nil), syncerCalls(nil), statusCalls(nil), assigneeCalls(nil), readyCalls(nil), boarderCalls(nil)} {
		for _, one := range calls {
			names = append(names, one.name)
		}
	}

	return names
}

// forgeCalls is every method of forge.Forge that takes arguments, with the arguments filled in. The closures need a target only when they run.
func forgeCalls(target forge.Forge) []call {
	var all []call

	all = append(all, readCalls(target)...)
	all = append(all, newCalls(target)...)
	all = append(all, mergeCalls(target)...)

	return append(all, standingCalls(target)...)
}

func readCalls(target forge.Forge) []call {
	return []call{
		{"Reviews", func(ctx context.Context) error { _, err := target.Reviews(ctx, Seven); return err }},
		{"PRBody", func(ctx context.Context) error { _, err := target.PRBody(ctx, Seven); return err }},
		{"PRView", func(ctx context.Context) error { _, err := target.PRView(ctx, Seven, "title"); return err }},
		{"PullRead", func(ctx context.Context) error { _, err := target.PullRead(ctx, Seven); return err }},
		{"OpenPulls", func(ctx context.Context) error { _, err := target.OpenPulls(ctx, SevenBase); return err }},
		{"PRList", func(ctx context.Context) error {
			_, err := target.PRList(ctx, pageSize, "--json", "number")
			return err
		}},
		{"PullCounts", func(ctx context.Context) error { _, _, err := target.PullCounts(ctx, Seven); return err }},
		{"PRChecks", func(ctx context.Context) error { _, err := target.PRChecks(ctx, Seven); return err }},
		{"OpenChangeRequests", func(ctx context.Context) error { _, err := target.OpenChangeRequests(ctx, Seven); return err }},
		{"UnresolvedThreads", func(ctx context.Context) error { _, err := target.UnresolvedThreads(ctx, Seven); return err }},
		{"StackID", func(ctx context.Context) error { _, err := target.StackID(ctx, Seven); return err }},
		{"ExistingReview", func(ctx context.Context) error { _, err := target.ExistingReview(ctx, Seven); return err }},
	}
}

// standingCalls are the batched read, the title read, the annotation read, the item read, the closing references and the state of an issue.
func standingCalls(target forge.Forge) []call {
	return []call{
		{"Item", func(ctx context.Context) error { _, err := target.Item(ctx, Seven); return err }},
		{"Title", func(ctx context.Context) error { _, err := target.Title(ctx, Seven); return err }},
		{"OwnPulls", func(ctx context.Context) error { _, err := target.OwnPulls(ctx, SearchUser); return err }},
		{"CheckNotStarted", func(ctx context.Context) error { _, err := target.CheckNotStarted(ctx, SevenRun); return err }},
		{"ClosingRefs", func(ctx context.Context) error { _, err := target.ClosingRefs(ctx, Seven); return err }},
		{"IssueState", func(ctx context.Context) error { _, err := target.IssueState(ctx, Seven); return err }},
	}
}

// newCalls are the overlap, the conversation and the branch methods.
func newCalls(target forge.Forge) []call {
	return []call{
		{"Overlap", func(ctx context.Context) error {
			_, err := target.Overlap(ctx, model.OverlapRequest{PR: "7"})
			return err
		}},
		{"Comments", func(ctx context.Context) error { _, err := target.Comments(ctx, Seven); return err }},
		{"ReviewComments", func(ctx context.Context) error { _, err := target.ReviewComments(ctx, Seven); return err }},
		{"Search", func(ctx context.Context) error {
			_, err := target.Search(ctx, model.SearchRequest{Kind: model.SearchIssues, Involves: SearchUser, Since: SearchSince, Limit: pageSize})
			return err
		}},
		{"DefaultBranch", func(ctx context.Context) error { _, err := target.DefaultBranch(ctx); return err }},
		{"BranchRef", func(ctx context.Context) error { _, _, err := target.BranchRef(ctx, SevenBranch); return err }},
		{"PostComment", func(ctx context.Context) error { return target.PostComment(ctx, Seven, "hello") }},
		{"SetDraft", func(ctx context.Context) error { return target.SetDraft(ctx, Seven, true) }},
	}
}

// mergeCalls are the merge, the branch and pull request edits and the CI write read.
func mergeCalls(target forge.Forge) []call {
	return []call{
		{"MergePR", func(ctx context.Context) error { return target.MergePR(ctx, Seven, SevenTip) }},
		{"CreateBranch", func(ctx context.Context) error { return target.CreateBranch(ctx, NewBranch, SevenTip) }},
		{"SetBase", func(ctx context.Context) error { return target.SetBase(ctx, Seven, NewBranch) }},
		{"SetBody", func(ctx context.Context) error { return target.SetBody(ctx, Seven, "new body") }},
		{"CanWriteCI", func(ctx context.Context) error { _, err := target.CanWriteCI(ctx); return err }},
		{"Committers", func(ctx context.Context) error { _, err := target.Committers(ctx, Seven); return err }},
	}
}

// CheckAssigneeReadsFail runs the assignee read of a forge whose commands all fail. It must return an error and never an empty list.
func CheckAssigneeReadsFail(r Reporter, target forge.AssigneeReads) {
	r.Helper()

	check{r: r}.allFail(assigneeCalls(target))
}

func assigneeCalls(target forge.AssigneeReads) []call {
	return []call{
		{"IssueAssignees", func(ctx context.Context) error { _, err := target.IssueAssignees(ctx, "", Seven); return err }},
	}
}

// CheckReadyReadsFail runs the ready-mark read of a forge whose commands all fail. It must return an error and never an empty list.
func CheckReadyReadsFail(r Reporter, target forge.ReadyReads) {
	r.Helper()

	check{r: r}.allFail(readyCalls(target))
}

func readyCalls(target forge.ReadyReads) []call {
	return []call{
		{"ReadyMarks", func(ctx context.Context) error { _, err := target.ReadyMarks(ctx, Seven); return err }},
	}
}

// sevenCard is the card of pull request 7 on board 1, for the board calls of a forge whose commands all fail.
func sevenCard() model.Card {
	return model.Card{Board: 1, Number: Seven, Pull: true, Status: "Todo"}
}

// CheckBoarderFails runs both methods of a Boarder whose commands all fail. Each must return an error and never a guess.
func CheckBoarderFails(r Reporter, target forge.Boarder) {
	r.Helper()

	check{r: r}.allFail(boarderCalls(target))
}

func boarderCalls(target forge.Boarder) []call {
	return []call{
		{"PlaceCard", func(ctx context.Context) error { return target.PlaceCard(ctx, sevenCard()) }},
		{"CardStatus", func(ctx context.Context) error { _, err := target.CardStatus(ctx, sevenCard()); return err }},
	}
}

// What the fake behind a Syncer serves: the count of commits that SyncHead has and SyncBase lacks, and the merge commit of the merge.
const (
	SyncBase  = "sync/round"
	SyncHead  = "main"
	SyncAhead = 3
	SyncMerge = "89abcdef0123456789abcdef0123456789abcdef"
)

// CheckSyncer runs the rules of a Syncer whose fake serves SyncAhead commits and answers a merge with SyncMerge.
func CheckSyncer(r Reporter, target forge.Syncer) {
	r.Helper()

	c, ctx := check{r: r}, context.Background()

	ahead, err := target.CommitsAhead(ctx, SyncBase, SyncHead)
	c.noError(err, "CommitsAhead")
	c.equal(ahead, SyncAhead, "commits ahead")

	merge, err := target.MergeBranch(ctx, SyncBase, SyncHead)
	c.noError(err, "MergeBranch")
	c.equal(merge, SyncMerge, "merge commit")
}

// CheckSyncerFails runs both methods of a Syncer whose commands all fail. Each must return an error and never a guess.
func CheckSyncerFails(r Reporter, target forge.Syncer) {
	r.Helper()

	check{r: r}.allFail(syncerCalls(target))
}

func syncerCalls(target forge.Syncer) []call {
	return []call{
		{"CommitsAhead", func(ctx context.Context) error { _, err := target.CommitsAhead(ctx, SyncBase, SyncHead); return err }},
		{"MergeBranch", func(ctx context.Context) error { _, err := target.MergeBranch(ctx, SyncBase, SyncHead); return err }},
	}
}

// What the fake behind a Statuses serves: the branch that requires a pull request and the statuses StatusGate and StatusReview, and the commit that a status is posted on.
const (
	StatusBranch = "integration/round"
	StatusGate   = "beehest/gate"
	StatusReview = "beehest/review"
	StatusCommit = "fedcba9876543210fedcba9876543210fedcba98"
)

// CheckStatuses runs the rules of a Statuses whose fake accepts a status on StatusCommit and serves a protection of StatusBranch that requires a pull request
// and the statuses StatusGate and StatusReview.
func CheckStatuses(r Reporter, target forge.Statuses) {
	r.Helper()

	c, ctx := check{r: r}, context.Background()

	c.noError(target.PostStatus(ctx, model.CommitStatus{SHA: StatusCommit, Context: StatusGate, State: model.StatusSuccess, Description: "the gate passed"}), "PostStatus")

	got, err := target.Protection(ctx, StatusBranch)
	c.noError(err, "Protection")
	c.equal(got, model.Protection{PullRequest: true, RequiredStatuses: []string{StatusGate, StatusReview}}, "protection")
}

// CheckStatusesFails runs both methods of a Statuses whose commands all fail. Each must return an error and never a guess.
func CheckStatusesFails(r Reporter, target forge.Statuses) {
	r.Helper()

	check{r: r}.allFail(statusCalls(target))
}

func statusCalls(target forge.Statuses) []call {
	return []call{
		{"PostStatus", func(ctx context.Context) error {
			return target.PostStatus(ctx, model.CommitStatus{SHA: StatusCommit, Context: StatusGate, State: model.StatusSuccess})
		}},
		{"Protection", func(ctx context.Context) error { _, err := target.Protection(ctx, StatusBranch); return err }},
	}
}

func pick[T, V any](rows []T, field func(T) V) []V {
	out := make([]V, len(rows))
	for i, row := range rows {
		out[i] = field(row)
	}

	return out
}
