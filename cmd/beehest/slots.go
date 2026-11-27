package main

import (
	"errors"
	"fmt"

	"github.com/tobocop2/beehest/internal/config"
	"github.com/tobocop2/beehest/internal/flow"
	"github.com/tobocop2/beehest/internal/model"
)

// errNoSlotClass is the refusal of a command call that names a slot class that the host block of its project does not list.
var errNoSlotClass = errors.New("the host block names no such slot class")

// slotsOf gives the host that a command call with a slot class runs on, and the number of jobs of the class that the host runs at once.
// The host block of the project must list the class. The config load makes every project that declares the host give the same count.
func slotsOf(projects map[string]config.Project) func(project, cluster, class string) (flow.SlotGrant, error) {
	return func(project, cluster, class string) (flow.SlotGrant, error) {
		host, err := slotHost(projects, project, cluster, class)
		if err != nil {
			return flow.SlotGrant{}, err
		}

		count, _ := config.SlotCount(projects, host, class)

		return flow.SlotGrant{Host: host, Count: count}, nil
	}
}

// slotHost gives the host of a call and checks that the host block of the project lists the class.
func slotHost(projects map[string]config.Project, project, cluster, class string) (string, error) {
	found, ok := projects[project]
	if !ok {
		return "", fmt.Errorf("%w: %q", errUnknownProject, project)
	}

	if found.Host == nil {
		return "", errNoHost
	}

	host, err := found.HostFor(cluster)
	if err != nil {
		return "", err
	}

	if class != model.GateClass && class != model.DefaultClass && found.Host.Slots[class] < 1 {
		return "", fmt.Errorf("%w: the class %q is not in `slots` of the `host` block of project %s, which runs the call on host %s. Add the class to `slots`, or remove `slots` from the call", errNoSlotClass, class, project, host)
	}

	return host, nil
}
