package contract_test

import (
	"os"
	"testing"

	"github.com/tobocop2/obee/internal/sandbox/contract"
	"github.com/tobocop2/obee/internal/sandbox/landlock"
)

// TestMain lets the test binary act as the Landlock helper, as obee does in main.
func TestMain(m *testing.M) {
	if code, helper := landlock.RunHelper(os.Args, os.Stderr); helper {
		os.Exit(code)
	}

	os.Exit(m.Run())
}

// TestRun_ARealEnforcerPassesEverySubtest proves that Run itself runs every check and that a confining enforcer passes them.
func TestRun_ARealEnforcerPassesEverySubtest(t *testing.T) {
	t.Parallel()

	if err := landlock.New().Check(); err != nil {
		t.Skipf("Landlock cannot run on this machine: %v", err)
	}

	contract.Run(t, landlock.New())
}
