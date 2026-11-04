package scope_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/examples/remediation/tools/check"
	"github.com/tobocop2/beehave/examples/remediation/tools/scope"
)

var errDown = errors.New("down")

type records struct {
	closes, adds       map[string][]string
	closesErr, addsErr error
}

func (r records) Closes(key string) ([]string, error) { return r.closes[key], r.closesErr }

func (r records) OwnerAdds(key string) ([]string, error) { return r.adds[key], r.addsErr }

type forge struct {
	closing []int
	err     error
	asked   []int
}

func (f *forge) ClosingIssues(_ context.Context, pr int) ([]int, error) {
	f.asked = append(f.asked, pr)

	return f.closing, f.err
}

// bodyForge is a forge with no closing-reference read: only the body of the pull request.
type bodyForge struct {
	body string
	err  error
}

func (f bodyForge) PRBody(context.Context, int) (string, error) { return f.body, f.err }

func run(t *testing.T, recs records, f *forge, req scope.Request) scope.Result {
	t.Helper()

	got, err := scope.Check(t.Context(), recs, f, req)
	require.NoError(t, err)

	return got
}

func TestCheck_AnIssueClosedOnlyAfterTheFirstReviewIsRefusedWithoutAnOwnerRow(t *testing.T) {
	t.Parallel()

	recs := records{closes: map[string][]string{"792": {"751", "762", "778"}}}
	got := run(t, recs, &forge{closing: []int{800, 751, 795}}, scope.Request{PR: "792"})

	assert.Equal(t, scope.CodeRefused, got.Code)
	require.Len(t, got.Lines, 4)
	assert.Equal(t, "REFUSED: pull request 792 closes #795 #800 now, and did not at its first review (then: 751 762 778).", got.Lines[0])
	assert.Equal(t, "         remedy ledger add 792 <issue> owner-add --state <folder> --words <the owner's words>", got.Lines[2])
}

func TestCheck_AnOwnerRowThatNamesTheIssueLetsItStay(t *testing.T) {
	t.Parallel()

	recs := records{closes: map[string][]string{"792": {"751"}}, adds: map[string][]string{"792": {"795"}}}
	got := run(t, recs, &forge{closing: []int{751, 795, 800}}, scope.Request{PR: "792"})

	assert.Equal(t, scope.CodeRefused, got.Code)
	assert.Contains(t, got.Lines[0], "closes #800 now")
	assert.NotContains(t, got.Lines[0], "#795")
}

func TestCheck_EveryNewIssueWithAnOwnerRowIsOK(t *testing.T) {
	t.Parallel()

	recs := records{closes: map[string][]string{"792": {"751"}}, adds: map[string][]string{"792": {"795"}}}
	got := run(t, recs, &forge{closing: []int{751, 795}}, scope.Request{PR: "792"})

	assert.Equal(t, scope.Result{Lines: []string{"OK: pull request 792 closes no issue that is new since its first review without an owner row."}}, got)
}

func TestCheck_AFirstReviewWithNoClosesLineFixesTheScopeUnderTheKey(t *testing.T) {
	t.Parallel()

	recs := records{closes: map[string][]string{"h2m-spacing": {"-"}}}
	got := run(t, recs, &forge{closing: []int{803}}, scope.Request{PR: "792", Key: "h2m-spacing"})

	assert.Equal(t, scope.CodeRefused, got.Code)
	assert.Contains(t, got.Lines[0], "closes #803 now")
	assert.Contains(t, got.Lines[0], "(then: -)")
	assert.Equal(t, "         remedy ledger add h2m-spacing <issue> owner-add --state <folder> --words <the owner's words>", got.Lines[2])
}

func TestCheck_APullRequestTheLedgerHasNotReviewedHasNoScopeYet(t *testing.T) {
	t.Parallel()

	f := &forge{closing: []int{1}}
	got := run(t, records{}, f, scope.Request{PR: "792", Key: "other"})

	assert.Equal(t, scope.Result{Lines: []string{"OK: the ledger holds no review row for other, so its scope is not fixed yet."}}, got)
	assert.Empty(t, f.asked, "the body is not read when there is no scope")
}

