package github_test

import (
	"context"
	"maps"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/forge/github"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc/proctest"
)

const (
	tip = "0123456789abcdef0123456789abcdef01234567"
	// authText is the text answer of gh 2.101.0. The read takes JSON only, so this text is a refusal.
	authText = "github.com\n  ✓ Logged in to github.com account me (keyring)\n  - Token scopes: 'gist', 'read:org', 'repo', 'workflow'\n"
)

// The command lines below come from `gh pr merge --help` (--merge, --match-head-commit), `gh pr edit --help` (--base, --body),
// `gh auth status --help` and `gh api --help` (-X, -f), which the branch create uses on the git refs endpoint of the REST API.

func TestMergePR_MergesOnlyTheGatedHead(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, "")

	require.NoError(t, f.MergePR(context.Background(), 4, tip))
	assert.Equal(t, [][]string{{"pr", "merge", "4", "--repo", repo, "--merge", "--match-head-commit", tip}}, run.Calls)
}

func TestMergePR_RefusesAPinThatIsNotAFullCommitIdWithoutRunningGh(t *testing.T) {
	t.Parallel()

	for _, pin := range []string{"", "0123456", strings.ToUpper(tip), tip + "0", "--admin" + tip[7:]} {
		f, run := fixed(t, "")

		err := f.MergePR(context.Background(), 4, pin)
		require.Error(t, err, pin)
		assert.Contains(t, err.Error(), "cannot merge pull request 4 without the full commit id of its head", pin)
		assert.Empty(t, run.Calls, pin)
	}
}

func TestMergePR_AFailedMergeIsARefusal(t *testing.T) {
	t.Parallel()

	down, _ := failing(t, "head branch was modified")

	err := down.MergePR(context.Background(), 4, tip)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot merge pull request 4")

	var refusal *forge.Error

	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, forge.CodeRefused, refusal.Code)
}

func TestAutoMergePR_QueuesTheMergeOfTheGatedHead(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, "")

	require.NoError(t, f.AutoMergePR(context.Background(), 4, tip))
	assert.Equal(t, [][]string{{"pr", "merge", "4", "--repo", repo, "--merge", "--auto", "--match-head-commit", tip}}, run.Calls)
}

func TestAutoMergePR_RefusesAPinThatIsNotAFullCommitIdWithoutRunningGh(t *testing.T) {
	t.Parallel()

	for _, pin := range []string{"", "0123456", strings.ToUpper(tip), tip + "0"} {
		f, run := fixed(t, "")

		err := f.AutoMergePR(context.Background(), 4, pin)
		require.Error(t, err, pin)
		assert.Contains(t, err.Error(), "cannot queue the merge of pull request 4 without the full commit id of its head", pin)
		assert.Empty(t, run.Calls, pin)
	}
}

func TestAutoMergePR_AFailedCallIsARefusal(t *testing.T) {
	t.Parallel()

	down, _ := failing(t, "auto merge is not allowed")

	err := down.AutoMergePR(context.Background(), 4, tip)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot queue the merge of pull request 4")

	var refusal *forge.Error

	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, forge.CodeRefused, refusal.Code)
}

func TestDisableAutoMergePR_TurnsOffTheQueuedMerge(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, "")

	require.NoError(t, f.DisableAutoMergePR(context.Background(), 4))
	assert.Equal(t, [][]string{{"pr", "merge", "4", "--repo", repo, "--disable-auto"}}, run.Calls)
}

func TestDisableAutoMergePR_AFailedCallIsARefusal(t *testing.T) {
	t.Parallel()

	down, _ := failing(t, "no")

	err := down.DisableAutoMergePR(context.Background(), 4)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot turn off the queued merge of pull request 4")
}

// authJSON is the answer of `gh auth status --json hosts` (gh v2.101.0, pkg/cmd/auth/status/status.go, type authEntry) for one account on github.com.
func authJSON(t *testing.T, entry fields) string {
	t.Helper()

	account := fields{"state": "success", "active": true, "host": "github.com", "login": "me", "tokenSource": "GH_TOKEN", "gitProtocol": "https"}
	maps.Copy(account, entry)

	return toJSON(t, fields{"hosts": fields{"github.com": []fields{account}}})
}

