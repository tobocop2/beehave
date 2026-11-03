//go:build darwin

package seatbelt

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/sandbox"
)

func command(env []string, argv ...string) sandbox.Command {
	return sandbox.Command{Argv: argv, Dir: "/", Env: env}
}

// writeScript writes an executable script in a new folder and returns its path.
func writeScript(t *testing.T, text string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "script")
	require.NoError(t, os.WriteFile(path, []byte(text), 0o700)) //nolint:gosec // a test script must be executable

	return path
}

var systemPath = []string{"PATH=/usr/bin:/bin"}

func TestCheck_ProvesTheConfinementHere(t *testing.T) {
	t.Parallel()

	e := New()
	assert.Equal(t, Name, e.Name())
	require.NoError(t, e.Check())
}

func TestCheck_AMissingSandboxExecMeansTheEnforcerCannotRun(t *testing.T) {
	t.Parallel()

	err := Enforcer{program: filepath.Join(t.TempDir(), "sandbox-exec")}.Check()

	require.ErrorIs(t, err, sandbox.ErrUnavailable)
	assert.Contains(t, err.Error(), "Apple deprecated it")
}

func TestCheck_AProgramThatFailsUnderTheProfileMeansTheEnforcerCannotRun(t *testing.T) {
	t.Parallel()

	err := Enforcer{program: "/usr/bin/false"}.Check()

	require.ErrorIs(t, err, sandbox.ErrUnavailable)
	assert.Contains(t, err.Error(), "cannot run a program")
}

func TestCheck_AProgramThatDoesNotConfineMeansTheEnforcerCannotRun(t *testing.T) {
	t.Parallel()

	err := Enforcer{program: "/usr/bin/true"}.Check()

	require.ErrorIs(t, err, sandbox.ErrUnavailable)
	assert.Contains(t, err.Error(), "does not refuse a write")
}

func TestCheck_AProgramThatWritesOutsideThePlanMeansTheEnforcerCannotRun(t *testing.T) {
	t.Parallel()

	// Runs the last argument as a shell line when it is a write, and fails after it, so only the file shows that the write got through.
	script := writeScript(t, `#!/bin/sh
for last; do :; done
case "$last" in echo*) sh -c "$last"; exit 1;; esac
exit 0
`)

	err := Enforcer{program: script}.Check()

	require.ErrorIs(t, err, sandbox.ErrUnavailable)
	assert.Contains(t, err.Error(), "does not refuse a write")
}

func TestCheck_AProgramThatCannotStartMeansTheEnforcerCannotRun(t *testing.T) {
	t.Parallel()

	plain := filepath.Join(t.TempDir(), "plain")
	require.NoError(t, os.WriteFile(plain, []byte("x"), 0o600))

	err := Enforcer{program: plain}.Check()
	require.ErrorIs(t, err, sandbox.ErrUnavailable)
	assert.Contains(t, err.Error(), "cannot start a program")

	// The second run fails to start: the script takes away its own permission to run after the first.
	err = Enforcer{program: writeScript(t, "#!/bin/sh\nchmod 0 \"$0\"\nexit 0\n")}.Check()
	require.ErrorIs(t, err, sandbox.ErrUnavailable)
	assert.Contains(t, err.Error(), "cannot start a program")
}

func TestCheck_ATemporaryFolderThatCannotBeMadeMeansTheEnforcerCannotRun(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "absent"))

	err := New().Check()

	require.ErrorIs(t, err, sandbox.ErrUnavailable)
	assert.Contains(t, err.Error(), "make the folder of the probe")
}

func TestRun_AnEmptyCommandIsAnError(t *testing.T) {
	t.Parallel()

	_, err := New().Run(t.Context(), sandbox.Plan{}, 0, sandbox.Command{})

	require.Error(t, err)
}

func TestRun_AProgramThatIsNotFoundGives127AndTheReasonOnStderr(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer

	cmd := command(systemPath, "obee-no-such-program")
	cmd.Stderr = &stderr

	code, err := New().Run(t.Context(), sandbox.Plan{Read: sandbox.SystemRead()}, 0, cmd)

	require.NoError(t, err)
	assert.Equal(t, 127, code)
	assert.Contains(t, stderr.String(), "obee-no-such-program")

	code, err = New().Run(t.Context(), sandbox.Plan{}, 0, command([]string{"PATH="}, "true"))
	require.NoError(t, err)
	assert.Equal(t, 127, code, "an empty PATH finds nothing, and the stderr of nobody is discarded")
}

func TestRun_AProgramWithASlashIsTakenAsItIsAndASignalGives128PlusTheNumber(t *testing.T) {
	t.Parallel()

	code, err := New().Run(t.Context(), sandbox.Plan{Read: sandbox.SystemRead()}, 0, command(systemPath, "/bin/sh", "-c", "kill -9 $$"))

	require.NoError(t, err)
	assert.Equal(t, 128+9, code)

	code, err = New().Run(t.Context(), sandbox.Plan{}, 0, command(systemPath, "./absent/prog"))
	require.NoError(t, err)
	assert.Equal(t, 127, code)
}

func TestRun_ASandboxExecThatCannotStartIsAnError(t *testing.T) {
	t.Parallel()

	_, err := Enforcer{program: filepath.Join(t.TempDir(), "absent")}.Run(t.Context(), sandbox.Plan{Read: sandbox.SystemRead()}, 0, command(systemPath, "true"))

	require.ErrorContains(t, err, "start sandbox-exec")
}
