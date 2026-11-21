package agent_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/agent"
)

func TestNoUsage_IsAnUnknownReadingThatNamesTheAgent(t *testing.T) {
	t.Parallel()

	got := agent.NoUsage("robot")

	_, known := got.Usage()

	assert.False(t, known)
	assert.Equal(t, "the robot agent cannot report usage", got.Reason())
}

func TestLimit_NameNamesTheKindAndTheWindowOnlyWhenTheCLIGivesOne(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "rate limit five_hour", agent.Limit{Kind: agent.LimitRate, Window: "five_hour"}.Name())
	assert.Equal(t, "spend limit", agent.Limit{Kind: agent.LimitSpend}.Name(), "no window, no trailing space")
	assert.Equal(t, "overload", agent.Limit{Kind: agent.LimitOverload}.Name(), "an overload is no limit of a window")
}

func TestTokens_AddSumsEveryCount(t *testing.T) {
	t.Parallel()

	got := agent.Tokens{Input: 1, CachedInput: 2, Output: 3, Reasoning: 4}.Add(agent.Tokens{Input: 10, CachedInput: 20, Output: 30, Reasoning: 40})

	assert.Equal(t, agent.Tokens{Input: 11, CachedInput: 22, Output: 33, Reasoning: 44}, got)
}
