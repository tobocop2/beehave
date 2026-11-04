package host_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/proc/proctest"
)

const (
	originURL  = "git@example.test:org/repo.git"
	privateURL = "git@private.test:org/bank.git"
)

// git runs a real git command in dir and returns what it printed.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()

	full := append([]string{"-c", "user.name=t", "-c", "user.email=t@example.test", "-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main", "-C", dir}, args...)
	out, err := exec.CommandContext(t.Context(), "git", full...).CombinedOutput() //nolint:gosec // fixed binary, arguments from the test
	require.NoError(t, err, string(out))

	return strings.TrimSpace(string(out))
}

func commit(t *testing.T, dir, name string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600))
	git(t, dir, "add", name)
	git(t, dir, "commit", "-m", name)
}

// bare makes a bare repository that a URL with a host part reaches through insteadOf, as the remote of repo.
func bare(t *testing.T, repo, url string) string {
	t.Helper()

	remote := filepath.Join(t.TempDir(), "remote.git")
	git(t, t.TempDir(), "init", "--bare", remote)
	git(t, repo, "config", "url."+remote+".insteadOf", url)

	return remote
}

// banked makes a repository whose main branch is pushed to origin, a remote with a host part.
func banked(t *testing.T) string {
	t.Helper()

	repo := t.TempDir()
	git(t, repo, "init")
	commit(t, repo, "a")
	bare(t, repo, originURL)
	git(t, repo, "remote", "add", "origin", originURL)
	git(t, repo, "push", "origin", "main")

	return repo
}

func missing(t *testing.T, repo string, extra ...string) []host.Unbanked {
	t.Helper()

	items, err := host.Banked(t.Context(), proc.NewExecRunner("git", ""), []string{repo}, extra...)
	require.NoError(t, err)

	return items
}

func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

func TestBanked_Cases(t *testing.T) { //nolint:tparallel // t.Setenv forbids a parallel parent; the subtests run in parallel
	isolate(t)

	t.Run("everything_pushed_is_banked", func(t *testing.T) {
		t.Parallel()

		assert.Empty(t, missing(t, banked(t)))
	})

	t.Run("an_unpushed_branch_tip_is_unbanked", func(t *testing.T) {
		t.Parallel()

		repo := banked(t)
		git(t, repo, "switch", "-c", "feature")
		commit(t, repo, "b")

		items := missing(t, repo)

		require.Len(t, items, 1)
		assert.Equal(t, "refs/heads/feature", items[0].Ref)
		assert.Equal(t, repo, items[0].Repo)
	})

	t.Run("a_commit_behind_a_remote_head_is_banked", func(t *testing.T) {
		t.Parallel()

		repo := banked(t)
		git(t, repo, "branch", "old")
		commit(t, repo, "b")
		git(t, repo, "push", "origin", "main")

		assert.Empty(t, missing(t, repo))
	})

	t.Run("an_unpushed_tag_is_unbanked", func(t *testing.T) {
		t.Parallel()

		repo := banked(t)
		commit(t, repo, "b")
		git(t, repo, "tag", "-a", "v1", "-m", "v1")
		git(t, repo, "reset", "--hard", "HEAD~1")

		items := missing(t, repo)

		require.Len(t, items, 1)
		assert.Equal(t, "refs/tags/v1", items[0].Ref)
	})

	t.Run("a_stash_entry_is_unbanked", func(t *testing.T) {
		t.Parallel()

		repo := banked(t)
		require.NoError(t, os.WriteFile(filepath.Join(repo, "a"), []byte("changed"), 0o600))
		git(t, repo, "stash")

		items := missing(t, repo)

		require.Len(t, items, 1)
		assert.Equal(t, "stash@{0}", items[0].Ref)
	})

	t.Run("a_worktree_head_is_unbanked", func(t *testing.T) {
		t.Parallel()

		repo := banked(t)
		tree := filepath.Join(t.TempDir(), "lane")
		git(t, repo, "worktree", "add", "--detach", tree)
		commit(t, tree, "b")

		items := missing(t, repo)

		require.Len(t, items, 1)
		assert.Equal(t, "worktree HEAD", items[0].Ref)
	})

	t.Run("a_path_remote_does_not_count", func(t *testing.T) {
		t.Parallel()

		repo := t.TempDir()
		git(t, repo, "init")
		commit(t, repo, "a")
		remote := filepath.Join(t.TempDir(), "path.git")
		git(t, repo, "init", "--bare", remote)
		git(t, repo, "remote", "add", "origin", remote)
		git(t, repo, "push", "origin", "main")

		assert.Len(t, missing(t, repo), 1)
	})

	t.Run("a_file_url_remote_does_not_count", func(t *testing.T) {
		t.Parallel()

		repo := t.TempDir()
		git(t, repo, "init")
		commit(t, repo, "a")
		remote := filepath.Join(t.TempDir(), "file.git")
		git(t, repo, "init", "--bare", remote)
		git(t, repo, "remote", "add", "origin", "file://"+remote)
		git(t, repo, "push", "origin", "main")

		assert.Len(t, missing(t, repo), 1)
	})

	t.Run("a_repository_with_no_remote_is_unbanked", func(t *testing.T) {
		t.Parallel()

		repo := t.TempDir()
		git(t, repo, "init")
		commit(t, repo, "a")

		assert.Len(t, missing(t, repo), 1)
	})

	t.Run("a_directory_git_cannot_read_is_unbanked", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		items, err := host.Banked(t.Context(), proc.NewExecRunner("git", ""), []string{dir})

		require.NoError(t, err)
		requireUnreadable(t, items, dir, "git")
	})
}

