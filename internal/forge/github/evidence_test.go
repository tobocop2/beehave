package github_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