func TestCanWriteCI_TheScopesOfTheActiveAccountDecideWithoutRegardToCase(t *testing.T) {
	t.Parallel()

	for scopes, want := range map[string]model.CIWrite{
		"gist, read:org, repo, workflow": model.CIWriteYes,
		"workflow":                       model.CIWriteYes,
		"repo,WORKFLOW":                  model.CIWriteYes,
		"repo, Workflow , gist":          model.CIWriteYes,
		"repo, read:org":                 model.CIWriteNo,
		"workflows, repo":                model.CIWriteNo,
		"admin:repo_hook, public_repo":   model.CIWriteNo,
	} {
		f, run := fixed(t, authJSON(t, fields{"scopes": scopes}))

		answer, err := f.CanWriteCI(context.Background())
		require.NoError(t, err, scopes)
		assert.Equal(t, want, answer, scopes)
		assert.Equal(t, [][]string{{"auth", "status", "--active", "--hostname", "github.com", "--json", "hosts"}}, run.Calls, scopes)
	}
}

func TestCanWriteCI_AnAccountWhoseScopesGitHubDoesNotShowIsUnknownNeverNo(t *testing.T) {
	t.Parallel()

	// gh leaves out the scopes when GitHub sends no X-OAuth-Scopes header, as for a fine-grained token.
	for name, entry := range map[string]fields{"no scopes field": {}, "empty scopes": {"scopes": ""}} {
		f, _ := fixed(t, authJSON(t, entry))

		answer, err := f.CanWriteCI(context.Background())
		require.NoError(t, err, name)
		assert.Equal(t, model.CIWriteUnknown, answer, name)
	}
}

func TestCanWriteCI_AnAnswerThatCannotBeReadIsARefusal(t *testing.T) {
	t.Parallel()

	requireUnread(t, map[string]model.CommandResult{
		"the text of a gh before 2.40, which does not quote the scopes": {Stdout: "github.com\n  ✓ Token scopes: gist, read:org, repo, workflow\n"},
		"the text of gh 2.101 with quoted scopes":                       {Stdout: authText},
		"a gh that has no --json flag on auth status":                   {ExitCode: 1, Stderr: "unknown flag: --json\n"},
		"no output":                      {},
		"a null answer":                  {Stdout: "null"},
		"a fatal error after the answer": {ExitCode: 1, Stdout: authJSON(t, fields{"scopes": "repo"})},
	})
}

func TestCanWriteCI_AnAnswerThatIsNotLoggedInIsARefusal(t *testing.T) {
	t.Parallel()

	requireUnread(t, map[string]model.CommandResult{
		"not logged in to any host":       {Stdout: `{"hosts":{}}`},
		"an account on another host only": {Stdout: `{"hosts":{"example.com":[{"state":"success","active":true,"scopes":"workflow"}]}}`},
		"an account that is not active":   {Stdout: authJSON(t, fields{"active": false, "scopes": "workflow"})},
		"a token that GitHub refuses":     {Stdout: authJSON(t, fields{"state": "error", "error": "HTTP 401: Bad credentials"})},
		"a timeout":                       {Stdout: authJSON(t, fields{"state": "timeout", "scopes": "repo"})},
	})
}

// requireUnread checks that each answer of gh is a refusal of the CI read, never an answer.
func requireUnread(t *testing.T, answers map[string]model.CommandResult) {
	t.Helper()

	for name, answer := range answers {
		f := newForge(t, repo, proctest.Always(answer))

		got, err := f.CanWriteCI(context.Background())

		var refusal *forge.Error

		require.ErrorAs(t, err, &refusal, name)
		assert.Equal(t, forge.CodeRefused, refusal.Code, name)
		assert.Equal(t, model.CIWriteUnknown, got, name)
	}
}

func TestCanWriteCI_ARefusalNamesWhatGitHubSaid(t *testing.T) {
	t.Parallel()

	f, _ := fixed(t, authJSON(t, fields{"state": "error", "error": "HTTP 401: Bad credentials"}))

	_, err := f.CanWriteCI(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 401: Bad credentials")
}

func TestLogin_ReadsTheLoginOfTheActiveAccount(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, "octo\n")

	login, err := f.Login(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "octo", login)
	assert.Equal(t, [][]string{{"api", "user", "--jq", ".login"}}, run.Calls)
}

