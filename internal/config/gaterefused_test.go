package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/model"
)

func TestGateRefused_OnlyAGateOnThisMachineUnderTheSandboxIsRefused(t *testing.T) {
	t.Parallel()

	local := model.Project{BuildHost: config.LocalHost, Sandbox: model.SandboxOn}
	remote := model.Project{BuildHost: "cluster", Sandbox: model.SandboxOn}
	off := model.Project{BuildHost: config.LocalHost, Sandbox: model.SandboxOff}

	assert.True(t, config.GateRefused(local, false), "the project gate of a local project")
	assert.True(t, config.GateRefused(local, true), "a program gate")
	assert.True(t, config.GateRefused(remote, true), "a program gate runs here even when the project builds on a host")
	assert.False(t, config.GateRefused(remote, false), "the project gate of a project that builds on a host")
	assert.False(t, config.GateRefused(off, false))
	assert.False(t, config.GateRefused(off, true))
}
