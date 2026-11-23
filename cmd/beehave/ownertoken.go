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

// ownerTokens holds the token of the owner for each https host that gh auth token gave. A failed read is not held, so the call after a gh auth login reads again.
type ownerTokens struct {
	mu     sync.Mutex
	byHost map[string]string
}

// ownerTokenOf returns the token of the owner for host. A nil writers holds none.
func (w *writers) ownerTokenOf(host string) (string, error) {
	if w == nil {
		return "", fmt.Errorf("%w for %s", reach.ErrNoOwnerToken, host)
	}

	return w.owner.read(context.Background(), host)
}

// read returns the token for host. It runs gh auth token until a read succeeds, and then it holds the token. A call waits while another call reads.
func (o *ownerTokens) read(ctx context.Context, host string) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if token, ok := o.byHost[host]; ok {
		return token, nil
	}

	token, err := ghToken(ctx, host)
	if err != nil {
		return "", err
	}

	if o.byHost == nil {
		o.byHost = map[string]string{}
	}

	o.byHost[host] = token

	return token, nil
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