func TestLogin_AFailedOrEmptyReadIsARefusal(t *testing.T) {
	t.Parallel()

	down, _ := failing(t, "HTTP 401")
	_, err := down.Login(context.Background())
	require.ErrorContains(t, err, "cannot read the login")

	empty, _ := fixed(t, "\n")
	_, err = empty.Login(context.Background())
	require.ErrorContains(t, err, "has no login")
}

func TestCreateBranch_PostsTheRefAtTheCommit(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, "")

	require.NoError(t, f.CreateBranch(context.Background(), "round/next", tip))
	assert.Equal(t, [][]string{{"api", "-X", "POST", "repos/" + repo + "/git/refs", "-f", "ref=refs/heads/round/next", "-f", "sha=" + tip}}, run.Calls)

	down, _ := failing(t, "HTTP 422")
	err := down.CreateBranch(context.Background(), "round/next", tip)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot create the branch round/next")
}

func TestSetBase_RetargetsThePullRequest(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, "")

	require.NoError(t, f.SetBase(context.Background(), 4, "round/next"))
	assert.Equal(t, [][]string{{"pr", "edit", "4", "--repo", repo, "--base", "round/next"}}, run.Calls)

	down, _ := failing(t, "HTTP 422")
	err := down.SetBase(context.Background(), 4, "round/next")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot set the base of pull request 4 to round/next")
}

// authored is a forge whose pull request 4 was written by author and whose token belongs to the login me.
func authored(t *testing.T, author string) (*github.Forge, *proctest.Scripted) {
	t.Helper()

	run := &proctest.Scripted{}
	run.Plan(toJSON(t, fields{"author": fields{"login": author}}), "pr", "view", "4")
	run.Plan("Me\n", "api", "user")
	run.Plan("", "pr", "edit")

	return newForge(t, repo, run), run
}

// requireNotAuthor fails the test unless err is the refusal of a body write on a pull request of another author.
func requireNotAuthor(t *testing.T, err error) {
	t.Helper()

	var refused *model.NotAuthorError

	require.ErrorAs(t, err, &refused)
}

func TestSetBody_SendsTheBodyAsOneArgument(t *testing.T) {
	t.Parallel()

	f, run := authored(t, "me")
	body := "--admin\nline two"

	require.NoError(t, f.SetBody(context.Background(), 4, body))
	assert.True(t, run.Called("pr", "edit", "4", "--repo", repo, "--body", body))

	run.PlanFail("HTTP 502", "pr", "edit")
	err := f.SetBody(context.Background(), 4, body)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot set the body of pull request 4")
}

func TestSetBody_WritesNothingOnAPullRequestOfAnotherAuthor(t *testing.T) {
	t.Parallel()

	f, run := authored(t, "dev")

	err := f.SetBody(context.Background(), 4, "x")

	requireNotAuthor(t, err)
	assert.Equal(t, `its author is "dev", not the owner`, err.Error())
	assert.False(t, run.Called("pr", "edit"))
}

func TestSetBody_WritesNothingWhenTheAuthorCannotBeRead(t *testing.T) {
	t.Parallel()

	f, run := authored(t, "me")
	run.PlanFail("HTTP 502", "pr", "view")

	err := f.SetBody(context.Background(), 4, "x")

	requireNotAuthor(t, err)
	assert.Contains(t, err.Error(), "cannot read its author")
	assert.False(t, run.Called("pr", "edit"))
}

func TestSetBody_WritesNothingWhenTheLoginOfTheTokenCannotBeRead(t *testing.T) {
	t.Parallel()

	f, run := authored(t, "me")
	run.PlanFail("HTTP 401", "api", "user")

	err := f.SetBody(context.Background(), 4, "x")

	requireNotAuthor(t, err)
	assert.Contains(t, err.Error(), "cannot read the login of the token")
	assert.False(t, run.Called("pr", "edit"))
}
