// Package gitexec builds the git commands that beehave runs in the trees it manages. Every such command runs with the hooks of the repository off, so a hook
// that the owner or the project put in a tree never runs as the owner. This package is the one place that names the git binary.
//
// A runner from New is local: it allows the file transport only and never fetches a missing object from a promisor remote, so no setting that an agent
// writes into the git config of a tree can send it to the network with the credential of the owner. The package reach builds the runners that reach a remote.
// A runner from Repositories.Runner is local too, and it names the repository of each call from the engine layout and refuses a clone setting outside the
// allow list.
package gitexec

import (
	"context"
	"slices"
	"time"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/proc/gitexec/internal/gitbin"
)

// IsGit reports whether name is the name of the git program.
func IsGit(name string) bool {
	return name == gitbin.Name
}

// local returns the environment of a local git call. GIT_ALLOW_PROTOCOL allows the file transport only, and no git config overrides it.
// GIT_NO_LAZY_FETCH stops the fetch of a missing object from a promisor remote, so a read of a missing object fails. Git 2.45 and later and the
// 2024-05 security releases from 2.39.4 on honor it. An unpatched git older than 2.45 ignores it: no promisor reaches the network, but a file
// promisor still fetches, and an upload-pack program in the clone config then runs (the class of #727).
func local() []string {
	return []string{"GIT_ALLOW_PROTOCOL=file", "GIT_NO_LAZY_FETCH=1"}
}

// Runner runs git in a folder with the hooks off. Its fields are those of proc.ExecRunner. Env comes before the local environment, so it cannot widen it.
type Runner struct {
	proc.ExecRunner

	repo Repository
	fail error
}

// Runner returns a local runner for git in the repository of dir. A run sets the repository that Of finds and never one from a file in dir, and it
// refuses a clone whose config holds a setting outside the allow list. When Of finds no repository, every run fails with the reason.
func (r Repositories) Runner(dir string, timeout time.Duration) Runner {
	runner := New(dir, timeout)
	runner.repo, runner.fail = r.Of(dir)

	return runner
}

// New returns a local runner for git in dir. A zero timeout means no limit.
func New(dir string, timeout time.Duration) Runner {
	return Runner{ExecRunner: proc.ExecRunner{Binary: gitbin.Name, Dir: dir, Timeout: timeout}}
}

// Run runs git with the hooks off. A pinned runner runs no git when its folder is unknown, or the config of its clone cannot be listed or holds a
// setting outside the allow list.
func (r Runner) Run(ctx context.Context, args []string) model.CommandResult {
	return r.run(ctx, gitbin.Args(args))
}

// ReadConfig returns the value that the git config of the repository of the runner holds for key, with the refusals of Run. It reads configuration and
// runs no hook, and the hooks setting must not reach it, so it does not use Args.
func (r Runner) ReadConfig(ctx context.Context, key string) model.CommandResult {
	return r.run(ctx, []string{"config", "--get", key})
}

// run runs git with args in the repository of the runner.
func (r Runner) run(ctx context.Context, args []string) model.CommandResult {
	pin, err := r.pinned(ctx)
	if err != nil {
		return refused(err)
	}

	runner := r.ExecRunner
	runner.Env = append(slices.Clone(r.Env), pin...)

	runner.Env = append(runner.Env, local()...)

	return runner.Run(ctx, args)
}

// pinned returns the variables that name the repository of a pinned runner, or the reason it runs no git. A runner from New names none.
func (r Runner) pinned(ctx context.Context) ([]string, error) {
	if r.fail != nil {
		return nil, r.fail
	}

	if r.repo == (Repository{}) {
		return nil, nil
	}

	if err := r.repo.check(ctx, r.Timeout); err != nil {
		return nil, err
	}

	return r.repo.env(), nil
}
