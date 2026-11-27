package github_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/forge"
	"github.com/tobocop2/beehest/internal/forge/forgetest"
	"github.com/tobocop2/beehest/internal/model"
	"github.com/tobocop2/beehest/internal/proc/proctest"
)

const (
	conflictText = "gh: Merge conflict (HTTP 409)"
	workflowText = "refusing to allow a GitHub App to create or update workflow `.github/workflows/ci.yml` without `workflows` permission (HTTP 403)"
)

func TestSyncer_ConformsToTheSyncerRules(t *testing.T) {
	t.Parallel()

	run := &proctest.Scripted{}
	run.Plan("3\n", "api", "repos/owner/name/compare/"+forgetest.SyncBase+"..."+forgetest.SyncHead)
	run.Plan(toJSON(t, fields{"sha": forgetest.SyncMerge}), "api", "-X", "POST", "repos/owner/name/merges")
	forgetest.CheckSyncer(t, newForge(t, repo, run))

	down, _ := failing(t, "boom")
	forgetest.CheckSyncerFails(t, down)
}

func TestCommitsAhead_ReadsTheCompareEndpointOfHeadAgainstBase(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, "2\n")

	got, err := f.CommitsAhead(context.Background(), "sync/x", "main")
	require.NoError(t, err)
	assert.Equal(t, 2, got)
	assert.Equal(t, [][]string{{"api", "repos/" + repo + "/compare/sync/x...main", "--jq", ".ahead_by"}}, run.Calls)
}

func TestCommitsAhead_ZeroIsACountAndNotARefusal(t *testing.T) {
	t.Parallel()

	f, _ := fixed(t, "0\n")

	got, err := f.CommitsAhead(context.Background(), "sync/x", "main")
	require.NoError(t, err, "a base that holds the head answers 0")
	assert.Equal(t, 0, got)
}

func TestCommitsAhead_AnAnswerThatIsNotACountIsARefusalNeverZero(t *testing.T) {
	t.Parallel()

	for _, answer := range []string{"", "many", "-1"} {
		f, _ := fixed(t, answer)

		_, err := f.CommitsAhead(context.Background(), "sync/x", "main")
		require.ErrorContains(t, err, "cannot read the number of commits", answer)
	}

	down, _ := failing(t, "gh: Not Found (HTTP 404)")
	_, err := down.CommitsAhead(context.Background(), "sync/x", "main")
	require.ErrorContains(t, err, "cannot compare branch main with sync/x: gh: Not Found (HTTP 404)")
}

func TestMergeBranch_PostsHeadIntoBaseAndReturnsTheMergeCommit(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, toJSON(t, fields{"sha": tip}))

	got, err := f.MergeBranch(context.Background(), "sync/x", "main")
	require.NoError(t, err)
	assert.Equal(t, tip, got)
	assert.Equal(t, [][]string{{"api", "-X", "POST", "repos/" + repo + "/merges", "-f", "base=sync/x", "-f", "head=main"}}, run.Calls)
}

func TestMergeBranch_NothingToMergeIsAnEmptyShaAndNoError(t *testing.T) {
	t.Parallel()

	f, _ := fixed(t, "")

	got, err := f.MergeBranch(context.Background(), "sync/x", "main")
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestMergeBranch_AnAnswerWithNoShaIsARefusal(t *testing.T) {
	t.Parallel()

	for _, answer := range []string{"{}", "[1]"} {
		f, _ := fixed(t, answer)

		got, err := f.MergeBranch(context.Background(), "sync/x", "main")
		require.Error(t, err, answer)
		assert.Empty(t, got, answer)
	}
}

func TestMergeBranch_AConflictIsTheCauseMergeConflictWithTheMessageOfTheForge(t *testing.T) {
	t.Parallel()

	down, _ := failing(t, conflictText)

	_, err := down.MergeBranch(context.Background(), "sync/x", "main")
	require.ErrorIs(t, err, forge.ErrMergeConflict)
	require.NotErrorIs(t, err, forge.ErrWorkflowPermission)
	assert.EqualError(t, err, "cannot merge branch main into sync/x: "+conflictText)
}

func TestMergeBranch_AWorkflowRefusalIsTheCauseWorkflowPermissionAndNotAConflict(t *testing.T) {
	t.Parallel()

	down, _ := failing(t, workflowText)

	_, err := down.MergeBranch(context.Background(), "sync/x", "main")
	require.ErrorIs(t, err, forge.ErrWorkflowPermission)
	require.NotErrorIs(t, err, forge.ErrMergeConflict)
	assert.ErrorContains(t, err, "without `workflows` permission")
}

func TestMergeBranch_AnotherFailureHasNeitherCause(t *testing.T) {
	t.Parallel()

	down, _ := failing(t, "HTTP 502 Bad Gateway")

	_, err := down.MergeBranch(context.Background(), "sync/x", "main")
	require.Error(t, err)
	require.NotErrorIs(t, err, forge.ErrMergeConflict)
	require.NotErrorIs(t, err, forge.ErrWorkflowPermission)
	assert.ErrorContains(t, err, "HTTP 502 Bad Gateway")
}

func TestEveryWriteKeepsTheMessageOfTheForgeInItsRefusal(t *testing.T) {
	t.Parallel()

	writes := map[string]func(ctx context.Context, f forge.Activity) error{
		"MergePR":       func(ctx context.Context, f forge.Activity) error { return f.MergePR(ctx, 4, tip) },
		"CreateBranch":  func(ctx context.Context, f forge.Activity) error { return f.CreateBranch(ctx, "b", tip) },
		"SetBase":       func(ctx context.Context, f forge.Activity) error { return f.SetBase(ctx, 4, "b") },
		"PostComment":   func(ctx context.Context, f forge.Activity) error { return f.PostComment(ctx, 4, "x") },
		"SetDraftTrue":  func(ctx context.Context, f forge.Activity) error { return f.SetDraft(ctx, 4, true) },
		"SetDraftFalse": func(ctx context.Context, f forge.Activity) error { return f.SetDraft(ctx, 4, false) },
	}

	for name, write := range writes {
		down, _ := failing(t, "\n  "+workflowText+"\nsecond line")

		err := write(context.Background(), down)
		require.ErrorIs(t, err, forge.ErrWorkflowPermission, name)

		var refusal *forge.Error

		require.ErrorAs(t, err, &refusal, name)
		assert.Equal(t, workflowText, refusal.Detail, name)
		assert.Equal(t, forge.CodeRefused, refusal.Code, name)
	}
}

func TestOpenerWritesKeepTheMessageOfTheForgeInTheirRefusal(t *testing.T) {
	t.Parallel()

	down, _ := failing(t, "HTTP 422 unprocessable")

	_, err := down.CreatePR(context.Background(), model.NewPull{Head: "h", Base: "b", Title: "t"})
	require.ErrorContains(t, err, "Push the branch first: HTTP 422 unprocessable")
	require.ErrorContains(t, down.LinkStack(context.Background(), 1, 2, ""), ": HTTP 422 unprocessable")

	require.ErrorContains(t, down.AutoMergePR(context.Background(), 4, tip), ": HTTP 422 unprocessable")
	require.ErrorContains(t, down.DisableAutoMergePR(context.Background(), 4), ": HTTP 422 unprocessable")
}

func TestAFailureWithNoMessageKeepsTheOldRefusalText(t *testing.T) {
	t.Parallel()

	down, _ := failing(t, "")

	assert.EqualError(t, down.MergePR(context.Background(), 4, tip), "cannot merge pull request 4")
}
