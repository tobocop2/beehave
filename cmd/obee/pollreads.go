package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/flow"
	"github.com/tobocop2/obee/internal/forge"
)

// errNoLogin is returned when the forge of a project cannot tell the login of the account that polls.
var errNoLogin = errors.New("this forge cannot tell the login of the active account")

// loginReader is the optional read of the login. The github forge has it; the forge contract does not list it.
type loginReader interface {
	Login(ctx context.Context) (string, error)
}

// pollReads serves the review poll: the configured projects, and the forge and the login of each one.
type pollReads struct {
	projects map[string]config.Project
	pulls    pulls
}

// Projects lists the names of the configured projects, in order.
func (p pollReads) Projects() []string {
	return slices.Sorted(maps.Keys(p.projects))
}

// Source returns the forge of the project, which the poll only reads, and the login of the active account.
// A project with no forge returns flow.ErrNoPollSource.
func (p pollReads) Source(ctx context.Context, project string) (flow.PollSource, string, error) { //nolint:ireturn // the flow package defines the read-only contract of the poll
	built, err := p.pulls.forgeOf(project)
	if errors.Is(err, forge.ErrNoForge) {
		return nil, "", flow.ErrNoPollSource
	}

	if err != nil {
		return nil, "", err
	}

	reader, ok := built.Forge.(loginReader)
	if !ok {
		return nil, "", fmt.Errorf("project %s: %w", project, errNoLogin)
	}

	login, err := reader.Login(ctx)
	if err != nil {
		return nil, "", err
	}

	return readOnlyPoll{PollSource: built}, login, nil
}

// readOnlyPoll shows only the read methods of the poll. The forge behind it keeps its write methods out of reach of a type assertion.
type readOnlyPoll struct {
	flow.PollSource
}
