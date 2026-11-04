package main

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/model"
)

// clusterNames records the cluster that each call of the host table asked for.
type clusterNames struct {
	mu    sync.Mutex
	names []string
}

func (c *clusterNames) host(hosted host.Host) func(string, string) (host.Host, error) {
	return func(_, cluster string) (host.Host, error) {
		c.mu.Lock()
		defer c.mu.Unlock()

		c.names = append(c.names, cluster)

		return hosted, nil
	}
}

func (c *clusterNames) asked() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]string(nil), c.names...)
}

// twoHostProject declares the clusters gpu (the default) and gpu-b.
func twoHostProject() map[string]config.Project {
	return map[string]config.Project{"p": {Project: model.Project{
		Name: "p", Gate: "make check", BuildHost: "gpu", BuildHosts: []string{"gpu", "gpu-b"}, Host: &model.HostConfig{},
	}}}
}

func TestProjectGate_AGateOfARunThatChoseAHostSubmitsAndWaitsOnThatHost(t *testing.T) {
	t.Parallel()

	hosted := &fakeHost{state: model.JobSucceeded}
	asked := &clusterNames{}
	gate := projectGate{
		projects: twoHostProject(),
		programs: programs{
			trees: treeAt("/tree"),
			local: gateRunner(&gateCalls{}, model.CommandResult{Stdout: "abc123"}, model.CommandResult{}),
			hosts: asked.host(hosted),
		},
		forge: forgeAt("https://example.test/repo.git"),
	}

	job, err := gate.Submit(t.Context(), "p", "1", "gpu-b")
	require.NoError(t, err)

	_, err = gate.Run(t.Context(), "p", "1", "gpu-b", job)
	require.NoError(t, err)

	assert.Equal(t, []string{"gpu-b", "gpu-b"}, asked.asked(), "the submit and the wait both use the host that the run recorded, never the default")
}

func TestProjectGate_AGateOfARunThatChoseNoHostUsesTheDefaultAndAnUndeclaredHostIsRefused(t *testing.T) {
	t.Parallel()

	hosted := &fakeHost{state: model.JobSucceeded}
	asked := &clusterNames{}
	gate := projectGate{
		projects: twoHostProject(),
		programs: programs{
			trees: treeAt("/tree"),
			local: gateRunner(&gateCalls{}, model.CommandResult{Stdout: "abc123"}, model.CommandResult{}),
			hosts: asked.host(hosted),
		},
		forge: forgeAt("https://example.test/repo.git"),
	}

	_, err := gate.Submit(t.Context(), "p", "1", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"gpu"}, asked.asked())

	_, err = gate.Submit(t.Context(), "p", "1", "rogue")
	require.ErrorIs(t, err, model.ErrUndeclaredHost)
	assert.Len(t, asked.asked(), 1, "a refused host starts no job")
}

// A run records its host in host_up. If the project file drops that host before the run resumes, the run is refused: it never moves to
// another host, because the work on the recorded host would be lost. The error names the hosts that the project declares now.
func TestProjectGate_AResumedRunWhoseRecordedHostLeftTheProjectFileIsRefusedAndStartsNothing(t *testing.T) {
	t.Parallel()

	hosted := &fakeHost{state: model.JobSucceeded}
	asked := &clusterNames{}
	projects := map[string]config.Project{"p": {Project: model.Project{Name: "p", Gate: "make check", BuildHost: "gpu", Host: &model.HostConfig{}}}}
	gate := projectGate{
		projects: projects,
		programs: programs{
			trees: treeAt("/tree"),
			local: gateRunner(&gateCalls{}, model.CommandResult{Stdout: "abc123"}, model.CommandResult{}),
			hosts: asked.host(hosted),
		},
		forge: forgeAt("https://example.test/repo.git"),
	}

	_, err := gate.Submit(t.Context(), "p", "1", "gpu-b")
	require.ErrorIs(t, err, model.ErrUndeclaredHost)
	require.ErrorContains(t, err, "use one of gpu")

	_, err = gate.Run(t.Context(), "p", "1", "gpu-b", flow.GateJob{ID: "job-1"})
	require.ErrorIs(t, err, model.ErrUndeclaredHost)
	assert.Empty(t, asked.asked(), "a refused host is never built")
}

func TestProjectGate_AWorkflowThatNamesTheLocalHostWorksOnAProjectThatBuildsHere(t *testing.T) {
	t.Parallel()

	_, err := declaredHostOf(config.Config{Projects: map[string]config.Project{"p": {Project: model.Project{Name: "p", BuildHost: "local"}}}}, "p", "local")
	require.NoError(t, err, "BUILD_HOST=local is the host of a project with no cluster")
}

func TestProjectGate_TheKeyOfAGateNamesTheHostTheRunChose(t *testing.T) {
	t.Parallel()

	gate := projectGate{
		projects: twoHostProject(),
		programs: programs{
			trees: treeAt("/tree"),
			local: gateRunner(&gateCalls{}, model.CommandResult{Stdout: "abc123"}, model.CommandResult{}),
		},
		forge: forgeAt("https://example.test/repo.git"),
	}

	onDefault, err := gate.Key(t.Context(), "p", "1", "")
	require.NoError(t, err)

	onB, err := gate.Key(t.Context(), "p", "1", "gpu-b")
	require.NoError(t, err)

	assert.NotEqual(t, onDefault.ID, onB.ID, "a pass on one host never stands in for a gate on another")
}

func TestPrograms_ACommandOnTheHostRunsOnTheHostThatTheRunChose(t *testing.T) {
	t.Parallel()

	hosted := &fakeHost{state: model.JobSucceeded}
	asked := &clusterNames{}
	runs := programs{trees: treeAt("/tree"), hosts: asked.host(hosted)}

	_, err := runs.Run(t.Context(), flow.CommandRequest{Project: "p", Item: "1", Run: []string{"make", "x"}, Host: "build", BuildHost: "gpu-b"})
	require.NoError(t, err)

	assert.Equal(t, []string{"gpu-b"}, asked.asked(), "the command goes to the recorded host, never the default")
}