func TestCheck_ThePullRequestMustBeDigits(t *testing.T) {
	t.Parallel()

	for _, pr := range []string{"", "7a", "-7", "٧", "1 2"} {
		_, err := scope.Check(t.Context(), records{}, &forge{}, scope.Request{PR: pr})

		var refusal *check.Refusal

		require.ErrorAs(t, err, &refusal, pr)
		assert.Equal(t, "name the pull request by its number, digits only.", refusal.Message)
	}
}

func TestCheck_ADigitStringAboveTheRangeIsTooLargeNotNonDigits(t *testing.T) {
	t.Parallel()

	_, err := scope.Check(t.Context(), records{}, &forge{}, scope.Request{PR: "99999999999999999999999"})

	var refusal *check.Refusal

	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, "the pull request number is too large.", refusal.Message)
}

func TestCheck_ABodyThatCannotBeReadNamesThePullRequest(t *testing.T) {
	t.Parallel()

	recs := records{closes: map[string][]string{"792": {"751"}}}
	_, err := scope.Check(t.Context(), recs, &forge{err: errDown}, scope.Request{PR: "792", Repo: "owner/name"})

	var refusal *check.Refusal

	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, "cannot read the issues that pull request 792 closes in owner/name.", refusal.Message)
	require.ErrorIs(t, err, errDown)
}

func TestCheck_ALeadingZeroPullRequestIsAskedWithoutIt(t *testing.T) {
	t.Parallel()

	f := &forge{closing: []int{1}}
	got := run(t, records{closes: map[string][]string{"007": {"1"}}}, f, scope.Request{PR: "007"})

	assert.Equal(t, 0, got.Code)
	assert.Equal(t, []int{7}, f.asked)
}

func TestCheck_TwoSpellingsOfOneIssueAreOneIssue(t *testing.T) {
	t.Parallel()

	recs := records{closes: map[string][]string{"792": {"012"}}}
	got := run(t, recs, &forge{closing: []int{12}}, scope.Request{PR: "792"})

	assert.Equal(t, 0, got.Code)
}

func TestCheck_AnOwnerRowCountsOnlyForTheWholeIssueNumber(t *testing.T) {
	t.Parallel()

	for name, adds := range map[string][]string{"longer number": {"55"}, "other issue": {"6"}, "none": nil} {
		recs := records{closes: map[string][]string{"1": {"1"}}, adds: map[string][]string{"1": adds}}
		got := run(t, recs, &forge{closing: []int{5}}, scope.Request{PR: "1"})

		assert.Equal(t, scope.CodeRefused, got.Code, name)
	}
}

func TestFromBody_AForgeWithNoClosingReferencesIsReadByTheClosingKeywordsOfTheBody(t *testing.T) {
	t.Parallel()

	recs := records{closes: map[string][]string{"792": {"751"}}}
	got, err := scope.Check(t.Context(), recs, scope.FromBody(bodyForge{body: "Closes #751\ncloses #795, Fixes #800\nnot a keyword #9"}), scope.Request{PR: "792"})

	require.NoError(t, err)
	assert.Equal(t, scope.CodeRefused, got.Code)
	assert.Contains(t, got.Lines[0], "closes #795 #800 now")
}

func TestCheck_AnIssueClosedOnlyBySidebarLinkIsSeenLikeOneInTheBody(t *testing.T) {
	t.Parallel()

	recs := records{closes: map[string][]string{"792": {"751"}}}
	got := run(t, recs, &forge{closing: []int{751, 900}}, scope.Request{PR: "792"})

	assert.Equal(t, scope.CodeRefused, got.Code)
	assert.Contains(t, got.Lines[0], "closes #900 now")
}

func TestCheck_ALedgerThatCannotBeReadStopsTheCheck(t *testing.T) {
	t.Parallel()

	for name, recs := range map[string]records{
		"closes": {closesErr: errDown},
		"adds":   {closes: map[string][]string{"1": {"1"}}, addsErr: errDown},
	} {
		_, err := scope.Check(t.Context(), recs, &forge{closing: []int{5}}, scope.Request{PR: "1"})

		require.ErrorIs(t, err, errDown, name)
	}
}
