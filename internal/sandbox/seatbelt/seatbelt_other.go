//go:build !darwin

package seatbelt

import (
	"context"
	"fmt"
	"runtime"

	"github.com/tobocop2/beehave/internal/sandbox"
)

// Enforcer cannot run on this system.
type Enforcer struct{}

// New returns the Seatbelt enforcer.
func New() Enforcer { return Enforcer{} }

// Name returns Name.
func (Enforcer) Name() string { return Name }

// Check says that Seatbelt needs macOS.
func (Enforcer) Check() error {
	return fmt.Errorf("%w: Seatbelt is a macOS feature, and this is %s", sandbox.ErrUnavailable, runtime.GOOS)
}

// Run is never called: Check refuses first.
func (Enforcer) Run(context.Context, sandbox.Plan, int, sandbox.Command) (int, error) {
	return 0, fmt.Errorf("%w: Seatbelt is a macOS feature", sandbox.ErrUnavailable)
}
