package main

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/credential"
	forgegithub "github.com/tobocop2/beehave/internal/forge/github"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/proc/gitexec/reach"
)

// ownerTokens holds the token of the owner for each https host that gh auth token gave. A failed read is not held, so the call after a gh auth login reads again.
type ownerTokens struct {
	mu     sync.Mutex
	byHost map[string]string
	// running holds the gh run of each host that is being read, so the calls for one host share it.
	running map[string]*tokenRun
	// timeout bounds one gh run. Zero means ownerTokenTimeout.
	timeout time.Duration
	// fetch replaces the gh run in tests. Nil means ghToken.
	fetch func(ctx context.Context, host string) (string, error)
}

// ownerTokenTimeout is the longest one gh auth token run may take.
const ownerTokenTimeout = 30 * time.Second

// tokenRun is one gh run for a host. Its done channel closes when the run ends, and err is the failure, if any.
type tokenRun struct {
	done chan struct{}
	err  error
}

// ownerTokenOf returns the token of the owner for host. A nil writers holds none.
func (w *writers) ownerTokenOf(host string) (string, error) {
	if w == nil {
		return "", fmt.Errorf("%w for %s", reach.ErrNoOwnerToken, host)
	}

	return w.owner.read(context.Background(), host)
}

// read returns the token for host. It runs gh auth token until a read succeeds, and then it holds the token. The lock is never held during a gh run, so a held
// token never waits for a run. Calls for one host that fails share one run.
func (o *ownerTokens) read(ctx context.Context, host string) (string, error) {
	token, run, leader := o.begin(host)
	if run == nil {
		return token, nil
	}

	if !leader {
		return o.await(ctx, host, run)
	}

	token, err := o.fetchToken(ctx, host)
	o.finish(host, run, token, err)

	return token, err
}

// begin returns the held token of host, or the run of host. The caller that starts the run is its leader.
func (o *ownerTokens) begin(host string) (token string, run *tokenRun, leader bool) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if token, ok := o.byHost[host]; ok {
		return token, nil, false
	}

	if run, ok := o.running[host]; ok {
		return "", run, false
	}

	run = &tokenRun{done: make(chan struct{})}
	if o.running == nil {
		o.running = map[string]*tokenRun{}
	}

	o.running[host] = run

	return "", run, true
}

// finish ends the run of host. It holds the token of a run that succeeded, and wakes the calls that wait for the run.
func (o *ownerTokens) finish(host string, run *tokenRun, token string, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	delete(o.running, host)

	if err == nil {
		if o.byHost == nil {
			o.byHost = map[string]string{}
		}

		o.byHost[host] = token
	}

	run.err = err
	close(run.done)
}

// await returns the result of the run of another call for host.
func (o *ownerTokens) await(ctx context.Context, host string, run *tokenRun) (string, error) {
	select {
	case <-run.done:
	case <-ctx.Done():
		return "", fmt.Errorf("%w for %s: %w", reach.ErrNoOwnerToken, host, context.Cause(ctx))
	}

	if run.err != nil {
		return "", run.err
	}

	o.mu.Lock()
	defer o.mu.Unlock()

	return o.byHost[host], nil
}

// fetchToken runs one gh auth token for host, bounded by the timeout.
func (o *ownerTokens) fetchToken(ctx context.Context, host string) (string, error) {
	if o.fetch != nil {
		return o.fetch(ctx, host)
	}

	return ghToken(ctx, host, cmp.Or(o.timeout, ownerTokenTimeout))
}

// ghToken runs gh auth token for host in the environment of the engine, and stops it after timeout. gh reads its config as data and starts no program of it.
func ghToken(ctx context.Context, host string, timeout time.Duration) (string, error) {
	runner := proc.NewExecRunner(forgegithub.Tool, "")
	runner.Timeout = timeout
	out := runner.Run(ctx, []string{"auth", "token", "--hostname", host})

	if out.TimedOut {
		return "", fmt.Errorf("%w for %s: gh auth token timed out after %s", reach.ErrNoOwnerToken, host, timeout)
	}

	token := strings.TrimSpace(out.Stdout)
	if out.ExitCode != 0 || token == "" {
		return "", fmt.Errorf("%w for %s. Run gh auth login --hostname %s, or declare credentials for the project", reach.ErrNoOwnerToken, host, host)
	}

	return token, nil
}

// readOwnerTokens reads the token of the owner for each https host of the projects with no credentials, so that the read comes before any step. A host that
// the projects do not name is read at its first call. A failed read stops nothing here: the call to that host reads again and fails with the reason.
func (w *writers) readOwnerTokens(ctx context.Context, cfg config.Config) {
	hosts := map[string]bool{}

	for _, project := range cfg.Projects {
		if project.HasWriter() || project.Source == "" {
			continue
		}

		if host, err := credential.HTTPSHost(project.Source); err == nil {
			hosts[host] = true
		}
	}

	for _, host := range slices.Sorted(maps.Keys(hosts)) {
		_, _ = w.owner.read(ctx, host) //nolint:errcheck // the call to the host reads again and returns the failure
	}
}
