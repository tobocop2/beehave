// Package reach picks the git runner for a call that reaches a remote. It is the only code that builds a runner that reaches one.
package reach

import (
	"context"
	"errors"
	"fmt"

	"github.com/tobocop2/beehave/internal/credential"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/proc/gitexec"
	"github.com/tobocop2/beehave/internal/reach/internal/remote"
)

// ErrNotLoaded is the reason a call is refused when its project declares credentials and the process holds none.
var ErrNotLoaded = errors.New("the credentials of the project are not loaded. Start the engine with beehave run")

// Call describes a git call in Dir that reaches the address Remote of Project.
// Declared is true when the project declares writer credentials. Loaded is true when this process holds them, and Token is the writer token then.
// Base is the environment that the writer call takes its names from, and Pairs are extra NAME=value pairs of the writer call.
// OwnerEnv is the environment of the call of a project with no credentials. Owner replaces the runner of such a call when it is not nil.
type Call struct {
	Project  string
	Dir      string
	Remote   string
	Declared bool
	Loaded   bool
	Token    string
	Base     map[string]string
	Pairs    []string
	OwnerEnv []string
	Owner    func(dir string) proc.Runner
}

// Runner returns the git runner for c. A project with no credentials gets the owner. A project with credentials gets the writer: its environment is built
// from the allow list, the credential helper of the call for the host of the remote, and the pairs, so no helper, token or ssh agent of the owner reaches git.
// A project with credentials that this process does not hold, or whose remote is not an https address, gets a runner that refuses the call.
func Runner(c Call) proc.Runner { //nolint:ireturn // the callers take the interface
	if !c.Loaded && !c.Declared {
		return owner(c)
	}

	if !c.Loaded {
		return refused{err: fmt.Errorf("project %s: %w", c.Project, ErrNotLoaded)}
	}

	host, err := credential.HTTPSHost(c.Remote)
	if err != nil {
		return refused{err: fmt.Errorf("project %s: %w", c.Project, err)}
	}

	runner := remote.New(c.Dir, proc.DefaultTimeout)
	runner.Only = gitexec.RoleEnv(c.Token, host, c.Base, c.Pairs...)

	return runner
}

// owner returns the runner of a call that runs with the login of the owner.
func owner(c Call) proc.Runner { //nolint:ireturn // the callers take the interface
	if c.Owner != nil {
		return c.Owner(c.Dir)
	}

	runner := remote.New(c.Dir, proc.DefaultTimeout)
	runner.Env = c.OwnerEnv

	return runner
}

// refused is the runner of a call that must not run. It runs nothing and returns the reason as the error text of a failed call.
type refused struct{ err error }

// Run returns a failed result that carries the reason.
func (r refused) Run(context.Context, []string) model.CommandResult {
	return model.CommandResult{ExitCode: 1, Stderr: r.err.Error(), SetupErr: r.err}
}
