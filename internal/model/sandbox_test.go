package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
)

func TestParseSandboxMode_NamesOnAndOffAndRefusesTheRest(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"on", "off"} {
		mode, err := model.ParseSandboxMode(name)
		require.NoError(t, err)
		assert.Equal(t, model.SandboxMode(name), mode)
	}

	_, err := model.ParseSandboxMode("yes")
	require.ErrorIs(t, err, model.ErrUnknownSandboxMode)
}

func TestProject_OnlyAProjectThatSaysOnRunsTheSandboxByDefault(t *testing.T) {
	t.Parallel()

	assert.True(t, model.Project{Sandbox: model.SandboxOn}.SandboxByDefault())
	assert.False(t, model.Project{Sandbox: model.SandboxOff}.SandboxByDefault())
	assert.False(t, model.Project{}.SandboxByDefault())
}

func TestCheckNetwork_AcceptsDomainNamesAndRefusesEverythingElse(t *testing.T) {
	t.Parallel()

	require.NoError(t, model.CheckNetwork([]string{"api.example.org", "*.example.org", "a-b.example.co.uk"}))

	for _, bad := range []string{"", "*", "localhost", "https://example.org", "example.org/path", "example.org:443", "exa mple.org", "-a.example.org"} {
		require.Error(t, model.CheckNetwork([]string{bad}), bad)
	}
}

func TestCheckAllowWrite_RefusesAnEmptyPathAndAParentReference(t *testing.T) {
	t.Parallel()

	require.NoError(t, model.CheckAllowWrite([]string{"out", "/abs/path", "~/cache"}))
	require.Error(t, model.CheckAllowWrite([]string{""}))
	require.Error(t, model.CheckAllowWrite([]string{"  "}))
	require.Error(t, model.CheckAllowWrite([]string{"../up"}))
}

func TestCheckAllowWrite_RefusesWhatTheDesignKeepsClosedAtAnyDepth(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{
		".git", ".git/hooks", "app/.git/hooks", ".claude", "sub/.claude/settings.json", ".husky", ".githooks/pre-commit",
		".env", "app/.env", "config/.env.production", ".envrc", "/work/tree/.git", "~/.ssh", "~/.ssh/keys", "~/.claude.json", "~/.cargo/credentials.toml",
		"~/.config/obee/state", "~/Library/Keychains", "~", "~/",
	} {
		require.Error(t, model.CheckAllowWrite([]string{bad}), bad)
	}

	for _, good := range []string{"out", "app/.github", "environment", "~/.config/other", "~/cache", "/var/data", ".gitignore", ".cargo/registry"} {
		require.NoError(t, model.CheckAllowWrite([]string{good}), good)
	}
}

func TestCheckAllowWrite_ComparesNamesWithoutCase(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{".Git/hooks", ".GIT", ".CLAUDE", ".Husky", "sub/.GitHooks", ".ENV", "app/.Env.local", "~/.SSH", "~/.Config/Obee/x", "~/.CLAUDE.json"} {
		require.Error(t, model.CheckAllowWrite([]string{bad}), bad)
	}

	for _, bad := range []string{".Env", "app/.ENV", "~/.SSH", "~/.Claude.json"} {
		require.Error(t, model.CheckSandboxRead([]string{bad}), bad)
	}

	assert.Equal(t, ".Git", model.ClosedPart("x/.Git/hooks"))
}

func TestCheckSandboxRead_RefusesTheFileSystemTheHomeFolderAndCredentials(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{"/", "~", "~/", "~/.ssh", "~/.ssh/id_ed25519", "~/.config", "~/.claude.json", "/opt/app/.env", "~/Library"} {
		require.Error(t, model.CheckSandboxRead([]string{bad}), bad)
	}

	require.NoError(t, model.CheckSandboxRead([]string{"/opt/tools", "~/sdk", "~/.config/other", "/srv/environment"}))
	require.NoError(t, model.CheckSandboxRead(nil))
}
