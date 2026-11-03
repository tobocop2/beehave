package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/flow"
	"github.com/tobocop2/obee/internal/model"
)

func sandboxedProject(mode model.SandboxMode, host string) map[string]config.Project {
	return map[string]config.Project{"p": {Project: model.Project{
		Name: "p", Gate: "make check", BuildHost: host, Sandbox: mode, Host: &model.HostConfig{},
		GateNetwork: []string{"proxy.golang.org"}, GateAllowWrite: []string{"cache"},
	}}}
}

func TestGateRefusal_ASandboxedLocalProjectRefusesTheProjectGateAndAProgramGate(t *testing.T) {
	t.Parallel()

	refuse := gateRefusal(sandboxedProject(model.SandboxOn, config.LocalHost))

	for _, program := range []bool{false, true} {
		err := refuse("p", program)

		require.ErrorIs(t, err, errGateSandbox)
		require.ErrorContains(t, err, "no sandbox can enforce the gate on this machine yet")
		require.ErrorContains(t, err, `sandbox = "off"`)
	}
}

func TestGateRefusal_ARemoteProjectRefusesOnlyAProgramGate(t *testing.T) {
	t.Parallel()

	refuse := gateRefusal(sandboxedProject(model.SandboxOn, "cluster"))

	require.NoError(t, refuse("p", false), "the gate of the project runs on the host")
	require.ErrorIs(t, refuse("p", true), errGateSandbox, "a program gate runs on this machine")
}

func TestGateRefusal_ASandboxOffProjectAndAnUnknownProjectRefuseNothing(t *testing.T) {
	t.Parallel()

	refuse := gateRefusal(sandboxedProject(model.SandboxOff, config.LocalHost))

	require.NoError(t, refuse("p", false))
	require.NoError(t, refuse("p", true))
	require.NoError(t, refuse("other", true))
}

func TestProjectGate_ASandboxOffGateStillRuns(t *testing.T) {
	t.Parallel()

	calls := &gateCalls{}
	gate := projectGate{
		projects: sandboxedProject(model.SandboxOff, config.LocalHost),
		programs: programs{trees: treeAt(t.TempDir()), local: gateRunner(calls, model.CommandResult{Stdout: "abc123\n"}, model.CommandResult{Stdout: "ok"})},
	}

	_, err := gate.Run(t.Context(), "p", "1", flow.GateJob{})

	require.NoError(t, err)
	assert.Contains(t, calls.binaries, "sh")
}
