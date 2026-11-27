package github_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/forge/github"
)

func TestRoleRunner_AChildGetsTheRoleTokenAnEmptyConfigFolderAndNoVariableOfTheEngine(t *testing.T) {
	t.Setenv("GH_TOKEN", "owner-token")
	t.Setenv("GITHUB_TOKEN", "owner-github")
	t.Setenv("SSH_AUTH_SOCK", "/owner/agent")

	runner := github.RoleRunner("role-token", "/cfg", map[string]string{"PATH": "/usr/bin:/bin", "HOME": "/owner/home"})
	runner.Binary = "env"

	done := runner.Run(t.Context(), nil)
	lines := strings.Split(strings.TrimSpace(done.Stdout), "\n")

	require.Zero(t, done.ExitCode)
	require.Contains(t, lines, "GH_TOKEN=role-token")
	require.Contains(t, lines, "GH_CONFIG_DIR=/cfg")
	require.Contains(t, lines, "GH_PROMPT_DISABLED=1")
	require.NotContains(t, done.Stdout, "owner-")
	require.NotContains(t, done.Stdout, "SSH_AUTH_SOCK")
	require.NotContains(t, done.Stdout, "HOME=")
}
