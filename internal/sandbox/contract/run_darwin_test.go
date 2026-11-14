//go:build darwin

package contract_test

import (
	"testing"

	"github.com/tobocop2/beehave/internal/sandbox/contract"
	"github.com/tobocop2/beehave/internal/sandbox/seatbelt"
)

// TestRun_ARealEnforcerPassesEverySubtest proves that Run itself runs every check and that a confining enforcer passes them.
func TestRun_ARealEnforcerPassesEverySubtest(t *testing.T) {
	t.Parallel()

	if err := seatbelt.New().Check(); err != nil {
		t.Skipf("Seatbelt cannot run on this machine: %v", err)
	}

	contract.Run(t, seatbelt.New())
}
