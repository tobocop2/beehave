//go:build darwin

package seatbelt_test

import (
	"testing"

	"github.com/tobocop2/beehave/internal/sandbox/contract"
	"github.com/tobocop2/beehave/internal/sandbox/seatbelt"
)

func TestEnforcer_PassesTheContractSuite(t *testing.T) {
	t.Parallel()

	if err := seatbelt.New().Check(); err != nil {
		t.Skipf("Seatbelt cannot run on this machine, so the suite has nothing to prove here: %v", err)
	}

	contract.Run(t, seatbelt.New())
}
