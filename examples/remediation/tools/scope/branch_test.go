package scope_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/examples/remediation/tools/check"
	"github.com/tobocop2/beehave/examples/remediation/tools/scope"
	"github.com/tobocop2/beehave/internal/model"
)

// refusalOf fails the test unless err is a check.Refusal (exit code 2), and returns it.
func refusalOf(t *testing.T, err error) *check.Refusal {
	t.Helper()

	var refusal *check.Refusal

	require.ErrorAs(t, err, &refusal)

	return refusal
}

// branchForge lists closing references with the base branch and the default branch in the same answer, and reads the body too.
type branchForge struct {
	bodyForge
	refs  model.ClosingRefs
	err   error
	calls int
}

func (f *branchForge) ClosingRefs(context.Context, int) (model.ClosingRefs, error) {
	f.calls++

	return f.refs, f.err
}

func TestFromBody_ABodyThatCannotBeReadOrNamesAHugeIssueRefusesTheCheck(t *testing.T) {
	t.Parallel()

	recs := records{closes: map[string][]string{"792": {"751"}}}

	_, err := scope.Check(t.Context(), recs, scope.FromBody(bodyForge{err: errDown}), scope.Request{PR: "792"})
	require.ErrorIs(t, err, errDown)
	assert.Equal(t, "cannot read the body of pull request 792.", refusalOf(t, err).Message)

	_, err = scope.Check(t.Context(), recs, scope.FromBody(bodyForge{body: "Closes #99999999999999999999999"}), scope.Request{PR: "792"})

	assert.Equal(t, "the body of pull request 792 names the issue number 99999999999999999999999, which is too large.", refusalOf(t, err).Message)
}

func TestReaderOf_APullRequestIntoTheDefaultBranchUsesTheListOfTheForgeFromOneCall(t *testing.T) {
	t.Parallel()

	f := &branchForge{bodyForge: bodyForge{body: "Closes #9"}, refs: model.ClosingRefs{BaseBranch: "main", DefaultBranch: "main", Issues: []int{12}}}

	got, err := scope.ReaderOf(f).ClosingIssues(t.Context(), 7)

	require.NoError(t, err)
	assert.Equal(t, []int{12}, got)
	assert.Equal(t, 1, f.calls, "one forge call")
}

func TestReaderOf_AnEmptyListForTheDefaultBranchIsTrustedAsEmpty(t *testing.T) {
	t.Parallel()

	f := &branchForge{bodyForge: bodyForge{body: "Closes #9"}, refs: model.ClosingRefs{BaseBranch: "main", DefaultBranch: "main", Issues: []int{}}}

	got, err := scope.ReaderOf(f).ClosingIssues(t.Context(), 7)

	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestReaderOf_APullRequestIntoAnotherBranchUsesTheClosingKeywordsOfTheBody(t *testing.T) {
	t.Parallel()

	f := &branchForge{bodyForge: bodyForge{body: "Closes #9"}, refs: model.ClosingRefs{BaseBranch: "integration/r7", DefaultBranch: "main", Issues: []int{99}}}

	got, err := scope.ReaderOf(f).ClosingIssues(t.Context(), 7)

	require.NoError(t, err)
	assert.Equal(t, []int{9}, got)
}

func TestReaderOf_AForgeWithNoClosingReferencesUsesTheBody(t *testing.T) {
	t.Parallel()

	got, err := scope.ReaderOf(bodyForge{body: "Fixes #4"}).ClosingIssues(t.Context(), 7)

	require.NoError(t, err)
	assert.Equal(t, []int{4}, got)
}

func TestReaderOf_EveryFailedReadIsACheckRefusalWithItsOwnWords(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		forge *branchForge
		want  string
		cause error
	}{
		"the read fails": {
			forge: &branchForge{err: errDown},
			want:  "cannot read the issues that pull request 7 closes.",
			cause: errDown,
		},
		"the base is not given": {
			forge: &branchForge{refs: model.ClosingRefs{DefaultBranch: "main"}},
			want:  "cannot read the base branch of pull request 7.",
		},
		"the default branch is not given": {
			forge: &branchForge{refs: model.ClosingRefs{BaseBranch: "main"}},
			want:  "cannot read the default branch of the repository of pull request 7.",
		},
		"the body of a pull request into another branch cannot be read": {
			forge: &branchForge{bodyForge: bodyForge{err: errDown}, refs: model.ClosingRefs{BaseBranch: "x", DefaultBranch: "main"}},
			want:  "cannot read the body of pull request 7.",
			cause: errDown,
		},
	}

	for name, tt := range tests {
		got, err := scope.ReaderOf(tt.forge).ClosingIssues(t.Context(), 7)

		require.Error(t, err, name)
		assert.Nil(t, got, name)
		assert.Equal(t, tt.want, refusalOf(t, err).Message, name)

		if tt.cause != nil {
			require.ErrorIs(t, err, tt.cause, name)
		}
	}
}

func TestCheck_ARefusalOfTheReaderPassesUnchangedAndIsNotWrappedAgain(t *testing.T) {
	t.Parallel()

	recs := records{closes: map[string][]string{"7": {"1"}}}
	f := &branchForge{refs: model.ClosingRefs{DefaultBranch: "main"}}

	_, err := scope.Check(t.Context(), recs, scope.ReaderOf(f), scope.Request{PR: "7", Repo: "owner/name"})

	assert.EqualError(t, err, "cannot read the base branch of pull request 7.")
}
