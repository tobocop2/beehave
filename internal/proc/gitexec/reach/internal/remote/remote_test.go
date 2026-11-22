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

func TestRunner_ReachesTheRemoteOverTheTransportOfItsAddressWithTheHooksOff(t *testing.T) {
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	script := "#!/bin/sh\necho \"$* allow=${GIT_ALLOW_PROTOCOL-unset} lazy=${GIT_NO_LAZY_FETCH-unset}\" >> " + log + "\n"
	require.NoError(t, exectest.Write(filepath.Join(bin, "git"), script))
	t.Setenv("PATH", strings.Join([]string{bin, "/usr/bin", "/bin"}, string(os.PathListSeparator)))

	remote.New(t.TempDir(), 0).Run(t.Context(), []string{"fetch"})

	data, err := os.ReadFile(log) //nolint:gosec // a file of the test
	require.NoError(t, err)
	assert.Equal(t, "-c core.hooksPath=/dev/null fetch allow=unset lazy=unset", strings.TrimSpace(string(data)))
}