func TestBanked_AnUnreadableRemoteHoldsNothing(t *testing.T) { //nolint:paralleltest // sets the git environment
	isolate(t)

	repo := t.TempDir()
	git(t, repo, "init")
	commit(t, repo, "a")
	git(t, repo, "remote", "add", "origin", "git@unreachable.invalid:org/repo.git")

	assert.Len(t, missing(t, repo), 1)
}

func TestBanked_AnExtraURLCounts(t *testing.T) { //nolint:paralleltest // sets the git environment
	isolate(t)

	repo := banked(t)
	git(t, repo, "switch", "-c", "feature")
	commit(t, repo, "b")
	remote := bare(t, repo, privateURL)
	git(t, repo, "push", remote, "feature")

	assert.Empty(t, missing(t, repo, privateURL))
	assert.Len(t, missing(t, repo), 1)
}

func refsOf(t *testing.T, remote string) []string {
	t.Helper()

	out := git(t, remote, "for-each-ref", "--format=%(refname)")
	if out == "" {
		return nil
	}

	return strings.Split(out, "\n")
}

func bankedRef(commit string) string { return "refs/heads/beehave-banked/" + commit[:12] }

func TestBank_PushesToThePrivateRemote(t *testing.T) { //nolint:paralleltest // sets the git environment
	isolate(t)

	repo := banked(t)
	git(t, repo, "switch", "-c", "feature")
	commit(t, repo, "b")
	private := bare(t, repo, privateURL)

	require.NoError(t, host.Bank(t.Context(), proc.NewExecRunner("git", ""), []string{repo}, privateURL))

	assert.Equal(t, []string{bankedRef(git(t, repo, "rev-parse", "feature"))}, refsOf(t, private), "one new branch, no tag and no other name")
}

func TestBank_NeverOverwritesABankedBranch(t *testing.T) { //nolint:paralleltest // sets the git environment
	isolate(t)

	repo := banked(t)
	git(t, repo, "switch", "-c", "side")
	commit(t, repo, "side")
	other := git(t, repo, "rev-parse", "side")
	git(t, repo, "switch", "-c", "feature", "main")
	commit(t, repo, "b")
	private := bare(t, repo, privateURL)
	name := bankedRef(git(t, repo, "rev-parse", "feature"))
	git(t, repo, "push", private, "side:"+name)
	git(t, repo, "branch", "-D", "side")

	err := host.Bank(t.Context(), proc.NewExecRunner("git", ""), []string{repo}, privateURL)

	require.ErrorIs(t, err, host.ErrUnbanked)
	assert.Equal(t, other, git(t, private, "rev-parse", name), "the existing branch keeps its commit")
}

