package main

import (
	"fmt"

	"github.com/tobocop2/obee/internal/cli"
	"github.com/tobocop2/obee/internal/model"
)

// machine builds the build host of a project for the host commands. A host that cannot bank its work and stop itself is refused.
func (a adapters) machine(project model.Project) (cli.Machine, error) { //nolint:ireturn // the host commands use the contract
	h, err := a.host(project)
	if err != nil {
		return nil, err
	}

	machine, ok := h.(cli.Machine)
	if !ok {
		return nil, fmt.Errorf("project %s: the %s host cannot bank its work and stop itself", project.Name, project.Host.Provider)
	}

	return machine, nil
}
