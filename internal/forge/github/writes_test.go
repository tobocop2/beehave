package github_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/forge"
)

const (
	tip      = "0123456789abcdef0123456789abcdef01234567"
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

func TestTokenScopes_ReadsTheQuotedScopesOfTheTokenLine(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, authText)

	scopes, err := f.TokenScopes(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"gist", "read:org", "repo", "workflow"}, scopes)
	assert.Equal(t, [][]string{{"auth", "status", "--active"}}, run.Calls)
}

func TestTokenScopes_ReadsTheScopesFromStderrToo(t *testing.T) {
	t.Parallel()

	f, _ := failing(t, authText)

	scopes, err := f.TokenScopes(context.Background())
	require.NoError(t, err)
	assert.Contains(t, scopes, "workflow")
}

func TestTokenScopes_ATokenWithNoScopeLineHasNoScopes(t *testing.T) {
	t.Parallel()

	f, _ := fixed(t, "github.com\n  - Token scopes: none\n")

	scopes, err := f.TokenScopes(context.Background())
	require.NoError(t, err)
	assert.Empty(t, scopes)
}

func TestTokenScopes_AnAnswerWithNoTokenLineIsARefusal(t *testing.T) {
	t.Parallel()

	for name, stdout := range map[string]string{"empty": "", "logged out": "You are not logged into any GitHub hosts.\n"} {
		f, _ := fixed(t, stdout)

		_, err := f.TokenScopes(context.Background())
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "cannot read the token scopes", name)
	}
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

func TestSetBody_SendsTheBodyAsOneArgument(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, "")
	body := "--admin\nline two"

	require.NoError(t, f.SetBody(context.Background(), 4, body))
	assert.Equal(t, [][]string{{"pr", "edit", "4", "--repo", repo, "--body", body}}, run.Calls)

	down, _ := failing(t, "HTTP 502")
	err := down.SetBody(context.Background(), 4, body)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot set the body of pull request 4")
}