func TestBank_BanksAnAnnotatedTagAndAStashEntry(t *testing.T) { //nolint:paralleltest // sets the git environment
	isolate(t)

	repo := banked(t)
	commit(t, repo, "b")
	git(t, repo, "tag", "-a", "v1", "-m", "v1")
	tagged := git(t, repo, "rev-parse", "HEAD")
	git(t, repo, "reset", "--hard", "HEAD~1")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "a"), []byte("changed"), 0o600))
	git(t, repo, "stash")
	stashed := git(t, repo, "rev-parse", "stash@{0}")
	private := bare(t, repo, privateURL)

	require.NoError(t, host.Bank(t.Context(), proc.NewExecRunner("git", ""), []string{repo}, privateURL))

	assert.ElementsMatch(t, []string{bankedRef(tagged), bankedRef(stashed)}, refsOf(t, private))
	assert.Empty(t, missing(t, repo, privateURL))
}

func TestBank_BanksATagOfATag(t *testing.T) { //nolint:paralleltest // sets the git environment
	isolate(t)

	repo := banked(t)
	commit(t, repo, "b")
	git(t, repo, "tag", "-a", "inner", "-m", "inner")
	git(t, repo, "tag", "-a", "outer", "-m", "outer", "inner")
	tagged := git(t, repo, "rev-parse", "HEAD")
	git(t, repo, "reset", "--hard", "HEAD~1")
	private := bare(t, repo, privateURL)

	require.NoError(t, host.Bank(t.Context(), proc.NewExecRunner("git", ""), []string{repo}, privateURL))

	assert.Equal(t, []string{bankedRef(tagged)}, refsOf(t, private), "the commit is pushed, never a tag object")
	assert.Empty(t, missing(t, repo, privateURL))
}

func TestBank_ARemoteThatIsAPathGetsNoRefEvenWhenItExists(t *testing.T) { //nolint:paralleltest // sets the git environment
	isolate(t)

	repo := banked(t)
	git(t, repo, "switch", "-c", "feature")
	commit(t, repo, "b")
	path := filepath.Join(t.TempDir(), "private.git")
	git(t, t.TempDir(), "init", "--bare", path)

	err := host.Bank(t.Context(), proc.NewExecRunner("git", ""), []string{repo}, path)

	require.ErrorIs(t, err, host.ErrUnbanked)
	assert.Empty(t, refsOf(t, path), "the push never runs against a remote with no host part")
}

func TestBanked_ARemoteTipTheCloneNeverFetchedDoesNotHideBankedWork(t *testing.T) { //nolint:paralleltest // sets the git environment
	isolate(t)

	repo := t.TempDir()
	git(t, repo, "init")
	commit(t, repo, "a")
	origin := bare(t, repo, originURL)
	git(t, repo, "remote", "add", "origin", originURL)
	git(t, repo, "push", "origin", "main")

	other := t.TempDir()
	git(t, other, "clone", origin, "work")
	work := filepath.Join(other, "work")
	git(t, work, "switch", "-c", "elsewhere")
	commit(t, work, "theirs")
	git(t, work, "push", "origin", "elsewhere")

	assert.Empty(t, missing(t, repo), "the new remote tip is not in the clone")
}

func TestBank_RefusesWithoutAPrivateRemote(t *testing.T) { //nolint:paralleltest // sets the git environment
	isolate(t)

	repo := banked(t)
	git(t, repo, "switch", "-c", "feature")
	commit(t, repo, "b")

	for _, private := range []string{"", "/srv/path.git", "file:///srv/file.git"} {
		err := host.Bank(t.Context(), proc.NewExecRunner("git", ""), []string{repo}, private)

		require.ErrorIs(t, err, host.ErrUnbanked, private)
		require.ErrorContains(t, err, "refs/heads/feature")
	}
}

func TestBank_NothingMissingNeedsNoPush(t *testing.T) { //nolint:paralleltest // sets the git environment
	isolate(t)

	require.NoError(t, host.Bank(t.Context(), proc.NewExecRunner("git", ""), []string{banked(t)}, ""))
}

func TestBank_RefusesWhenGitCannotReadTheRepository(t *testing.T) { //nolint:paralleltest // sets the git environment
	isolate(t)

	require.Error(t, host.Bank(t.Context(), proc.NewExecRunner("git", ""), []string{t.TempDir()}, privateURL))
}

