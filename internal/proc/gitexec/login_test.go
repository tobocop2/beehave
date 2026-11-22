package gitexec_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/exectest"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/proc/gitexec"
)

// fakeConfigGit puts a fake git first on PATH. It records its arguments and prints the output and exit code that the script text gives for each scope.
func fakeConfigGit(t *testing.T, body string) string {
	t.Helper()

	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	require.NoError(t, exectest.Write(filepath.Join(bin, "git"), "#!/bin/sh\necho \"$*\" >> "+log+"\n"+body))
	t.Setenv("PATH", strings.Join([]string{bin, "/usr/bin", "/bin"}, string(os.PathListSeparator)))

	return log
}

func TestReadOwnerLogin_ReturnsTheCredentialSettingsOfTheSystemFileThenTheGlobalFileAsArguments(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	log := fakeConfigGit(t, `case "$2" in
--system) printf 'credential.helper\nsystem-helper\0' ;;
--global) printf 'credential.helper\n\0credential.https://example.test.username\nme=x\0credential.usehttppath\0' ;;
esac
`)

	login, err := gitexec.ReadOwnerLogin(t.Context())

	require.NoError(t, err)
	assert.Equal(t, []string{
		"-c", "credential.helper=system-helper",
		"-c", "credential.helper=",
		"-c", "credential.https://example.test.username=me=x",
		"-c", "credential.usehttppath",
	}, login.Settings(), "the system file comes first, an empty value stays so that it clears the helpers before it, and a key with no value stays a key")
	assert.Equal(t, "config --system --null --get-regexp ^credential\\.\nconfig --global --null --get-regexp ^credential\\.", calls(t, log))
}

func TestReadOwnerLogin_AFileWithNoCredentialSettingGivesNoSetting(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	fakeConfigGit(t, "exit 1\n")

	login, err := gitexec.ReadOwnerLogin(t.Context())

	require.NoError(t, err)
	assert.Empty(t, login.Settings())
}

func TestReadOwnerLogin_AFileThatGitCannotReadIsAnErrorThatNamesTheFile(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	fakeConfigGit(t, "[ \"$2\" = --global ] && { echo 'bad config line 3' >&2; exit 128; }\nexit 1\n")

	_, err := gitexec.ReadOwnerLogin(t.Context())

	require.ErrorContains(t, err, "(--global): git exited with code 128: bad config line 3")
}

func TestReadOwnerLogin_AGitThatCannotStartIsAnError(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	_, err := gitexec.ReadOwnerLogin(t.Context())

	require.ErrorContains(t, err, "read the credential settings of the git config of the owner (--system)")
}

func TestOwnerLoginStart_KeepsTheLoginVariablesOfTheProcessAndNoConfigFileAndNoOtherGitVariable(t *testing.T) {
	log := dumpGit(t)
	t.Setenv("SSH_AUTH_SOCK", "/agent.sock")
	t.Setenv("GH_TOKEN", "owner-token")
	t.Setenv("XDG_CONFIG_HOME", "/home/owner/.config")
	t.Setenv("HOME", "/home/owner")
	t.Setenv("GIT_SSH_COMMAND", "evil")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	t.Setenv("UNRELATED", "value")

	spec := gitexec.OwnerLogin{}.Start(proc.ExecRunner{Binary: "git"}, "GIT_DIR=/x", "GIT_CONFIG_GLOBAL=/home/caller/.gitconfig")
	require.Zero(t, spec.Run(t.Context(), []string{"fetch"}).ExitCode)

	block := strings.Join(blocks(t, log)[0], "\n")

	for _, want := range []string{
		"SSH_AUTH_SOCK=/agent.sock", "GH_TOKEN=owner-token", "XDG_CONFIG_HOME=/home/owner/.config", "HOME=/home/owner", "GIT_DIR=/x",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1",
	} {
		assert.Contains(t, block, want)
	}

	for _, unwanted := range []string{"evil", "GIT_CONFIG_COUNT", "GIT_ALLOW_PROTOCOL", "UNRELATED", "/home/caller"} {
		assert.NotContains(t, block, unwanted)
	}
}
