package agent_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/agent"
)

func TestLaunch_IsEmptyOnlyWhenItAddsNothing(t *testing.T) {
	t.Parallel()

	assert.True(t, agent.Launch{}.Empty())
	assert.False(t, agent.Launch{Args: []string{"-x"}}.Empty())
	assert.False(t, agent.Launch{Env: map[string]string{"A": "b"}}.Empty())
	assert.False(t, agent.Launch{Files: map[string]string{"f": "c"}}.Empty())
}

func TestTokens_AddSumsEveryCount(t *testing.T) {
	t.Parallel()

	got := agent.Tokens{Input: 1, CachedInput: 2, Output: 3, Reasoning: 4}.Add(agent.Tokens{Input: 10, CachedInput: 20, Output: 30, Reasoning: 40})

	assert.Equal(t, agent.Tokens{Input: 11, CachedInput: 22, Output: 33, Reasoning: 44}, got)
}