func TestBank_RefusesWhenThePushFails(t *testing.T) { //nolint:paralleltest // sets the git environment
	isolate(t)

	repo := banked(t)
	git(t, repo, "switch", "-c", "feature")
	commit(t, repo, "b")

	err := host.Bank(t.Context(), proc.NewExecRunner("git", ""), []string{repo}, "git@unreachable.invalid:org/bank.git")

	require.ErrorIs(t, err, host.ErrUnbanked)
	require.ErrorContains(t, err, "push of refs/heads/feature")
}

// scriptedRepo answers the reads of one repository with one unbanked branch.
func scriptedRepo() *proctest.Scripted {
	git := &proctest.Scripted{}
	git.Plan("commit\t1111111111111111111111111111111111111111\t\t\trefs/heads/main\n", "for-each-ref", refFormat)
	git.Plan("", "--format=%(refname)")
	git.Plan("", "worktree", "list")
	git.PlanResult(model.CommandResult{ExitCode: 1}, "config", "--get-regexp")
	git.PlanResult(model.CommandResult{ExitCode: 1}, "rev-list")

	return git
}

func TestBank_RefusesWhenThePushDoesNotBankTheWork(t *testing.T) {
	t.Parallel()

	git := scriptedRepo()
	git.PlanResult(model.CommandResult{}, "push")
	git.Plan("", "ls-remote")

	err := host.Bank(t.Context(), git, []string{"/r"}, privateURL)

	require.ErrorIs(t, err, host.ErrUnbanked)
	require.ErrorContains(t, err, "/r refs/heads/main")
}

func TestBanked_EveryGitReadThatFailsLeavesTheRepositoryUnbanked(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		words []string
		stash bool
	}{
		{"refs", []string{"for-each-ref", refFormat}, false},
		{"stash_probe", []string{"--format=%(refname)"}, false},
		{"stash_log", []string{"reflog"}, true},
		{"worktrees", []string{"worktree", "list"}, false},
		{"remote_config", []string{"config", "--get-regexp"}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			git := scriptedRepo()
			if tc.stash {
				git.Plan("refs/stash\n", "--format=%(refname)")
			}

			git.PlanResult(model.CommandResult{ExitCode: 128, Stderr: "fatal"}, tc.words...)

			items, err := host.Banked(t.Context(), git, []string{"/r"})

			require.NoError(t, err)
			requireUnreadable(t, items, "/r", "exit 128")
		})
	}
}

func TestBanked_ReadsAStashLogAndSkipsAnUnbornWorktree(t *testing.T) {
	t.Parallel()

	git := scriptedRepo()
	git.Plan("refs/stash\n", "--format=%(refname)")
	git.Plan("2222222222222222222222222222222222222222\n3333333333333333333333333333333333333333\n", "reflog")
	git.Plan("worktree /r\nHEAD 0000000000000000000000000000000000000000\n\n", "worktree", "list")

	items, err := host.Banked(t.Context(), git, []string{"/r"})

	require.NoError(t, err)
	require.Len(t, items, 3)
	assert.Equal(t, "stash@{1}", items[2].Ref)
}

func TestBanked_ATagOfATagResolvesToItsCommitWhateverTheGitVersionPeels(t *testing.T) {
	t.Parallel()

	const (
		outerTag = "4444444444444444444444444444444444444444"
		tagged   = "5555555555555555555555555555555555555555"
	)

	git := scriptedRepo()
	// Git 2.43 peels one level in a for-each-ref field, so the listing alone never shows the commit of a tag of a tag.
	git.Plan("tag\t"+outerTag+"\ttag\t7777777777777777777777777777777777777777\trefs/tags/outer\ncommit\t"+localTip+"\t\t\trefs/heads/main\n", "for-each-ref", refFormat)
	git.Plan(tagged+"\n", "refs/tags/outer^{commit}")

	items, err := host.Banked(t.Context(), git, []string{"/r"})

	require.NoError(t, err)
	assert.Contains(t, items, host.Unbanked{Repo: "/r", Ref: "refs/tags/outer", Commit: tagged})
	assert.NotContains(t, items, host.Unbanked{Repo: "/r", Ref: "refs/tags/outer", Commit: outerTag})
}

