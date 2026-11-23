// Package reach picks the git runner for a call that reaches a remote. It is the only code that builds a runner that reaches one.
package reach

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/tobocop2/beehave/internal/credential"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/proc/gitexec"
	"github.com/tobocop2/beehave/internal/proc/gitexec/reach/internal/remote"
)

// ErrNotLoaded is the reason a call is refused when its project declares credentials and the process holds none.
var ErrNotLoaded = errors.New("the credentials of the project are not loaded. Start the engine with beehave run")

// ErrNoOwnerToken is the reason a call to an https remote of a project with no credentials is refused when the owner has no token for the host.
var ErrNoOwnerToken = errors.New("the owner has no gh token")

// Call describes a git call in Dir that reaches the address Remote of Project.
// Declared is true when the project declares writer credentials. Loaded is true when this process holds them, and Token is the writer token then.
// Base is the environment that the writer call takes its names from, and Pairs are extra NAME=value pairs of the writer call.
// OwnerEnv is the extra NAME=value pairs of the call of a project with no credentials. OwnerToken returns the token of the owner for an https host.
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

	OwnerToken func(host string) (string, error)
}

// Runner returns the git runner for c. A project with no credentials gets the owner. A project with credentials gets the writer: its environment is built
// from the allow list, the credential helper of the call for the host of the remote, and the pairs, so no helper, token or ssh agent of the owner reaches git.
// A project with credentials that this process does not hold, or whose remote is not an https address, gets a runner that refuses the call.
// stand replaces the owner runner in a test. Production passes nil.
func Runner(c Call, stand func(dir string) proc.Runner) proc.Runner { //nolint:ireturn // the callers take the interface
	if !c.Loaded && !c.Declared {
		return owner(c, stand)
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

// owner returns the runner of a call of a project with no credentials. It reads no git config of the owner (see ownerEnv). A non-nil stand replaces it.
func owner(c Call, stand func(dir string) proc.Runner) proc.Runner { //nolint:ireturn // the callers take the interface
	if stand != nil {
		return stand(c.Dir)
	}

	env, err := c.ownerEnv()
	if err != nil {
		return refused{err: err}
	}

	runner := remote.New(c.Dir, proc.DefaultTimeout)
	runner.Only = env

	return runner
}

// ownerEnv returns the environment of an owner call. An https address gets the token of gh for its host. Any other address gets no token.
func (c Call) ownerEnv() ([]string, error) {
	host, err := credential.HTTPSHost(c.Remote)
	if err != nil {
		return gitexec.OwnerEnv(c.Base, isSSH(c.Remote), c.OwnerEnv...), nil //nolint:nilerr // a non-https address is not an error: it takes no token
	}

	token, err := c.ownerToken(host)
	if err != nil {
		return nil, err
	}

	return gitexec.RoleEnv(token, host, c.Base, c.OwnerEnv...), nil
}

// ownerToken returns the token of the owner for host, or the reason there is none.
func (c Call) ownerToken(host string) (string, error) {
	if c.OwnerToken == nil {
		return "", fmt.Errorf("project %s: %w for %s", c.Project, ErrNoOwnerToken, host)
	}

	return c.OwnerToken(host)
}

// isSSH reports whether address is an ssh address: the ssh:// form, or the form user@host:path with no scheme.
func isSSH(address string) bool {
	if strings.HasPrefix(address, "ssh://") {
		return true
	}

	before, _, found := strings.Cut(address, ":")

	return found && !strings.Contains(address, "://") && !strings.Contains(before, "/")
}

// refused is the runner of a call that must not run. It runs nothing and returns the reason as the error text of a failed call.
type refused struct{ err error }

// Run returns a failed result that carries the reason.
func (r refused) Run(context.Context, []string) model.CommandResult {
	return model.CommandResult{ExitCode: 1, Stderr: r.err.Error(), SetupErr: r.err}
}
