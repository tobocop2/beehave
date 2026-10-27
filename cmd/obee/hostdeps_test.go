package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/host"
	"github.com/tobocop2/obee/internal/host/skypilot"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/store/beads/beadstest"
)

// bareHost is a host with no Teardown.
type bareHost struct{ host.Host }

func TestAdapters_MachineBuildsAHostThatCanStopItself(t *testing.T) {
	t.Parallel()

	machine, err := newAdapters(beadstest.NewBd(t)).machine(hostProject(model.HostSkyPilot, "lane-host"))

	require.NoError(t, err)
	assert.IsType(t, &skypilot.Host{}, machine)
}

func TestAdapters_MachineRefusesAProjectWithNoHost(t *testing.T) {
	t.Parallel()

	_, err := newAdapters(beadstest.NewBd(t)).machine(model.Project{Name: "p"})

	require.EqualError(t, err, "project p: no [host] table, so no build host")
}

func TestAdapters_MachineRefusesAHostThatCannotBankAndStop(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t))
	table.hosts[model.HostSkyPilot] = func(model.Project) (host.Host, error) { return bareHost{}, nil }

	_, err := table.machine(hostProject(model.HostSkyPilot, "lane-host"))

	require.ErrorContains(t, err, "cannot bank its work and stop itself")
}