func TestBanked_ATagOfATreeOrABlobHoldsNoCommitToBank(t *testing.T) {
	t.Parallel()

	git := scriptedRepo()
	listing := "tag\t4444444444444444444444444444444444444444\ttree\t6666666666666666666666666666666666666666\trefs/tags/tree\n" +
		"tag\t4444444444444444444444444444444444444444\tblob\t8888888888888888888888888888888888888888\trefs/tags/blob\n" +
		"tag\t4444444444444444444444444444444444444444\ttag\t7777777777777777777777777777777777777777\trefs/tags/nested\n" +
		"tree\t6666666666666666666666666666666666666666\t\t\trefs/tags/raw\n" +
		"commit\t" + localTip + "\t\t\trefs/heads/main\n"
	git.Plan(listing, "for-each-ref", refFormat)
	git.PlanFail("", "refs/tags/nested^{commit}")

	items, err := host.Banked(t.Context(), git, []string{"/r"})

	require.NoError(t, err)
	assert.Equal(t, []host.Unbanked{{Repo: "/r", Ref: "refs/heads/main", Commit: localTip}}, items)
}

func TestBanked_AListingLineThatCannotBeReadLeavesTheRepositoryUnbanked(t *testing.T) {
	t.Parallel()

	git := scriptedRepo()
	git.Plan("short line\n", "for-each-ref", refFormat)

	items, err := host.Banked(t.Context(), git, []string{"/r"})

	require.NoError(t, err)
	requireUnreadable(t, items, "/r", "short line")
}

func TestBanked_ManyAnnotatedTagsNeedNoPeelCall(t *testing.T) {
	t.Parallel()

	const tags = 2000

	var listing strings.Builder

	for i := range tags {
		fmt.Fprintf(&listing, "tag\t%040d\tcommit\t%040d\trefs/tags/v%d\n", i+1, i+1000000, i)
	}

	git := scriptedRepo()
	git.Plan(listing.String(), "for-each-ref", refFormat)

	items, err := host.Banked(t.Context(), git, []string{"/r"})

	require.NoError(t, err)
	assert.Equal(t, tags, len(items), "every tag commit is listed") //nolint:testifylint // Len would print 2000 items on failure
	assert.Zero(t, countCalls(git, "rev-parse"), "an annotated tag of a commit needs no call per tag")
}

const refFormat = "--format=%(objecttype)%09%(objectname)%09%(*objecttype)%09%(*objectname)%09%(refname)"

func TestBanked_APeelThatFailsForAnyOtherReasonLeavesTheRepositoryUnbanked(t *testing.T) {
	t.Parallel()

	git := scriptedRepo()
	git.Plan("tag\t4444444444444444444444444444444444444444\ttag\t7777777777777777777777777777777777777777\trefs/tags/outer\n", "for-each-ref", refFormat)
	git.PlanResult(model.CommandResult{ExitCode: 128, Stderr: "fatal"}, "refs/tags/outer^{commit}")

	items, err := host.Banked(t.Context(), git, []string{"/r"})

	require.NoError(t, err)
	requireUnreadable(t, items, "/r", "exit 128")
}

func TestBanked_ARealTagOfATreeHoldsNoCommit(t *testing.T) { //nolint:paralleltest // sets the git environment
	isolate(t)

	repo := banked(t)
	git(t, repo, "tag", "-a", "treetag", "-m", "tree", "HEAD^{tree}")

	items, err := host.Banked(t.Context(), proc.NewExecRunner("git", ""), []string{repo})

	require.NoError(t, err)
	assert.Empty(t, items)
}

const localTip = "1111111111111111111111111111111111111111"

// scriptedWithRemote is scriptedRepo with one remote that has a host part and reports the tips as its heads.
func scriptedWithRemote(tips []string) *proctest.Scripted {
	git := scriptedRepo()
	git.Plan("remote.origin.url "+originURL+"\n", "config", "--get-regexp")

	var listing strings.Builder

	for i, tip := range tips {
		listing.WriteString(tip + "\trefs/heads/b" + strconv.Itoa(i) + "\n")
	}

	git.Plan(listing.String(), "ls-remote")

	return git
}

func tipList(count int) []string {
	tips := make([]string, count)
	for i := range tips {
		tips[i] = fmt.Sprintf("%040d", i+2)
	}

	return tips
}

