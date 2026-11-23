package main

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/credential"
	forgegithub "github.com/tobocop2/beehave/internal/forge/github"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/proc/gitexec/reach"
)

// ownerTokens holds the token of the owner for each https host, read once with gh auth token. A failed read is held too, so a host with no gh login fails the
// same way on each call and gh runs once for it.
type ownerTokens struct {
	mu     sync.Mutex
	byHost map[string]tokenRead
}

// tokenRead is the result of one read of the token of the owner for a host.
type tokenRead struct {
	value string
	err   error
}

// ownerTokenOf returns the token of the owner for host. A nil writers holds none.
func (w *writers) ownerTokenOf(host string) (string, error) {
	if w == nil {
		return "", fmt.Errorf("%w for %s", reach.ErrNoOwnerToken, host)
	}

	return w.owner.read(context.Background(), host)
}

// read returns the token for host, and runs gh auth token for it the first time only.
func (o *ownerTokens) read(ctx context.Context, host string) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if found, ok := o.byHost[host]; ok {
		return found.value, found.err
	}

	token, err := ghToken(ctx, host)
	if o.byHost == nil {
		o.byHost = map[string]tokenRead{}
	}

	o.byHost[host] = tokenRead{value: token, err: err}

	return token, err
}

// ghToken runs gh auth token for host in the environment of the engine. gh reads its config as data and starts no program of it.
func ghToken(ctx context.Context, host string) (string, error) {
	out := proc.NewExecRunner(forgegithub.Tool, "").Run(ctx, []string{"auth", "token", "--hostname", host})

	token := strings.TrimSpace(out.Stdout)
	if out.ExitCode != 0 || token == "" {
		return "", fmt.Errorf("%w for %s. Run gh auth login --hostname %s, or declare credentials for the project", reach.ErrNoOwnerToken, host, host)
	}

	return token, nil
}

// readOwnerTokens reads the token of the owner for each https host of the projects with no credentials, so that the read comes before any step. A host that
// the projects do not name is read at its first call. A failed read stops nothing here: the call to that host fails with the reason.
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
		_, _ = w.owner.read(ctx, host) //nolint:errcheck // the failure is held and returned by the call to the host
	}
}
