package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentStep_TheSandboxBuiltinRefusesTheSkillsKeyword(t *testing.T) {
	t.Parallel()

	for _, extra := range []string{
		`sandbox = sandbox(skills = ["review"]), `,
		`sandbox = sandbox(network = [], skills = []), `,
	} {
		t.Run(extra, func(t *testing.T) {
			t.Parallel()

			rig := newAgentRig(t)

			_, err := rig.run(t, agentFlow(extra), "x")

			require.ErrorContains(t, err, "sandbox takes no argument skills. The prompt declares the skills of a step with the template function skill")
			assert.Empty(t, rig.h.agents.seen())
		})
	}
}