func TestBanked_AGitErrorOnTheAncestorCheckLeavesTheCommitUnbanked(t *testing.T) {
	t.Parallel()

	git := scriptedWithRemote(tipList(1))
	git.PlanResult(model.CommandResult{ExitCode: 128, Stderr: "fatal: bad object"}, "rev-list")

	items, err := host.Banked(t.Context(), git, []string{"/r"})

	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, localTip, items[0].Commit)
}

func TestBanked_OneCallForEachCommitHoldsEveryTip(t *testing.T) {
	t.Parallel()

	tips := tipList(3)
	git := scriptedWithRemote(tips)
	git.Plan("", "rev-list")

	items, err := host.Banked(t.Context(), git, []string{"/r"})

	require.NoError(t, err)
	assert.Empty(t, items)

	var calls [][]string

	for _, call := range git.Calls {
		if slices.Contains(call, "rev-list") {
			calls = append(calls, call)
		}
	}

	require.Len(t, calls, 1)
	assert.Equal(t, []string{"-C", "/r", "rev-list", "--ignore-missing", "-n1", localTip, "--not", tips[0], tips[1], tips[2]}, calls[0])
}

func TestBanked_ACommitThatOnlyALaterCallReachesIsBanked(t *testing.T) {
	t.Parallel()

	tips := tipList(1001)
	git := scriptedWithRemote(tips)
	git.Plan("", "rev-list")
	git.Plan(localTip+"\n", "--not", tips[0])

	items, err := host.Banked(t.Context(), git, []string{"/r"})

	require.NoError(t, err)
	assert.Empty(t, items, "the first 1000 tips do not reach the commit; the last tip does")
	assert.Equal(t, 2, countCalls(git, "rev-list"), "1001 tips need two calls")
}

func countCalls(git *proctest.Scripted, word string) int {
	count := 0

	for _, call := range git.Calls {
		if slices.Contains(call, word) {
			count++
		}
	}

	return count
}

func TestBanked_ACommitThatNoCallReachesIsUnbanked(t *testing.T) {
	t.Parallel()

	git := scriptedWithRemote(tipList(1001))
	git.Plan(localTip+"\n", "rev-list")

	items, err := host.Banked(t.Context(), git, []string{"/r"})

	require.NoError(t, err)
	assert.Len(t, items, 1)
	assert.Equal(t, 2, countCalls(git, "rev-list"))
}

func TestBanked_AFailingLsRemoteHoldsNothingEvenWhenItPrintsATip(t *testing.T) {
	t.Parallel()

	git := scriptedWithRemote(nil)
	git.PlanResult(model.CommandResult{ExitCode: 128, Stdout: localTip + "\trefs/heads/main\n", Stderr: "fatal"}, "ls-remote")
	git.Plan("", "rev-list")

	items, err := host.Banked(t.Context(), git, []string{"/r"})

	require.NoError(t, err)
	assert.Len(t, items, 1, "the line a failed ls-remote printed is not a tip")
	assert.Zero(t, countCalls(git, "rev-list"))
}

func TestBank_PutsAFenceBeforeTheRemoteOfEveryRemoteCommand(t *testing.T) {
	t.Parallel()

	git := scriptedWithRemote(nil)
	git.PlanResult(model.CommandResult{}, "push")

	err := host.Bank(t.Context(), git, []string{"/r"}, privateURL)

	require.ErrorIs(t, err, host.ErrUnbanked, "nothing lands on the scripted remote")

	assert.True(t, git.Called("push", "--", privateURL))
	assert.True(t, git.Called("--tags", "--", originURL))
	assert.True(t, git.Called("--tags", "--", privateURL))
}

// hangsOn is a git runner that never answers for one repository until its context ends, and answers the others from the script.
type hangsOn struct {
	repo  string
	inner *proctest.Scripted
}

func (h hangsOn) Run(ctx context.Context, args []string) model.CommandResult {
	if len(args) > 1 && args[1] == h.repo {
		<-ctx.Done()

		return model.CommandResult{ExitCode: -1, Stderr: ctx.Err().Error()}
	}

	return h.inner.Run(ctx, args)
}

