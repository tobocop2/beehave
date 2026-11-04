package github_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

const (
	commit = "0123456789abcdef0123456789abcdef01234567"
	tree   = "89abcdef0123456789abcdef0123456789abcdef"
)

func TestCommitTree_ReadsTheTreeOfACommit(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, tree+"\n")

	got, err := f.CommitTree(context.Background(), commit)
	require.NoError(t, err)
	assert.Equal(t, tree, got)
	assert.Equal(t, [][]string{{"api", "repos/owner/name/git/commits/" + commit, "--jq", ".tree.sha"}}, run.Calls)
}

func TestCommitTree_RefusesAFailedReadAnEmptyAnswerAndANameThatIsNotAnId(t *testing.T) {
	t.Parallel()

	down, _ := failing(t, "HTTP 502")
	_, err := down.CommitTree(context.Background(), commit)
	require.Error(t, err)

	empty, _ := fixed(t, "\n")
	_, err = empty.CommitTree(context.Background(), commit)
	require.Error(t, err)

	odd, run := fixed(t, tree)
	_, err = odd.CommitTree(context.Background(), "../x")
	require.Error(t, err)
	assert.Empty(t, run.Calls, "a name that is not a commit id reaches no URL")
}

func TestCompare_ReadsTheStatusAndBothCountsOfTheComparison(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		want     model.Comparison
		contains bool
	}{
		"ahead 4 0\n":     {model.Comparison{Status: "ahead", AheadBy: 4}, true},
		"identical 0 0\n": {model.Comparison{Status: "identical"}, true},
		"behind 0 2\n":    {model.Comparison{Status: "behind", BehindBy: 2}, false},
		"diverged 3 1\n":  {model.Comparison{Status: "diverged", AheadBy: 3, BehindBy: 1}, false},
		"ahead 3 1\n":     {model.Comparison{Status: "ahead", AheadBy: 3, BehindBy: 1}, false},
	}

	for answer, c := range cases {
		f, run := fixed(t, answer)

		got, err := f.Compare(context.Background(), commit, "release/1")
		require.NoError(t, err, answer)
		assert.Equal(t, c.want, got, answer)
		assert.Equal(t, c.contains, got.Contains(), answer)
		assert.Equal(t, [][]string{{"api", "repos/owner/name/compare/" + commit + "...release/1", "--jq", `[.status, .ahead_by, .behind_by] | map(tostring) | join(" ")`}}, run.Calls)
	}
}

func TestCompare_RefusesAFailedReadAnUnknownStatusACountThatIsNotANumberAndANameThatIsNotAnId(t *testing.T) {
	t.Parallel()

	down, _ := failing(t, "HTTP 502")
	_, err := down.Compare(context.Background(), commit, "main")
	require.Error(t, err)

	for _, answer := range []string{"\n", "ahead\n", "null 1 2\n", "sideways 1 2\n", "ahead null 0\n", "ahead 1 null\n", "ahead -1 0\n"} {
		odd, _ := fixed(t, answer)
		_, err = odd.Compare(context.Background(), commit, "main")
		require.ErrorContains(t, err, "no status and two counts", answer)
	}

	bad, run := fixed(t, "ahead 1 0")
	_, err = bad.Compare(context.Background(), "../x", "main")
	require.Error(t, err)
	assert.Empty(t, run.Calls)
}

func TestTreeBlobs_ListsEveryFileOfATreeAndSkipsFolders(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, `{"sha":"`+tree+`","truncated":false,"tree":[`+
		`{"path":"a.go","type":"blob","sha":"b1"},{"path":"dir","type":"tree","sha":"t1"},{"path":"dir/b.go","type":"blob","sha":"b2"},{"path":"sub","type":"commit","sha":"c1"}]}`)

	got, err := f.TreeBlobs(context.Background(), tree)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"a.go": "b1", "dir/b.go": "b2", "sub": "c1"}, got)
	assert.Equal(t, [][]string{{"api", "repos/owner/name/git/trees/" + tree + "?recursive=1"}}, run.Calls)
}

func TestTreeBlobs_RefusesATruncatedListAFailedReadABadAnswerAndANameThatIsNotAnId(t *testing.T) {
	t.Parallel()

	cut, _ := fixed(t, `{"truncated":true,"tree":[]}`)
	_, err := cut.TreeBlobs(context.Background(), tree)
	require.ErrorContains(t, err, "too large")

	down, _ := failing(t, "HTTP 502")
	_, err = down.TreeBlobs(context.Background(), tree)
	require.Error(t, err)

	bad, _ := fixed(t, "not json")
	_, err = bad.TreeBlobs(context.Background(), tree)
	require.Error(t, err)

	odd, run := fixed(t, `{}`)
	_, err = odd.TreeBlobs(context.Background(), "x?y")
	require.Error(t, err)
	assert.Empty(t, run.Calls)
}

func TestFailedLog_ReadsTheFailedStepsOfTheJobAtTheEndOfTheLink(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, "step failed\n")

	// The flag is --log-failed. The manual: https://cli.github.com/manual/gh_run_view
	got, err := f.FailedLog(context.Background(), "https://github.com/owner/name/actions/runs/77/job/88")
	require.NoError(t, err)
	assert.Equal(t, "step failed\n", got)
	assert.Equal(t, [][]string{{"run", "view", "--job", "88", "--log-failed", "--repo", "owner/name"}}, run.Calls)
}

func TestFailedLog_RefusesALinkWithNoJobNumberAndAFailedRead(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, "x")
	_, err := f.FailedLog(context.Background(), "https://ci.example/build/abc")
	require.Error(t, err)
	assert.Empty(t, run.Calls)

	down, _ := failing(t, "HTTP 502")
	_, err = down.FailedLog(context.Background(), "https://github.com/owner/name/actions/runs/77/job/88")
	require.Error(t, err)
}
