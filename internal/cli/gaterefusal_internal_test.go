package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/model"
)

func TestGateRefusal_NilRefusesEveryGateTheSandboxCoversAndTheGivenFunctionDecidesOtherwise(t *testing.T) {
	t.Parallel()

	covered := config.Project{Project: model.Project{Sandbox: model.SandboxOn, BuildHost: config.LocalHost}}
	off := config.Project{Project: model.Project{Sandbox: model.SandboxOff, BuildHost: config.LocalHost}}

	var none GateRefusal

	assert.Equal(t, config.GateSandboxRefusal, none.reason(covered, false))
	assert.Empty(t, none.reason(off, false))

	given := GateRefusal(func(config.Project, bool) string { return "from the enforcer" })
	assert.Equal(t, "from the enforcer", given.reason(covered, false))
}
