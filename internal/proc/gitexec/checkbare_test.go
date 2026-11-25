package gitexec_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/proc/gitexec"
)

func TestCheckBare_AFolderWithNoConfigFileAndABareRepositoryOnTheAllowListPass(t *testing.T) {
	t.Parallel()

	require.NoError(t, gitexec.CheckBare(t.Context(), t.TempDir(), proc.DefaultTimeout), "a folder with no config file")

	dir := filepath.Join(t.TempDir(), "p.git")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	plainGit(t, dir, "init", "--bare", "-q", "--template=")

	require.NoError(t, gitexec.CheckBare(t.Context(), dir, proc.DefaultTimeout))
}

func TestCheckBare_ASshCommandInTheConfigIsRefusedAndNamed(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "p.git")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	plainGit(t, dir, "init", "--bare", "-q", "--template=")
	plainGit(t, dir, "config", "core.sshCommand", "evil")

	err := gitexec.CheckBare(t.Context(), dir, proc.DefaultTimeout)

	var refusal *gitexec.SettingError

	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, []string{"core.sshcommand"}, refusal.Keys)
}

func TestCheckBare_AConfigThatGitCannotReadIsAnError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config"), []byte("[broken\n"), 0o600))

	err := gitexec.CheckBare(t.Context(), dir, proc.DefaultTimeout)

	require.ErrorContains(t, err, "read the git settings of "+dir)
}
