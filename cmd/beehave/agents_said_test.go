package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/agent/claude"
	"github.com/tobocop2/beehave/internal/flow"
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

func TestSessionTap_RemovesAJSONEscapedSecretFromTheTextTheCallsAndWhatItSays(t *testing.T) {
	t.Parallel()

	const sample = `pa"ss<w>ord&ABCDEFGH`

	var said []agent.Said

	tap := newSessionTap(claude.New(nil), agent.DefaultPrices(), nil)
	tap.redact = agent.Redactor(sample)
	tap.said = func(one agent.Said) { said = append(said, one) }

	line := `{"type":"assistant","message":{"content":[{"type":"text","text":"the key is pa\"ss<w>ord&ABCDEFGH"},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"login pa\"ss<w>ord&ABCDEFGH"}}]}}`
	result := `{"type":"result","session_id":"s1","result":"the key is pa\"ss<w>ord&ABCDEFGH"}`
	_, err := tap.Write([]byte(line + "\n" + result + "\n"))
	require.NoError(t, err)

	_, text := tap.found()

	assert.Equal(t, "the key is [redacted]", text)
	assert.Equal(t, []string{"login [redacted]"}, tap.shellCalls())
	assert.Equal(t, []agent.Said{{Text: "the key is [redacted]"}, {Call: true, Text: "Bash: login [redacted]"}}, said)
}

func TestSessionTap_RemovesAJSONEscapedSecretFromEachFieldThatTheEndOfARunKeeps(t *testing.T) {
	t.Parallel()

	const (
		sample  = `pa"ss<w>ord&ABCDEFGH`
		escaped = `pa\"ss<w>ord&ABCDEFGH`
	)

	cases := map[string]struct {
		seen agent.Seen
		read func(*sessionTap) string
	}{
		"denial target": {
			agent.Seen{Denials: []agent.Denial{{Tool: "Bash", Target: "login " + escaped}}},
			func(tap *sessionTap) string { return tap.denials[0].Target },
		},
		"failure text": {
			agent.Seen{Failed: "failed with " + escaped},
			func(tap *sessionTap) string { return tap.failed },
		},
		"denial reason": {
			agent.Seen{Denied: "refused " + escaped},
			func(tap *sessionTap) string { return tap.denied },
		},
	}

	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tap := newSessionTap(claude.New(nil), agent.DefaultPrices(), nil)
			tap.redact = agent.Redactor(sample)

			tap.mu.Lock()
			tap.addEnd(test.seen)
			got := test.read(tap)
			tap.mu.Unlock()

			assert.Contains(t, got, "[redacted]")
			assert.NotContains(t, got, "ABCDEFGH")
		})
	}
}

func TestAgents_TheTapOfARunRemovesTheSecretsOfTheEngineFromWhatItKeeps(t *testing.T) {
	t.Parallel()

	tap := agents{}.tapOf(claude.New(nil), flow.AgentRequest{Redact: agent.Redactor(`pa"ss<w>ord&ABCDEFGH`)})

	_, err := tap.Write([]byte(`{"type":"result","session_id":"s1","result":"the key is pa\"ss<w>ord&ABCDEFGH"}` + "\n"))
	require.NoError(t, err)

	_, text := tap.found()

	assert.Equal(t, "the key is [redacted]", text)
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
