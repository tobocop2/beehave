package remote_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/exectest"
	"github.com/tobocop2/beehave/internal/proc/gitexec/reach/internal/remote"
)

func TestRunner_PutsItsSettingsBeforeTheCommandAndAfterTheHooksSetting(t *testing.T) {
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	require.NoError(t, exectest.Write(filepath.Join(bin, "git"), "#!/bin/sh\necho \"$*\" >> "+log+"\n"))
	t.Setenv("PATH", strings.Join([]string{bin, "/usr/bin", "/bin"}, string(os.PathListSeparator)))

	runner := remote.New(t.TempDir(), 0)
	runner.Settings = []string{"-c", "credential.helper=x"}
	args := []string{"fetch"}
	runner.Run(t.Context(), args)
	runner.Run(t.Context(), args)

	data, err := os.ReadFile(log) //nolint:gosec // a file of the test
	require.NoError(t, err)
	assert.Equal(t, "-c core.hooksPath=/dev/null -c credential.helper=x fetch\n-c core.hooksPath=/dev/null -c credential.helper=x fetch", strings.TrimSpace(string(data)))
	assert.Equal(t, []string{"-c", "credential.helper=x"}, runner.Settings, "a call leaves the settings of the runner as they were")
}
