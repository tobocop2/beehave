package gitexec_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/proc/gitexec"
)

func TestRoleEnv_GitAsksTheCredentialHelperOfTheCallAndTheTokenGoesByTheEnvironment(t *testing.T) {
	t.Setenv("GIT_ASKPASS", "")

	env := gitexec.RoleEnv("role-token", map[string]string{"PATH": os.Getenv("PATH"), "HOME": "/owner"})
	require.NotContains(t, strings.Join(env, "\n"), "HOME=")

	fill := exec.CommandContext(t.Context(), "git", "credential", "fill")
	fill.Env = env
	fill.Dir = filepath.Clean(t.TempDir())
	fill.Stdin = strings.NewReader("protocol=https\nhost=github.com\n\n")

	out, err := fill.Output()

	require.NoError(t, err)
	require.Contains(t, string(out), "username=x-access-token\n")
	require.Contains(t, string(out), "password=role-token\n")
}

func TestRoleEnv_TheGivenPairsComeLast(t *testing.T) {
	t.Parallel()

	env := gitexec.RoleEnv("role-token", nil, "GIT_DIR=/r")

	require.Equal(t, "GIT_DIR=/r", env[len(env)-1])
}
