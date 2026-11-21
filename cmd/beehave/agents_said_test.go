package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/agent/claude"
	"github.com/tobocop2/beehave/internal/model"
)

const (
	assistantLine = `{"type":"assistant","message":{"content":[{"type":"text","text":"LINE-ONE"},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]}}`
	resultLine    = `{"type":"result","session_id":"s1","result":"LINE-ONE"}`
)

func TestSessionTap_GivesEachTextAndToolCallToTheCallbackAtTheLineThatHoldsThem(t *testing.T) {
	t.Parallel()

	var said []agent.Said

	tap := newSessionTap(claude.New(nil), agent.DefaultPrices(), nil)
	tap.said = func(one agent.Said) { said = append(said, one) }

	_, err := tap.Write([]byte(assistantLine + "\n"))
	require.NoError(t, err)
	assert.Equal(t, []agent.Said{{Text: "LINE-ONE"}, {Call: true, Text: "Bash: ls"}}, said, "they arrive before the run ends")

	_, err = tap.Write([]byte(resultLine + "\n"))
	require.NoError(t, err)
	assert.Len(t, said, 2, "the result line repeats the last message and adds nothing")
}

func TestSessionTap_WithNoSayCallbackReadsTheSameLines(t *testing.T) {
	t.Parallel()

	tap := newSessionTap(claude.New(nil), agent.DefaultPrices(), nil)

	_, err := tap.Write([]byte(assistantLine + "\n"))
	require.NoError(t, err)
	assert.Equal(t, []string{"ls"}, tap.shellCalls())
}

// sayingAdapter reads the lines "say=<text>" and "do=<call>" as what the agent said.
type sayingAdapter struct{ lineAdapter }

func (a sayingAdapter) Calls(line string) agent.Seen {
	seen := a.lineAdapter.Calls(line)

	if text, ok := cutPrefix(line, "say="); ok {
		seen.Said = []agent.Said{{Text: text}}
	}

	if text, ok := cutPrefix(line, "do="); ok {
		seen.Said = []agent.Said{{Call: true, Text: text}}
	}

	return seen
}

func TestAgents_TheRunGivesEachTextAndToolCallOfTheOutputToTheRequest(t *testing.T) {
	t.Parallel()

	f := newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited})
	f.agents.adapter = func(model.Agent) agent.Adapter { return sayingAdapter{} }
	f.extra = []string{"say=hello", "do=Bash: ls"}
	req := request()

	var said []agent.Said

	req.Said = func(one agent.Said) { said = append(said, one) }

	_, err := f.agents.Run(t.Context(), req)

	require.NoError(t, err)
	assert.Equal(t, []agent.Said{{Text: "hello"}, {Call: true, Text: "Bash: ls"}}, said)
}
