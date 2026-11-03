//go:build !linux

// Package landlock is the sandbox enforcer for Linux. On any other system it reports that it cannot run.
package landlock

import (
	"context"
	"fmt"
	"io"
	"runtime"

	"github.com/tobocop2/obee/internal/sandbox"
)

// Name is the name of this enforcer in sandbox_enforcer.
const Name = "landlock"

// Enforcer cannot run on this system.
type Enforcer struct{}

// New returns the Landlock enforcer.
func New() Enforcer { return Enforcer{} }

// Name returns Name.
func (Enforcer) Name() string { return Name }

// Check says that Landlock needs Linux.
func (Enforcer) Check() error {
	return fmt.Errorf("%w: Landlock is a Linux feature, and this is %s", sandbox.ErrUnavailable, runtime.GOOS)
}

// Run is never called: Check refuses first.
func (Enforcer) Run(context.Context, sandbox.Plan, int, sandbox.Command) (int, error) {
	return 0, fmt.Errorf("%w: Landlock is a Linux feature", sandbox.ErrUnavailable)
}

// RunHelper does nothing on this system: the program is never the helper.
func RunHelper([]string, io.Writer) (int, bool) { return 0, false }
