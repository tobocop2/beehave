package main

import (
	"context"
	"errors"
	"fmt"
)

var errNoTeardown = errors.New("the host of the project cannot bank its work and stop. Use a host provider that has a teardown")

// bankingHost is a host that banks its work and then stops. A provider that has no way to run git and find on its machine does not have it.
type bankingHost interface {
	Teardown(ctx context.Context) error
}

// hostStop stops the build host of a project for the host_teardown builtin. The provider banks the work first and keeps the host up when it cannot.
type hostStop struct {
	programs programs
}

// Teardown stops the host of the project. A project with no host block, or a provider with no teardown, is an error.
func (s hostStop) Teardown(ctx context.Context, project string) error {
	h, err := s.programs.hosts(project)
	if errors.Is(err, errNoHost) {
		return fmt.Errorf("project %s has no host, so there is no host to stop", project)
	}

	if err != nil {
		return err
	}

	banking, ok := h.(bankingHost)
	if !ok {
		return fmt.Errorf("project %s: %w", project, errNoTeardown)
	}

	return banking.Teardown(ctx)
}
