package realtools_test

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	forgegh "github.com/tobocop2/beehave/internal/forge/github"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/store"
	storegh "github.com/tobocop2/beehave/internal/store/github"
)

// The rows of this file read and write the sandbox repository with the token in GH_TOKEN. A row skips without the token. Each row closes what it opens and removes nothing:
// the repository setting that deletes the head branch of a merged pull request keeps the branches tidy, and the owner prunes the rest by hand.

// sweepLabel marks every pull request that the suite opens, so a run that crashed leaves pull requests that the next run closes.
const sweepLabel = "real-tools"

// runs counts the calls of runID, so two tests that run in parallel never get the same name.
var runs atomic.Int64

// runID names one run of a test in the branches and the keys it makes.
func runID() string {
	return strconv.FormatInt(time.Now().UnixNano(), 10) + "-" + strconv.FormatInt(runs.Add(1), 10)
}

func sandboxForge(t *testing.T) *forgegh.Forge {
	t.Helper()

	forge, err := forgegh.NewExec(sandbox())
	require.NoError(t, err)

	return forge
}

func sandboxStore(t *testing.T) *storegh.Store {
	t.Helper()

	items, err := storegh.New(sandbox(), proc.NewExecRunner("gh", ""))
	require.NoError(t, err)

	return items
}

// later returns a context that outlives the test, for the cleanup that closes what the test opened.
func later(t *testing.T) context.Context {
	t.Helper()

	return context.WithoutCancel(t.Context())
}

func TestRealGh_ReadsOfTheSandbox(t *testing.T) {
	t.Parallel()
	requireToken(t)

	forge := sandboxForge(t)
	ctx := t.Context()

	base, err := forge.DefaultBranch(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, base)

	_, found, err := forge.BranchRef(ctx, base)
	require.NoError(t, err)
	assert.True(t, found, "the default branch of the sandbox has no ref")

	_, err = forge.OpenPulls(ctx, "")
	require.NoError(t, err)

	_, err = forge.PRList(ctx, 100, "--state", "all", "--json", "number,title")
	require.NoError(t, err)

	_, err = forge.Search(ctx, model.SearchRequest{Kind: model.SearchPulls, Involves: "@me", Since: "2020-01-01", Limit: 10})
	require.NoError(t, err)

	_, err = forge.Login(ctx)
	require.NoError(t, err)

	_, err = sandboxStore(t).ItemList(ctx, sweepLabel)
	require.NoError(t, err)
}

func TestRealGh_IssueLifecycle(t *testing.T) {
	t.Parallel()
	requireToken(t)

	items := sandboxStore(t)
	ctx := t.Context()
	key := "real-tools-" + runID()

	id, created, err := items.Create(ctx, key, store.NewItem{Title: "real-tools " + key, Body: "An issue of the real-tools suite. The suite closes it."})
	require.NoError(t, err)
	require.True(t, created)

	t.Cleanup(func() { assert.NoError(t, items.SetStatus(later(t), id, store.StatusClosed)) })

	again, createdAgain, err := items.Create(ctx, key, store.NewItem{Title: "real-tools " + key, Body: "again"})
	require.NoError(t, err)
	assert.False(t, createdAgain, "the keyed lookup finds the issue that the first create made")
	assert.Equal(t, id, again)

	require.NoError(t, items.Comment(ctx, id, key+"-note", "a note"))
	require.NoError(t, items.Comment(ctx, id, key+"-note", "a note"))

	notes, err := items.Comments(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, []string{"a note"}, notes)

	for _, status := range []store.Status{store.StatusHold, store.StatusOpen, store.StatusClosed, store.StatusOpen} {
		require.NoError(t, items.SetStatus(ctx, id, status), "status %s", status)
	}
}

// pushBranch makes a branch of the sandbox with one new file, so a pull request can be opened from it. It writes through the contents endpoint of the REST API.
func pushBranch(t *testing.T, forge *forgegh.Forge, base, branch string) {
	t.Helper()

	sha, found, err := forge.BranchRef(t.Context(), base)
	require.NoError(t, err)
	require.True(t, found)
	require.NoError(t, forge.CreateBranch(t.Context(), branch, sha))

	done := proc.NewExecRunner("gh", "").Run(t.Context(), []string{
		"api", "-X", "PUT", "repos/" + sandbox() + "/contents/" + branch + ".txt", "-f", "message=real-tools", "-f", "content=cmVhbC10b29scwo=", "-f", "branch=" + branch,
	})
	require.Zero(t, done.ExitCode, done.Stderr)
}

// openPull opens a draft pull request from a new branch and closes it when the test ends.
func openPull(t *testing.T, forge *forgegh.Forge, base, branch string) int {
	t.Helper()

	pushBranch(t, forge, base, branch)

	number, err := forge.CreatePR(t.Context(), model.NewPull{Head: branch, Base: base, Title: "real-tools " + branch, Body: "A pull request of the real-tools suite. The suite closes it.", Label: sweepLabel})
	require.NoError(t, err)

	t.Cleanup(func() { assert.NoError(t, forge.ClosePR(later(t), number, "closed by the real-tools suite")) })

	return number
}

func TestRealGh_PullLifecycle(t *testing.T) {
	t.Parallel()
	requireToken(t)

	forge := sandboxForge(t)
	ctx := t.Context()

	base, err := forge.DefaultBranch(ctx)
	require.NoError(t, err)

	number := openPull(t, forge, base, "rt/"+runID()+"/pull")

	require.NoError(t, forge.SetDraft(ctx, number, false))
	require.NoError(t, forge.SetBody(ctx, number, "An edited body of the real-tools suite."))
	require.NoError(t, forge.PostComment(ctx, number, "a comment of the real-tools suite"))

	facts, err := forge.PullRead(ctx, number)
	require.NoError(t, err)
	assert.False(t, facts.Draft, "the pull request is still a draft after SetDraft(false)")

	require.NotEmpty(t, facts.Head)
	require.NoError(t, forge.AutoMergePR(ctx, number, facts.Head), "the sandbox must allow auto-merge")
	require.NoError(t, forge.DisableAutoMergePR(ctx, number))
}

func TestRealGh_StackLinkAndUnstack(t *testing.T) {
	t.Parallel()
	requireToken(t)

	done := proc.NewExecRunner("gh", "").Run(t.Context(), []string{"stack", "--help"})
	if done.ExitCode != 0 {
		t.Skip("the gh-stack extension is not installed: scripts/install-real-tools.sh installs it (owed: whether gh 2.101.0 carries it is measured at the first live run)")
	}

	forge := sandboxForge(t)
	ctx := t.Context()
	run := runID()

	base, err := forge.DefaultBranch(ctx)
	require.NoError(t, err)

	lower := openPull(t, forge, base, "rt/"+run+"/a")
	upper := openPull(t, forge, "rt/"+run+"/a", "rt/"+run+"/b")

	require.NoError(t, forge.LinkStack(ctx, lower, upper, base))

	id, err := forge.StackID(ctx, upper)
	require.NoError(t, err)
	assert.NotEmpty(t, id, "the stack of the two pull requests has no id")

	require.NoError(t, forge.Unstack(ctx, upper))
}
