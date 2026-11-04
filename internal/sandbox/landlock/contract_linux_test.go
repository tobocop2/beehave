package landlock_test

import (
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/sandbox/contract"
	"github.com/tobocop2/beehave/internal/sandbox/landlock"
)

// TestMain lets the test binary act as the Landlock helper, as beehave does in main.
func TestMain(m *testing.M) {
	if code, helper := landlock.RunHelper(os.Args, os.Stderr); helper {
		os.Exit(code)
	}

	os.Exit(m.Run())
}

func TestEnforcer_PassesTheContractSuite(t *testing.T) {
	t.Parallel()

	abi, err := landlock.ABI()
	t.Logf("Landlock ABI reported by this kernel: %d (error: %v)", abi, err)

	if err := landlock.New().Check(); err != nil {
		t.Skipf("Landlock cannot run on this machine, so the suite has nothing to prove here: %v", err)
	}

	contract.Run(t, landlock.New())
}

// helperExit starts the test binary as the helper with a plan text and returns its exit code and what it printed.
func helperExit(t *testing.T, plan string) (int, string) {
	t.Helper()

	self, err := os.Executable()
	require.NoError(t, err)

	out, err := exec.CommandContext(t.Context(), self, "--beehave-landlock-helper", plan).CombinedOutput() //nolint:gosec // the program is the test binary

	var exited *exec.ExitError
	require.ErrorAs(t, err, &exited)

	return exited.ExitCode(), string(out)
}

func TestRunHelper_APlanThatDoesNotApplyExitsWith125AndNamesTheReason(t *testing.T) {
	t.Parallel()

	code, out := helperExit(t, "not json")
	assert.Equal(t, 125, code)
	assert.Contains(t, out, "read the sandbox plan")

	code, out = helperExit(t, `{"argv":[],"env":[]}`)
	assert.Equal(t, 125, code)
	assert.Contains(t, out, "has no command")
}

func TestRunHelper_AProgramThatIsNotFoundExitsWith127AndRunsNoCode(t *testing.T) {
	t.Parallel()

	code, out := helperExit(t, `{"argv":["beehave-no-such-program"],"env":["PATH=/usr/bin:/bin"]}`)
	assert.Equal(t, 127, code)
	assert.Contains(t, out, "beehave-no-such-program")
}

func TestRunHelper_AProgramThatIsNotStartedAsTheHelperIsLeftAlone(t *testing.T) {
	t.Parallel()

	code, helper := landlock.RunHelper([]string{"beehave", "version"}, os.Stderr)
	assert.False(t, helper)
	assert.Zero(t, code)
}