func TestBank_ARepositoryThatHangsIsUnbankedAndTheScanGoesOn(t *testing.T) {
	t.Parallel()

	git := hangsOn{repo: "/hung", inner: scriptedRepo()}

	err := host.Bank(t.Context(), git, []string{"/hung", "/r"}, "", host.WithRepoBudget(50*time.Millisecond))

	require.ErrorIs(t, err, host.ErrUnbanked)
	require.ErrorContains(t, err, "/hung (unreadable): the time budget of 50ms ended")
	require.ErrorContains(t, err, "/r refs/heads/main", "the repository after the hung one is still read, and has no remote")
}

// requireUnreadable checks that items is one entry for repo that stands for a repository the scan could not read.
func requireUnreadable(t *testing.T, items []host.Unbanked, repo, reason string) {
	t.Helper()

	require.Len(t, items, 1)
	assert.Equal(t, repo, items[0].Repo)
	assert.Empty(t, items[0].Commit)
	assert.Contains(t, items[0].Reason, reason)
}

// slowRunner answers each call after a delay, or when its context ends.
type slowRunner struct {
	inner *proctest.Scripted
	delay time.Duration
}

func (s *slowRunner) Run(ctx context.Context, args []string) model.CommandResult {
	select {
	case <-time.After(s.delay):
		return s.inner.Run(ctx, args)
	case <-ctx.Done():
		return model.CommandResult{ExitCode: -1, Stderr: ctx.Err().Error()}
	}
}

func TestBank_ABudgetThatEndsBetweenTheCallsOfOneRepositoryStopsItsScan(t *testing.T) {
	t.Parallel()

	git := &slowRunner{inner: scriptedRepo(), delay: 30 * time.Millisecond}

	err := host.Bank(t.Context(), git, []string{"/r"}, privateURL, host.WithRepoBudget(80*time.Millisecond))

	require.ErrorIs(t, err, host.ErrUnbanked)
	require.ErrorContains(t, err, "/r (unreadable)")
	assert.False(t, git.inner.Called("push"), "an unreadable repository is never pushed from")
}

func TestBank_ARepositoryThatCannotBeReadDoesNotStopThePushOfTheOthers(t *testing.T) {
	t.Parallel()

	git := hangsOn{repo: "/hung", inner: scriptedRepo()}
	git.inner.PlanResult(model.CommandResult{}, "push")
	git.inner.Plan("", "ls-remote")

	err := host.Bank(t.Context(), git, []string{"/hung", "/r"}, privateURL, host.WithRepoBudget(50*time.Millisecond))

	require.ErrorIs(t, err, host.ErrUnbanked)
	require.ErrorContains(t, err, "/hung (unreadable)")
	assert.True(t, git.inner.Called("push", "--", privateURL), "the readable repository is pushed")
}

// hangsOnPush is a git runner whose push never answers until its context ends.
type hangsOnPush struct{ inner *proctest.Scripted }

func (h hangsOnPush) Run(ctx context.Context, args []string) model.CommandResult {
	if slices.Contains(args, "push") {
		<-ctx.Done()

		return model.CommandResult{ExitCode: -1, Stderr: ctx.Err().Error()}
	}

	return h.inner.Run(ctx, args)
}

func TestBank_APushThatHangsEndsWithTheRepositoryBudget(t *testing.T) {
	t.Parallel()

	git := hangsOnPush{inner: scriptedRepo()}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)

	defer cancel()

	start := time.Now()
	err := host.Bank(ctx, git, []string{"/r"}, privateURL, host.WithRepoBudget(50*time.Millisecond))

	require.ErrorIs(t, err, host.ErrUnbanked)
	require.ErrorContains(t, err, "the time budget of 50ms ended")
	assert.Less(t, time.Since(start), time.Second, "the budget ends the push, not the caller context")
}

func TestBank_ACancelledContextIsTheErrorNotAnUnbankedRepository(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := host.Bank(ctx, scriptedRepo(), []string{"/r"}, "")

	require.ErrorIs(t, err, context.Canceled)
	require.NotErrorIs(t, err, host.ErrUnbanked)
}

func TestWithRepoBudget_ZeroKeepsTheDefault(t *testing.T) {
	t.Parallel()

	git := hangsOn{repo: "/hung", inner: scriptedRepo()}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)

	defer cancel()

	err := host.Bank(ctx, git, []string{"/hung"}, "", host.WithRepoBudget(0))

	require.ErrorIs(t, err, context.DeadlineExceeded, "a zero budget is no budget of zero: the caller context ends the wait first")
}
